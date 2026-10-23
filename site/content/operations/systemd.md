+++
title = "Running under systemd"
weight = 63
description = "A hardened unit, and why the agent is confined too."
+++

The agent should itself be a supervised, confined service. There is a hardened
example at `configs/systemd/keystone.service`:

```ini
[Unit]
Description=Keystone Agent
After=network-online.target
Wants=network-online.target

[Service]
# The agent does not implement sd_notify; use a plain process type.
Type=simple

# Security-relevant configuration comes from an environment file:
#   KEYSTONE_API_TOKEN=<random token>           # required for a non-loopback bind
#   KEYSTONE_TRUST_BUNDLE=/etc/keystone/ca.pem  # CA for artifact/recipe signatures
EnvironmentFile=/etc/keystone/keystone.env

ExecStart=/opt/keystone/bin/keystone --http 0.0.0.0:8080
Restart=on-failure
RestartSec=3
User=keystone
Group=keystone
AmbientCapabilities=CAP_NET_BIND_SERVICE
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ProtectKernelTunables=true
ProtectControlGroups=true
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
```

## Choosing the agent's own privileges

This is the decision that shapes everything else, and it is a genuine trade-off.

**A non-root agent** (as above) is the safer default. The cost: it cannot install
into system paths, cannot write systemd units, cannot switch a component to another
user, and cannot narrow a component's capability bounding set — see the
`CAP_SETPCAP` case in [Process privileges](../../security/process-privileges/).

**A root agent** can do all of that, which is often why it exists: install hooks
that place binaries in `/usr/local/bin` or write unit files need it. If you go that
way, confine each component individually with `[lifecycle.run.security]` — that is
precisely the situation the feature was built for. A root agent with unconfined
components is the worst of both worlds.

{{% notice style="note" %}}
`ProtectSystem=strict` makes the filesystem read-only apart from a few paths. The
agent needs its working directory writable — set `WorkingDirectory=` and
`ReadWritePaths=` accordingly, or components will fail to install with confusing
permission errors.
{{% /notice %}}

## Working directory matters

Everything the agent stores — `runtime/artifacts`, `runtime/components`,
`runtime/state` — is relative to its working directory. Set it explicitly:

```ini
WorkingDirectory=/var/lib/keystone
```

Otherwise a change in how the service is launched silently relocates the state, and
the agent boots as if it had never deployed anything.

## Restart, but let it decide

`Restart=on-failure` is right. Do not add a `systemd` timer that restarts the agent
periodically "to be safe" — the agent already re-applies its plan on boot and
reconciles without churn. Restarting it on a schedule just adds a window where
nothing is supervising.

## Updating from v0.12.6 or earlier: once, by hand

Up to v0.12.6 the agent tried to install updates itself, which the A/B unit
does not allow, so a device on one of those versions cannot update itself to a
later one. Install the new version once from outside, together with the new
gate script. From then on self-update works through the gate:

```bash
sudo install -D -m 0755 keystone /opt/keystone/versions/v0.12.7/keystone
sudo install -m 0755 keystone-update-gate.sh /usr/local/lib/keystone/keystone-update-gate.sh
sudo ln -sfn versions/v0.12.7 /opt/keystone/current
# record it as confirmed, so a later update has somewhere to roll back to
sudo sed -i 's/^KEYSTONE_UPDATE_CONFIRMED=.*/KEYSTONE_UPDATE_CONFIRMED=v0.12.7/' /opt/keystone/state/update.env
sudo systemctl restart keystone
```

## Updating from v0.13.0 or earlier: a rollback needs the network

Later versions keep the cached copy of every staged artifact (one without
`unpack`) in its version's `.staged/` directory, where v0.13.0 and earlier do
not look. If the gate rolls such an update back, the version it goes back to
finds no cached copy and **downloads every staged artifact again** before it can
start its components. On a device that cannot reach its artifact URIs at that
moment, the download fails after its retries, the plan fails, and **every
component stays stopped**, not only the ones that stage an artifact. It stays
that way when the URIs come back: the failed plan is retried at the agent's next
start, which then downloads what it needs. Going back by hand costs the same. A
rollback between two versions on the same side of this change still works
offline, and so does going forward again.

**Devices that cannot reach their artifact URIs should not rely on the gate's
rollback for this upgrade.** Keep the source reachable until the update is
confirmed. The upgrade itself downloads nothing: the first start moves the cached
copies into `.staged/`. Why the copies moved is in
[Going back to an older agent](../../internals/artifacts/#going-back-to-an-older-agent).

## Log volume

Component stdout and stderr are streamed into the agent's log, so a chatty
component becomes agent log volume. On a device with a small journal, cap it:

```ini
[Service]
LogRateLimitIntervalSec=30s
LogRateLimitBurst=1000
```
