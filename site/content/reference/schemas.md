+++
title = "Recipe and plan schema"
weight = 74
description = "Every field, its type and its default, in one place."
+++

Both are TOML, and both are validated against a schema on load — invalid input is
rejected, not best-effort parsed.

## Plan

```toml
[[components]]
name = "api"                       # string, required, unique in the plan
recipe = "recipes/api.toml"        # path to a TOML file, or "name:version" from the store
```

That is the entire plan schema. Everything else is in the recipe.

## Recipe

### `[metadata]`

| Field | Type | Required | Notes |
|---|---|---|---|
| `name` | string | yes | Reverse-DNS by convention. Part of the recipe identity |
| `version` | string | yes | Semver. Part of the recipe identity |
| `description` | string | no | |
| `publisher` | string | no | |
| `type` | string | no | Reserved |

### `[[artifacts]]`

| Field | Type | Default | Notes |
|---|---|---|---|
| `uri` | string | — | Where to download from |
| `sha256` | string | — | Mandatory unless `--insecure-skip-verify` |
| `sig_uri` | string | `<uri>.sig` | Detached signature |
| `cert_uri` | string | — | Leaf certificate, if not provisioned on the device |
| `unpack` | bool | `false` | Extract into the working directory |
| `github_token` | string | — | Sets `Authorization` for private GitHub assets |
| `headers` | table | — | Extra HTTP headers for this download |

### `[lifecycle.install]`

| Field | Type | Default | Notes |
|---|---|---|---|
| `script` | string | — | Shell, run once in the working directory |
| `require_privilege` | bool | `false` | Declares that it needs privilege |

### `[lifecycle.run]`

| Field | Type | Default | Notes |
|---|---|---|---|
| `type` | string | `process` | `process` or `container` |
| `restart_policy` | string | `always` | `always`, `on-failure`, `never` |
| `max_retries` | int | `5` for always/on-failure | Terminal once exhausted |

### `[lifecycle.run.exec]` — process components

| Field | Type | Default | Notes |
|---|---|---|---|
| `command` | string | — | Required. `./x` is relative to the working directory |
| `args` | string list | — | |
| `working_dir` | string | component workdir | |
| `env` | table | — | Extra environment variables |

### `[lifecycle.run.security]` — process components

| Field | Type | Default | Notes |
|---|---|---|---|
| `user` | string | agent's user | `"user"`, `"uid"`, `"user:group"`, `"uid:gid"` |
| `no_new_privileges` | bool | `false` | `PR_SET_NO_NEW_PRIVS` |
| `capabilities` | string list | *absent* | Allow-list. `[]` means none; omitted means unchanged |

### `[lifecycle.run.container]` — container components

| Field | Type | Default | Notes |
|---|---|---|---|
| `image` | string | — | Required |
| `runtime` | string | `auto` | `auto`, `containerd`, `cli`, `nerdctl`, `docker`, `podman` |
| `pull_policy` | string | `if-not-present` | `always`, `never`, `if-not-present` |
| `network_mode` | string | `bridge` | `host`, `bridge`, `none` |
| `user` | string | — | `uid:gid` inside the container |
| `privileged` | bool | `false` | |
| `hostname` | string | — | |
| `env`, `labels` | table | — | |

`[[lifecycle.run.container.mounts]]`: `source`, `target`, `type`
(`bind`/`volume`/`tmpfs`), `read_only`.

`[[lifecycle.run.container.ports]]`: `host_ip`, `host_port`, `container_port`,
`protocol`.

`[lifecycle.run.container.resources]`: `memory_mb`, `memory_swap`, `cpu_shares`,
`cpu_quota`, `cpu_period`, `pids_limit`.

### `[lifecycle.run.health]`

| Field | Type | Default | Notes |
|---|---|---|---|
| `check` | string | — | `http://…` or `https://…` (2xx only), `tcp://…`, `cmd:…` |
| `interval` | duration | `10s` | |
| `timeout` | duration | `3s` | |
| `failure_threshold` | int | `3` | |

### `[lifecycle.shutdown]`

| Field | Type | Notes |
|---|---|---|
| `script` | string | Best-effort hook on stop |

### `[resources]`

| Field | Type | Enforced | Notes |
|---|---|---|---|
| `open_files` | uint | yes | `RLIMIT_NOFILE` |
| `memory_limit` | string | **no** | Placeholder for process components |
| `cpu_quota` | int | **no** | Placeholder for process components |

### `[[dependencies]]`

| Field | Type | Default | Notes |
|---|---|---|---|
| `name` | string | — | Another recipe's `metadata.name` |
| `version` | string | any | Semver constraint |
| `type` | string | `hard` | `hard`, `soft`, `ordering` |

## Validating before you deploy

```bash
curl -X POST --data-binary @plan.toml "localhost:8080/v1/plan/apply?dry=true"
```

A dry run loads and validates every recipe, verifies signatures and computes the
reconcile plan — without installing or starting anything.
