+++
title = "Running under systemd"
weight = 63
description = "A hardened unit, and why the agent is confined too."
+++

# Running under systemd

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

## Log volume

Component stdout and stderr are streamed into the agent's log, so a chatty
component becomes agent log volume. On a device with a small journal, cap it:

```ini
[Service]
LogRateLimitIntervalSec=30s
LogRateLimitBurst=1000
```
