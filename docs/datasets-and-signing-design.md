# Signed datasets: design

Two changes that ship as one chain of trust: a local signing tool
(`keystonectl sign`, `keystonectl manifest`) and a new kind of artifact whose
version is discovered rather than declared — a **dataset**.

They are documented together because neither is useful alone. A dataset is
authenticated by a signed manifest, and there is currently no way to produce
one. The driving use case is a device-discovery product for OT networks that
must keep two moving datasets fresh: the IEEE OUI/MA-L list, and a
consolidated vulnerability bundle published daily.

Status: **both parts are implemented.** `internal/signing`, `internal/manifest`
and the `keystonectl sign|verify|manifest` commands cover Part 1;
`internal/dataset` plus the dataset lifecycle in `internal/agent` cover Part 2,
with `[[datasets]]` and `[lifecycle.reload]` in the recipe, `GET /v1/datasets`
and `POST /v1/datasets:refresh` on the API.

What is still open is listed at the end.

---

## Why the current model cannot do this

KeyStone assumes an artifact is *immutable and identified by a digest declared
in a signed recipe*. That assumption is correct for code and load-bearing
everywhere. It is exactly wrong for a dataset that changes every day, and it
fails in four separate places:

| Where | What happens |
|---|---|
| `artifact.Ensure` (`internal/artifact/manager.go:84-95`) | The cache is keyed by URI with no expiry. If the index has an entry and the file exists, it returns the cached copy and issues no request. For a stable URL, the first download is the last one, permanently |
| `agent.go:900` | The unpack marker is `.unpacked-<basename>`. Same URL, same basename, marker present — no re-extraction, whatever changed underneath |
| `agent.go:506-511` | The install hook short-circuits on an `.installed` marker, so an ingest step never runs a second time |
| `ensureAndVerifyArtifact` (`agent.go:855-865`) | In secure mode `sha256` **and** `sig_uri` are mandatory. The digest lives in the recipe, and the recipe is signed |

That last one is the real knot. A daily digest inside a signed recipe means
re-signing a recipe every day and bumping `metadata.version`, which makes
`componentChanged` (`internal/agent/plan_reconcile.go:436-467`) see a new
`recipeID`/`recipeDigest` and stop-start the component — cascading to every
dependent. A discovery engine watching an industrial network cannot restart
nightly because a vulnerability feed arrived.

There is also no conditional request anywhere: `download.go` sends `Range` to
resume, never `ETag`/`If-None-Match`. Today nothing in the agent can even ask
"has this changed?"

**A periodic reconcile does not fix any of this.** The clock was never the
missing piece; the artifact type was.

---

# Part 1 — Signing

## What exists today

`internal/security` exposes two functions — `LoadTrustBundle` and
`VerifyDetached` (`verify.go:20,38`). There is no signing code in Go anywhere in
the repository. Signing is `openssl dgst -sha256 -sign` by hand, or
`scripts/dev-sign.sh`, which describes itself as development-only, mints a
throwaway CA and leaves keys in a gitignored `configs/trust/`.

`keystonectl sha256 <file>` (`internal/cli/commands_misc.go:74`, registered
under `localCommands()`) already establishes the pattern for commands that do
local work and never contact an agent. `sign` and `manifest` belong there.

## The invariant: the agent verifies, never signs

**`cmd/keystone` must not link the signing code.** A gateway sitting in a
customer's plant is the most exposed thing in the system; if it carries the
machinery to sign, whoever takes it has a head start on forging updates for
the rest of the fleet.

Concretely: a new `internal/signing`, imported by `internal/cli` only, never by
`internal/agent`. This is cheap to enforce and worth enforcing mechanically —
a test that shells out to `go list -deps ./cmd/keystone` and fails if
`internal/signing` appears in the output. Invariants that are only written down
stop being true within a year.

## `internal/signing`

Build the API on `crypto.Signer`, not on key bytes:

```go
// Backend resolves a signing key. FileBackend is the only implementation to
// begin with; PKCS#11 and cloud KMS both satisfy crypto.Signer, so neither
// needs this interface to change.
type Backend interface {
    Signer(ctx context.Context) (crypto.Signer, error)
    Certificate() (*x509.Certificate, error)
}

func SignDetached(path string, s crypto.Signer, out io.Writer) error
```

A production signing key should not live in a file. Discovering that *after*
threading `[]byte` through the tool means rewriting it, and the difference in
effort now is one interface.

## Ed25519 in the verifier

