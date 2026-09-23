# Self-update design

Status: **proposal**. Nothing here is implemented. Written against v0.10.0.

## Why this exists now

Keystone updates components. Nobody updates Keystone.

That was an acceptable gap while every deployment had a way in: a technician, an
SSH session, a configuration-management run. The first deployment where it is
not acceptable is a fleet of gateways inside hospital networks — behind NAT, no
VPN, no reverse tunnel, no jump host. Outbound only. The provisioning tool
reaches them exactly once, from a laptop, at installation time.

In that shape, **every agent update costs a visit to every hospital**. That is
the number that justifies this work, and it is worth stating plainly because it
is the only thing that does: self-update is a liability everywhere else.

## What makes it dangerous

Three properties, and every decision below follows from them.

**The thing being replaced is the thing doing the replacing.** A component that
fails to start is a bad component; the agent is still there to notice, report
and roll back. An agent that fails to start is the end of the conversation.

**The failure is invisible from outside.** Outbound-only means nobody can look.
A gateway that reverts successfully and says nothing is operationally identical
to one that bricked, until someone drives there.

**Self-update is a legitimate backdoor.** The mechanism that lets you replace
the binary lets an attacker replace it too, and their version persists across
reboots and survives everything else. The signature check is not a nice-to-have
here; it is the entire security boundary.

## Preconditions

These are not part of self-update. They are the things that must be true first,
and three of them are defects that exist today.

### P0. The artifact downloader ignores the system proxy

`internal/artifact/download.go:132` builds an `&http.Transport{}` by hand. A
hand-built transport has `Proxy: nil` — it does not inherit
`http.DefaultTransport`, so `HTTPS_PROXY`, `HTTP_PROXY` and `NO_PROXY` are
silently ignored. On a network that requires egress through a proxy, downloads
do not take a different route: they time out, and the timeout says nothing
about a proxy.

Fix: `Proxy: http.ProxyFromEnvironment`. One line.

This is a live defect affecting ordinary artifact downloads, not a self-update
concern, and it should be fixed on its own. Checked at the same time: the health
probe (`processrunner.go:415`) and the CLI both use the default transport, which
already handles this, and Go's proxy resolution exempts loopback — so a local
health check will not be sent to a proxy. The SSH transport (`cli/ssh.go:84`)
deliberately bypasses proxies, which is correct: it is already a tunnel.

Related and worth documenting rather than coding: if the proxy inspects TLS, the
device needs the network's CA. The transport sets no `TLSClientConfig`, so it
uses the system pool — installing the CA on the device is enough. That is the
second thing people trip over after the proxy itself.

### P1. `planPath` must go

`internal/adapter/nats/nats.go:273` and the MQTT adapter accept a `planPath`
field: a filesystem path the agent will read and execute. The HTTP adapter
rejects it and names it in the code — *local-file-inclusion vector*.

While messaging adapters were optional, this was a hole you could avoid by not
opening the door. In an outbound-only deployment the messaging adapter **is**
the control plane: it is the only transport that can carry a new plan inwards,
because the periodic reconcile re-applies the plan in force rather than
fetching a new one. So the channel that will carry *"update yourself to version
X"* is today the same channel that accepts *"execute the file at this path"*.

Recommendation: **remove the field. No flag.** A `--allow-plan-path` escape
turns a vulnerability into a documented option, and whoever hits the error will
eventually set it. "Trusted transport" does not survive contact with the fact
that the same agent refuses the same thing over HTTP.

Migration: send `content`. Both adapters already support it, and the MQTT apply
message carries inline `Recipes`, so a recipe and a plan can be pushed
atomically.

### P2. Commands have no replay protection

The command message carries `correlationId`, `planPath`, `content` and
`recipes`. No timestamp, no nonce, no expiry, and no deduplication in the
handler. Commands are subscribed at **QoS 1**, which is at-least-once *by
design*: a duplicate `apply` is the protocol working correctly.

Two nuances that change the shape of the problem:

- With the default `CleanSession: true`, a broker does not queue QoS 1 messages
  for a disconnected client. So "a gateway returns after two weeks and executes
  a two-week-old order" **does not apply to ordinary publishes**. It applies to
  **retained** messages, which are redelivered on every subscribe, forever.
  Never publishing commands with `retain` belongs in the operator runbook.
- The obvious fix for "the gateway misses orders while it has no coverage" —
  which is the normal condition in a hospital — is `CleanSession: false`. That
  is precisely the setting that opens a wide replay window. **The operator's
  natural next step is the change that makes the channel dangerous.**

