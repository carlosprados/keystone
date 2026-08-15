+++
title = "Recipe and plan schema"
weight = 75
description = "Every field, its type and its default, in one place."
+++

Both are TOML, and both are validated against a schema on load — invalid input is
rejected, not best-effort parsed. This page is the field list; if you want the
syntax itself — which tables repeat, what the dots mean, and which mistakes the
parser stays quiet about — read the [TOML cheat sheet](../toml/) first.

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
| `delta` | table | — | Opt into patching instead of downloading — see below |

### `[artifacts.delta]`

Optional. Absent means the artifact is always downloaded whole, which is what every
recipe written before this field does.

| Field | Type | Default | Notes |
|---|---|---|---|
| `server` | string | — | Base URL of a delta server. Required. The patch URL is derived: `{server}/delta/{base sha256}/{sha256}` |
| `sha256` | string | — | Digest of the **uncompressed** archive after patching. Required — it is what the result is verified against |
| `format` | string | `bsdiff+zstd` | Patch encoding. An unrecognised value is not an error: the agent logs it and downloads the whole artifact |

Requires `unpack = true`. Every failure falls back to the full download rather than
failing the apply. See [Artifacts](../../internals/artifacts/#delta-downloads) for
the trust model, the measured savings and the current limits.

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

### `[[datasets]]`

Data the agent keeps fresh without restarting the component. See
[datasets]({{% relref "/concepts/datasets" %}}).

| Field | Type | Default | Notes |
|---|---|---|---|
| `name` | string | — | Required. Becomes a directory and `KEYSTONE_DATASET_<NAME>`; same allow-list as a recipe name |
| `manifest` | string | — | Required. URL (or local path) of the signed manifest naming the current version |
| `sig_uri` | string | `<manifest>.sig` | Detached signature of the manifest |
| `cert_uri` | string | `KEYSTONE_LEAF_CERT` | Signing certificate |
| `refresh` | duration | `24h` | How often to look. Minimum 1m — a dataset is not a poll loop |
| `max_age` | duration | 3 × `refresh` | Past this the dataset reports stale |
| `keep` | integer | `2` | Versions retained. Minimum 2: below that there is no rollback target |
| `required` | boolean | `true` | Whether a component may start without it |
| `headers` | table | — | Extra HTTP headers, for a hub behind auth |

### `[lifecycle.reload]`

How a component is told its data changed, instead of being restarted.

| Field | Type | Notes |
|---|---|---|
| `signal` | string | `SIGHUP`, `SIGUSR1` or `SIGUSR2`. Process components only — a container has no PID to signal, and declaring it on one is rejected |
| `script` | string | Run instead of a signal, from the working directory. This is how a container reloads |
| `grace` | duration | How long the component has to prove it survived. Default `30s` |

Signals are an allow-list on purpose: a reload is meant to make a component
reread a file, and `SIGKILL` would turn "your data changed" into an outage.

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
reconcile plan — without installing or starting anything. It also rejects any key
the agent does not recognise, which a real apply tolerates on purpose so that a
recipe can outrun the agents it is published to. The
[TOML cheat sheet](../toml/#traps) has that trade in full.