`VerifyDetached` switches on the leaf's public key type and rejects everything
that is not RSA or ECDSA (`verify.go:92-110`). Ed25519 inside an X.509
certificate is standard (RFC 8410), Go parses it, and `leaf.PublicKey` yields
an `ed25519.PublicKey` — so support is one `case`. It is worth adding: on an
ARMv7 gateway, verifying Ed25519 is far cheaper than RSA-3072, and the keys are
32 bytes.

Two details decide whether independent implementations interoperate, so they
belong in the format documentation and not in a code comment:

1. `VerifyDetached` hashes the file and verifies **over the 32-byte digest**
   (`verify.go:49-53`). Ed25519 hashes its input internally. Signing the digest
   as the message is sound, but it is **not Ed25519ph** — signer and verifier
   must agree or nothing validates.
2. Do **not** reuse `protocol.ManifestSigningPayload` from `ota-updater`. That
   scheme signs a composite of two hashes (`targetHash || deltaHash`) for a
   different purpose. Borrow `pkg/atomicio`, `pkg/delta` and the *design* of its
   registry — not its signing scheme.

### Why X.509 stays the model

`ota-updater/pkg/crypto` signs with a bare Ed25519 key in PKCS#8 PEM, no PKI.
Simpler, and wrong for this product: X.509 buys **rotation and expiry**. An
offline root CA that never touches a network, signing certificates valid for 90
days, means a compromised signer expires on its own. With a bare pinned key, a
compromise is permanent until someone touches every device by hand.

That choice creates one problem worth naming: **a gateway without an RTC boots
in 1970 and rejects every valid certificate as "not yet valid"** — unable to
accept updates exactly when it most needs to. The same clock problem as periodic
scheduling, one layer up.

**Resolved, and implemented in `internal/clock`.** The agent keeps its own lower
bound on the current time from evidence rather than trust: the binary's build
timestamp (it cannot run before it was built) and a high-water mark persisted in
`runtime/state/clock`. Validity is judged against the later of that and the
system clock, so setting a clock back achieves nothing — which is the attack
that matters, since it would otherwise revive a revoked certificate. Setting it
forward only expires certificates early.

Two policies, `high-water` (default) and `strict`; deliberately none that
ignores expiry. Under `strict` the failure is reported as retryable, because NTP
clears it. `/healthz` and `keystone_clock_trusted` expose the state, since a
device on approximate time looks healthy in every other way.

One more source of evidence belongs here and is **not** implemented, because
its only supplier is a dataset manifest. It is worth writing down why the
obvious alternative was tried and dropped:

- **A verified certificate's `NotBefore` cannot raise the mark.** It looks like
  evidence — an authority the device trusts issued it, so time must have passed
  it. But verification uses `max(clock, mark, build)` as its reference, so a
  certificate that validates always has a `NotBefore` at or below that already.
  It is arithmetically incapable of teaching the device anything.
- **A manifest's `published` can.** It is independent of the validity window of
  the certificate that signed it, so one 90-day signing certificate vouches for
  manifests that keep proving later and later dates. A fleet that never reaches
  NTP would then track time through its update channel.

So the datasets phase adds an `Advance(published)` on the clock source, at the
point a manifest is accepted — after its signature and the anti-replay rule,
never before.

## Commands

```
keystonectl sign <file>              # writes <file>.sig
keystonectl verify <file>            # against a trust bundle, no agent involved
keystonectl manifest new <artifact>  # scaffolds a manifest from a file
keystonectl manifest sign <manifest>
keystonectl manifest verify <manifest>
```

`manifest verify` is the one that gets used daily: it validates a publication
in CI before the fleet ever sees it.

## Rolling this out to a mixed fleet

An agent built before the Ed25519 `case` rejects an Ed25519 signature as
"unsupported public key type" — it fails closed, which is correct and also
means **signing with Ed25519 strands every older agent**. Keep publishing
RSA/ECDSA until the fleet has the release that understands Ed25519. This is a
sequencing constraint, not a technical one, and it is the kind that gets
discovered at the worst moment.

## Tests

Written:

- Round-trip per algorithm (`signing/roundtrip_test.go`): sign with the real
  signing path, verify with the agent's own `VerifyDetached` — RSA-2048,
  ECDSA-P256, Ed25519 — plus the guarantee that a modified file stops verifying.
- The linking invariant, both ways (`signing/linkage_test.go`): `cmd/keystone`
  and `cmd/keystoneserver` must not link `internal/signing`, and `keystonectl`
  must, so the first test cannot pass because the commands moved.
- Ed25519 is plain, not Ed25519ph — the one-line mistake that would only surface
  on a device.
