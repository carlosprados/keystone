+++
title = "A service from a GitHub release"
weight = 251
description = "One component, end to end: sign it, describe it, deploy it, verify it."
+++

The smallest realistic case: a single Go binary published as a GitHub release asset,
running as an unprivileged service with a health check.

## 1. Sign the artifact

The agent will refuse an unsigned artifact, so signing is part of your build, not an
afterthought. With the development helper:

```bash
./scripts/dev-sign.sh init                          # one-off: a throwaway CA
./scripts/dev-sign.sh artifact dist/api-1.4.0.tar.gz
# → dist/api-1.4.0.tar.gz.sig
```

In production this is a CI step against a key in a KMS or HSM. Publish the `.sig`
next to the asset and give the device the CA:

```bash
export KEYSTONE_TRUST_BUNDLE=/etc/keystone/trust/ca.pem
```

## 2. Compute the digest

```bash
keystonectl sha256 dist/api-1.4.0.tar.gz
# 9f2c8b1e…
```

## 3. The recipe

`recipes/com.acme.api.toml`:

```toml
[metadata]
name = "com.acme.api"
version = "1.4.0"
description = "Acme HTTP API"
publisher = "Acme Ltd"

[[artifacts]]
uri = "https://github.com/acme/api/releases/download/v1.4.0/api-1.4.0.tar.gz"
sha256 = "9f2c8b1e…"
sig_uri = "https://github.com/acme/api/releases/download/v1.4.0/api-1.4.0.tar.gz.sig"
unpack = true

[lifecycle.install]
script = "chmod +x ./api"

[lifecycle.run]
restart_policy = "always"
max_retries = 5

[lifecycle.run.exec]
command = "./api"
args = ["--listen", ":8080"]

[lifecycle.run.exec.env]
LOG_LEVEL = "info"

[lifecycle.run.security]
user = "acme:acme"
no_new_privileges = true
capabilities = ["CAP_NET_BIND_SERVICE"]

[lifecycle.run.health]
check = "http://127.0.0.1:8080/healthz"
interval = "10s"
timeout = "2s"
failure_threshold = 3

[resources]
open_files = 8192
```

Sign the recipe too — a file-loaded recipe needs a signature before any hook runs:

```bash
./scripts/dev-sign.sh recipe recipes/com.acme.api.toml
```

{{% notice style="note" %}}
`capabilities = ["CAP_NET_BIND_SERVICE"]` is only needed to bind a port below 1024.
Listening on `:8080` as an unprivileged user needs no capability at all — use
`capabilities = []` and drop them all. The example keeps it to show the syntax.
{{% /notice %}}

## 4. The plan

`plan.toml`:

```toml
[[components]]
name = "api"
recipe = "recipes/com.acme.api.toml"
```

## 5. Deploy

```bash
keystonectl apply plan.toml
```

## 6. Verify — all four things worth checking

```bash
# state, PID and health
keystonectl components

# the process really is confined
PID=$(curl -s localhost:8080/v1/components | jq -r '.[]|select(.name=="api")|.pid')
grep -E 'Uid|CapEff|CapBnd|NoNewPrivs' /proc/$PID/status

# the artifact really was verified (no warning in the log)
journalctl -u keystone | grep -i "signature\|verif"

# re-applying changes nothing
keystonectl apply plan.toml && keystonectl components
```

That last one is the check people skip: if the PID changed, something in your recipe
is making the agent think it changed. Usually an edited recipe with the same version
— the digest is what the agent compares, not the version string.

## What the log looks like

```
[agent] reconcile stop_order=[] start_order=[api] no_touch=[]
[supervisor] layer=0 components=[api] msg=starting layer
[agent] component=api type=process cwd=runtime/components/com.acme.api/1.4.0 cmd=./api
[runner] component=api security=user=acme:acme,no_new_privileges=true,capabilities=CAP_NET_BIND_SERVICE msg=applying privilege restrictions
[agent] component=api pid=40219 restarts=0 msg=process started
[supervisor] component=api state=running
[supervisor] all components running
```