So replay protection is not defensive paranoia; it is what makes the useful
configuration safe. Design: orders carry an issue time and a validity window,
the agent refuses anything outside it, and deduplicates by identifier. There is
precedent to copy rather than invent — dataset manifests already enforce an
anti-replay rule (`manifest verify --since`).

For an `apply` a replayed command is an annoyance. For "update yourself" it
means reinstalling a version that was withdrawn for being bad.

### P3. The agent does not report its own version

There is no topic on which a gateway says what build it is running. The only
`version` that travels is a recipe's; the LWT publishes `online`/`offline`.

With outbound-only access this is already a gap, before any self-update exists:
**you cannot know what version runs in each hospital without going there.** It
is the input to deciding who to update, to knowing whether an update landed, and
to discovering that a gateway has sat on a withdrawn version for three months.

It is cheap next to the rest of this document, it improves operations on its own,
and without it self-update is flown blind.

## The design

### Layout

```
/opt/keystone/
  versions/
    v0.10.0/keystone
    v0.11.0/keystone
  current -> versions/v0.11.0      # swapped with rename(2), atomic
  state/
    update.json                    # boot counter, pending version, last failure
```

Writing over a running binary returns `ETXTBSY`; replacing the *path* is fine,
because the running process holds the old inode open. Download to a temporary
file, verify, place it under `versions/`, and only then move the symlink. A
power cut at any point leaves either the old version or the new one, never half
of one — which matters on hardware whose power supply has already been a
problem.

**Implemented** in `internal/selfupdate`. Three properties are worth naming
because each closes a way of ending up with no agent at all:

- **The switch never leaves a gap.** `os.Symlink` cannot replace an existing
  link, and the obvious remove-then-create leaves a window in which `current`
  does not exist — a restart landing there has nothing to start. Activation
  builds a temporary link and renames it over the old one. Pinned by a test
  that hammers the path while 50 activations run underneath it.
- **A version directory is renamed into place**, so an interrupted install
  cannot leave a half-populated directory that looks installed.
- **The architecture is checked before the swap**, from the ELF header. The
  wrong build for the board is otherwise indistinguishable from a corrupt one:
  both fail to execute, and both are discovered only afterwards, by burning
  restarts against the boot counter. Verified against real cross-compiled
  binaries: `binary is for arm64, this device is amd64`.

Pruning takes the versions to keep by name rather than a count. "Keep the
newest two" needs a version ordering this package deliberately does not invent
— the operator chose the scheme — and the two that matter are the running one
and the one to fall back to, which the caller knows and a sort does not.

### Who restarts, and who watches

**systemd.** `Type=notify`, `Restart=always`, `ExecStart=/opt/keystone/current/keystone`.

The agent does not restart itself: it exits with an agreed code and systemd
starts it again, resolving the symlink afresh. The watchdog is PID 1, which is
not being updated at the same time.

**The rollback decision cannot live in the new binary**, and this is the crux.
A binary that segfaults, has the wrong architecture, or is missing a library
never gets to run its own rollback logic. So the decision goes in
`ExecStartPre`, as a small script that does not change with updates:

1. Read the boot counter from `update.json`.
2. If it exceeds N and there is no confirmation, point `current` back at the
   previous version and record why.
3. Otherwise increment it and let systemd proceed.

Deliberately not the new binary, and deliberately not systemd's own
`StartLimitBurst`: that stops trying and leaves the unit dead, which is the
outcome we are trying to avoid. Our counter must revert *before* systemd's
limit is reached, so the two numbers have to be set together.

### Confirmation

The new agent clears the counter only when it has, in order:

1. Converged — the plan applied and its components healthy.
2. **Reconnected to the broker and published its status.**

The second condition is not optional, and it is the one that a design written
for a normal network would omit. A new version can start correctly, supervise
correctly, and have broken its MQTT reconnect — a library bump, a stricter TLS
default, a duplicated client id. It converges, confirms itself, and the gateway
is *healthy by its own account and mute to you*. Nobody can order it back,
because the order travels over the channel that broke. Rollback has to be able
to happen with nobody asking for it, because by definition nobody can.

**Sizing the timeout.** Until component re-adoption exists (below), an agent
restart also restarts everything it supervises, so "converged" is the startup
time of the whole plan, not of the binary. Tuning the counter against a binary
start will revert good updates for being slow.

### Verifying the download

Reuse what the agent already has: a **signed manifest verified against the
device's trust bundle**, the same machinery as any other artifact — resume,
optional deltas, fail-closed verification.