- A key that does not match its certificate is refused at signing time.
- Manifest validation, unknown-field tolerance, and the anti-replay rule
  including a six-month-old replay (`manifest/manifest_test.go`).

**Not covered:** an expired leaf certificate. That is the same code path as a
chain failure, and the interesting case is not the rejection but what a device
with a wrong clock does about it — which is the open question below, not a test.

## Documentation this touches

`docs/security.md`, `site/content/security/`, `configs/trust/README.md`, and
`task cli-docs` to regenerate `site/content/reference/keystonectl.md` — CI fails
if that page drifts.

---

# Part 2 — Dataset artifacts

## Manifest format

TOML, matching recipes and plans, and `validate.DecodeTOML` gives
unknown-field reporting for free.

```toml
schema    = 1
name      = "com.amplia.cve-bundle"
version   = "2026-08-14"                 # human label
published = 2026-08-14T03:00:00Z         # the monotonicity anchor

[artifact]
uri    = "https://hub.plant.local/datasets/cve-bundle-2026-08-14.tar"
sha256 = "…"
size   = 184320000

[delta]
server = "https://hub.plant.local"
from   = "2026-08-13"
```

Published alongside `<name>.manifest.toml.sig`.

**Monotonicity is judged on `published`, not `version`.** A date-shaped
`version` compares correctly as a string by accident, not by rule; an RFC3339
timestamp compares correctly always, and `version` stays a human label.

The agent **rejects any manifest whose `published` is not strictly greater
than the last accepted one**, with that value persisted in the snapshot.
Without this line the signature chain does not protect you from the most
obvious attack on a security product: replay yesterday's — or last March's —
perfectly valid, perfectly signed bundle, and the scanner reports no
vulnerabilities. A scanner that lies is worse than one that is down.

Note what this rule does *not* depend on: the local clock. It compares two
signed values against each other, so a gateway with a wrong clock still
enforces it correctly.

## Recipe surface

A new block, not a variant of `[[artifacts]]`:

```toml
[[datasets]]
name     = "oui"
manifest = "https://hub.plant.local/datasets/oui.manifest.toml"
refresh  = "24h"        # monotonic interval, not a cron expression
max_age  = "72h"        # older than this is reported stale
keep     = 2            # versions retained on disk: rollback + delta base
required = true         # no dataset on first install is a failure

[lifecycle.reload]
signal = "SIGHUP"       # process components
# script = "..."        # containers, or anything needing more than a signal
```

Why a separate block rather than extending `Artifact`:

- `[[artifacts]]` means immutable-and-installed-once, and its validation
  requires `sha256`. Making that conditional on a sibling field turns every
  consumer of `Artifact` into a type switch.
- Datasets live in a different tree on disk (below), which is what keeps
  `artifact.GC` and `EnforceCacheLimit` from deleting them.
- No existing recipe can be affected by a block that did not exist.

`refresh` is a duration, deliberately. A monotonic ticker cannot be broken by
NTP stepping the clock, and it needs no timezone, no DST rule and no
catch-up policy. Cron would need all four.

## Disk layout and activation

```
runtime/datasets/<name>/
  2026-08-13/          ← retained: rollback target and delta base
  2026-08-14/          ← newly activated
  current -> 2026-08-14
```

The component reads through `current` and never learns a version number. The
agent injects the path as an environment variable — `KEYSTONE_DATASET_OUI`,
built in `buildRunnerOptions` (`agent.go:1114`) alongside the existing env
handling.

Activation replaces the symlink by `rename(2)` over a temporary link in the
same directory: atomic, so no reader ever observes a half-written state. Two
constraints follow. `runtime/` must be a single filesystem — a cross-device
rename fails, and on a device with a separate `/var` or an overlay that is a
real possibility, so it is worth checking at startup rather than at the first
activation. And a process holding the old file open keeps reading the old
inode until it reopens, which is precisely why the reload hook exists.

## The refresh cycle

The numbering is a real sequence; each step can only follow the previous one.

1. Fetch the manifest. Small, cheap, and the only request made when nothing has
   changed.
2. Verify its detached signature against the trust bundle. Failure aborts here
   and is a *hard* failure — logged, counted, alertable — not a fallback.
3. Enforce monotonicity against the persisted `published`. A replay stops here.
4. Compare against the active version. Equal means done: record a successful
   refresh and return. This is the common case, once a day, for months.
5. Try the delta path with the active version as base. On any miss, fall back
   to the full download — `tryDeltaArtifact` already treats every failure here
   as ordinary (`agent.go:952-958`).
6. Verify `sha256` from the manifest against the assembled bytes.
7. Extract into `runtime/datasets/<name>/<version>/`, a fresh directory. No
   marker files, no idempotence shortcut.
8. Switch `current` atomically.
9. Run the reload hook.
10. Confirm, then retain or roll back.

### Interaction with reconcile

Worth stating because the obvious fear is unfounded: this does **not** need to
serialise against `ApplyPlan`. The symlink swap is atomic, so a component
restarting mid-refresh reads a coherent dataset either way. The only race is a
reload hook firing at a component that is shutting down, which fails harmlessly
and gets logged. No new lock, no interaction with `applyInProgress`.

## Reload

`signal` sends to the main PID — `a.currentPID(name)` already exists
(`agent.go:1879`). To the process leader, not the group: `SIGHUP` to a group
kills children that do not handle it.

Container components report PID 0 and cannot be signalled this way, so
`signal` is rejected at validation for `type = "container"`; those use `script`.
Rejecting it is the point — silently ignoring a declared reload would leave a
component reading a stale dataset with nothing to indicate it.

## Rollback, and where it cannot work

After the hook, wait `grace` (default 30s) for the component to report healthy,
then keep the new version. If it reports unhealthy, point `current` back at the
previous version, reload again, and mark the activation failed. A malformed
feed that takes down a discovery engine is a worse incident than a feed one day
old.

**This only works for components that declare a health check.** Without one
there is no verdict to wait for, and the agent can confirm nothing beyond "the
process is still alive" — `reuseRequiresHealth` (`plan_reconcile.go:473`) draws
the same line for the same reason. Say so in the documentation and recommend a
health check on any component that consumes a dataset. An undocumented
limitation is a surprise in production.

## Staleness is a first-class signal

If a gateway has not refreshed in 40 days — the DMZ closed, a certificate
expired, the hub died — nothing today would say so. The component stays
`running` and `healthy`.

So: `keystone_dataset_age_seconds{name}`, plus `last_refresh`, `last_result`
and the `max_age` verdict on the API. For a security product, "my data is old"
*is* security information.

The clock returns here, and this time it matters: age is `now - published`, and
a device with a wrong clock computes garbage. Report age as **unknown** rather
than as a number when the clock cannot be trusted — a plausible wrong number is
worse than an honest gap, because it silences the alert that should have fired.

## Retention and deltas

Keep `keep` versions (default 2). The existing collectors do not help and must
not be extended to try: `artifact.GC` retains by `{recipe}/{version}` under
`runtime/artifacts`, and `EnforceCacheLimit` is a global LRU by size
(`index.go:72`) that would happily delete the previous version — which is both
the rollback target and the delta base. A separate tree plus explicit retention
avoids both.

**How the dataset is published decides whether deltas are worth anything.**
The measurement already recorded in `internal/recipe/types.go:38-41`: a patch
over a `.tar.gz` saves nothing (98% of full size), because one changed byte
reshuffles the gzip stream; over the uncompressed tar of two adjacent releases
the same patch is 3%. A daily CVE bundle is the ideal delta case and would be
entirely wasted by publishing it gzipped. Publish the uncompressed `.tar`, or
compress with an rsyncable mode — and accept that the first fetch on a new
device transfers the whole thing. `FetchViaDelta` also requires an unpackable
archive (`agent.go:939`), so a single-file dataset takes the full-download path
regardless.

## Scheduling

One monotonic ticker for all datasets in the plan, sharing the jitter
derived from the device ID that Phase 1 introduces — a deterministic offset, so
a fleet of a thousand devices spreads across the window and each device lands in
the same slot every day, which is what makes it debuggable.

On startup, refresh immediately if `age > refresh`. That single rule replaces
any missed-run policy: an intermittently powered device catches up when it
wakes, which is the case that matters most and the one a wall-clock cron
handles worst.

## API and metrics

`GET /v1/datasets` — name, active version, `published`, last refresh, last
result, age, next refresh, manifest URI. Add it to `routes.go` (the single
source of truth) and run `task openapi`.

```
keystone_dataset_age_seconds{name}
keystone_dataset_refresh_total{name,result}
keystone_dataset_activation_total{name,result}
keystone_dataset_active_version_info{name,version,published}
```

`state.Snapshot` gains a `Datasets []DatasetState` carrying name, version,
`published`, sha256, last refresh and last result — `published` being the one
that must survive a restart for the replay rule to hold.

## What deliberately does not change

**No new component state.** Staleness is an attribute of the dataset, not a
phase of the component's lifecycle, and the component genuinely is still
running. `site/content/concepts/component-state.md` is a contract; adding a
state to the FSM to express "the data is old" would be modelling the wrong
thing and would make every reader of that contract handle a state that means
something else. The dataset's age is exposed on the dataset, and the alert
lives in Prometheus.