Explicitly **not** cosign, even though releases are signed with it. A hospital
gateway reaches an allow-list of our own domains; Sigstore and Rekor are not on
it, and a verification that depends on reaching them fails exactly where it is
needed most. The cosign signature serves humans downloading by hand and CI. Two
audiences, two mechanisms, and the offline one belongs on the device.

The manifest names the target architecture; the agent checks it against its own
before the swap. "Wrong binary for the board" is otherwise indistinguishable
from "binary is corrupt", and both end at the boot counter — which works, but
wastes three reboots discovering something knowable in advance.

### Agent state is data, and rollback does not revert data

Keystone persists state under `runtime/state/`. A new version may change that
format. If it updates, writes new state, and then reverts, the old binary finds
a snapshot it does not understand.

This is exactly the hazard [#49](https://github.com/carlosprados/keystone/pull/49)
closed for components — and that guardrail protects components, not the agent.

Cheap now, expensive in the field: version the snapshot format, and have startup
refuse a snapshot from a newer version, starting clean and saying so rather than
misreading it. It has to ship *with* the binary swap, not after.

**Implemented.** `state.CurrentSchemaVersion` is stamped by `Save` rather than
trusted from the caller, and `Load` refuses anything newer with
`ErrSnapshotFromNewerAgent`, returning an empty snapshot rather than a
half-read one. Startup says so loudly — a device that comes up with no plan
because its state could not be read looks exactly like one that was never given
a plan, and the two need different fixing. Snapshots written before versioning
carry no field and are still read: the format did not change, it only became
explicit, so refusing them would wipe every existing device on upgrade.

### Telemetry

Outbound-only means a successful rollback is silent. Reliable rollback plus total
blindness is close to worse than no rollback, because the fleet looks fine.

The agent reports, on the existing periodic event: agent version, plan version in
force, update state (idle / downloading / pending-confirmation / rolled-back) and
the reason for the last reversion.

### Rings

Percentage rings are meaningless for tens of gateways segmented by customer. Use
declarative cohorts instead: the device carries a ring label
(`canary` / `early` / `general`), the agent only obeys "version X is available
for your ring", and **advancing a ring is a control-plane decision, never the
agent's**. The natural canary is one gateway per site.

Last in the order. Without reliable reversion, rings only spread the same risk
over more batches.

## Alternatives considered

**Re-exec in place (`syscall.Exec`).** Keeps the PID, so supervised children
stay children. Rejected as the primary mechanism: it cannot recover from a
binary that fails to execute, which is the failure that strands a device. Also
loses the log pipes and handles, so it needs re-adoption anyway.

**A separate updater binary.** Moves the problem: who updates the updater. A
small script invoked by systemd is the same idea with less to go wrong, because
`ExecStartPre` is already run by something we are not replacing.

**Letting the layer below do it** — a package, an image, configuration
management. The right answer nearly everywhere, and the reason this document
would not exist for a lab machine. It fails only under the specific constraint
that nothing can reach in.

**systemd's `StartLimitBurst` as the rollback trigger.** It stops restarting and
leaves the unit dead. We need a revert, not a stop.

## What this does not solve

- **No systemd, no self-update.** The design leans on PID 1 as the watchdog. On
  a device without it the feature should be refused rather than half-implemented
  — the project's existing rule: a declaration is honoured or refused.
- **Component re-adoption is separate work.** Until it exists, updating the
  agent restarts everything it supervises. That is acceptable for a poller with
  backfill and unacceptable for a component holding unrecoverable in-memory
  state, and it must be documented as known behaviour of this feature rather
  than discovered.
- **A compromised control plane can push a signed-but-hostile version** if it
  also holds the signing key. Self-update narrows the blast radius of a lost
  device and widens the blast radius of a lost key. That trade is the reason the
  key does not live on the devices.
- **It does not make a bad release safe.** Rings and canaries reduce how many
  devices meet it; they do not reduce the chance of writing one.

## Open decisions

1. **TLS on the command channel.** The broker is ours, but the deployment
   reviewed here uses port 1883 in the clear, with credentials in the same
   plaintext. Debatable for telemetry; not debatable for the channel carrying
   "apply this plan", and less so for one carrying "replace your own binary".
2. **Publish ACL.** A device must be subscribe-only on its command topics. If a
   compromised gateway can publish to a command topic — its own, or worse
   another's — the ACL is decoration. This is what makes sharing a broker with
   the data plane acceptable.
3. **How many versions to keep**, and who garbage-collects them on a device with
   a small disk.
4. **Whether the agent runs as root for this.** It must write to a directory it
   should not otherwise be able to write to. A compromised agent that can
   rewrite its own binary is permanent.