## Tests worth writing

- A manifest whose `published` is equal to or older than the last accepted one
  is rejected, and the rejection survives an agent restart.
- Activation rolls back when the reload hook fails, and when the component goes
  unhealthy inside `grace`.
- Retention keeps exactly `keep` versions and never removes `current` or the
  delta base.
- An unsynchronised clock reports age as unknown instead of a wrong number.
- Delta falls back to a full download with no base present, and the result
  verifies against the manifest digest either way.
- `signal` on a container component is rejected at validation, not ignored.

## Documentation this touches

A new `site/content/concepts/datasets.md`; plus `concepts/recipes.md`,
`reference/schemas.md`, `reference/toml.md`, `reference/env.md`,
`operations/metrics.md`, the examples chapter, and a note in
`concepts/component-state.md` stating explicitly that staleness is not a state.
`internal/validate/validate.go` needs `datasets` in `recipeSchema`.

---

## Sequencing

| Order | Phase | Why here |
|---|---|---|
| 1 | Periodic reconcile | Self-repair. Independent of everything below |
| **2** | **Signing** | Nothing in Phase 3 can be tested without it |
| **3** | **Datasets** | The use case |
| 4 | Remote signed plan + rings | The code channel, separate policy |
| 5 | `keystone-hub` | After 3 and 4 run at a real customer |

## Native ingestion in the hub

Requested while building this phase, and belonging to the hub (phase 5) rather
than the agent: the hub should be able to **fetch upstream data itself** — IEEE
OUI, and vulnerability feeds — and turn it into signed, delta-friendly bundles
on a schedule. Generically, not with IEEE and NVD hard-coded.

The shape that follows from what the agent already expects:

- **A declarative source**, not code per feed. A source is a URL, a cadence, an
  optional transform, and a bundle name. IEEE OUI is one file. A vulnerability
  feed is paginated and incremental. Both should be configuration.
- **Conditional fetching is the whole point of the cache.** `ETag` /
  `If-None-Match` and `If-Modified-Since` on every poll, honouring `Retry-After`
  and backing off hard on 429. This is what keeps one hub per plant from
  becoming the thing that gets an API key throttled — and note the agent does
  *not* do conditional requests today, because its manifests are tiny and its
  artifacts are immutable; the hub's fetches are neither.
- **Incremental where the upstream supports it.** A feed with a
  "modified since" parameter should be pulled as a window, not re-downloaded
  whole, with the last successful window persisted.
- **Bundle as an uncompressed tar, versioned by publication date**, then
  generate a patch against the previous version with `ota-updater/pkg/delta`.
  Publishing gzipped would throw the delta away (98% vs 3%, measured).
- **Retention that matches the agent's**: keep enough versions to serve a patch
  to a device that has been offline for a while.

**The open question is signing, and it is the important one.** The rule so far
is that the hub never holds a signing key — a hub that signs is a hub that,
compromised, signs malware for the whole fleet. But a hub that *generates*
bundles produces artifacts that must be signed by someone. Three ways out, and
this needs a decision before any of it is built:

1. **The hub generates, a separate signer signs.** The hub publishes a
   candidate; a signing host (or CI) picks it up, verifies it, signs the
   manifest and publishes it. Keeps the rule intact, adds a moving part.
2. **A distinct, lower-privilege ingestion key**, trusted only for dataset
   manifests and never for recipes, plans or releases. Simpler; makes the trust
   bundle's meaning conditional on what is being verified.
3. **Ingestion stays in the backend**, and the hub only mirrors and serves. The
   cleanest, and it gives up the "hub in an air-gapped plant ingests locally"
   case.

Worth noting that (2) is only tolerable if the agent can express "this key may
sign datasets but not code", which it currently cannot — every signature chains
to one bundle. That is a real change to the trust model, not a configuration
flag.

## What stays open

- **Key rotation is not solved by either phase.** If the signing key is
  compromised, there is no mechanism to update `KEYSTONE_TRUST_BUNDLE` across
  offline devices — and a key that can sign plans can sign a plan that rewrites
  the trust bundle. Short-lived certificates limit the blast radius; they do not
  answer the question. TUF and Uptane solved rotation, threshold signing and
  metadata expiry, and are worth reading before designing this.
- **Publication format** for each dataset — uncompressed tar for delta
  efficiency against first-fetch cost.
- **Whether datasets should be fetched by non-consuming components at all**, or
  only ever by the component that reads them. The design above assumes the
  latter.
