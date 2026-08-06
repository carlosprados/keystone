+++
title = "HTTP"
weight = 51
description = "The REST API, endpoint by endpoint."
+++

# HTTP

On by default, bound to `127.0.0.1:8080`. Disable it with `--http ""`.

See [API authentication](../../security/api-auth/) before exposing it off-device —
the agent will refuse a non-loopback bind without a token.

## Endpoints

| Method | Path | Does |
|---|---|---|
| `GET` | `/healthz` | Agent liveness. Exempt from auth |
| `GET` | `/metrics` | Prometheus metrics |
| `GET` | `/v1/components` | Every component with state, PID, restarts, health |
| `GET` | `/v1/plan/status` | Plan path, status, last error |
| `GET` | `/v1/plan/graph` | Nodes, edges and topological start order |
| `POST` | `/v1/plan/apply` | Apply a plan (body = plan TOML). `?dry=true` to preview |
| `POST` | `/v1/plan/stop` | Stop every component |
| `GET` | `/v1/recipes` | List recipes in the store |
| `POST` | `/v1/recipes` | Add a recipe (body = recipe TOML) |
| `DELETE` | `/v1/recipes/{name}/{version}` | Remove a recipe from the store |
| `POST` | `/v1/components/{name}:stop` | Stop one component |
| `POST` | `/v1/components/{name}:restart` | Restart one component, cascading per dependency type |

## Examples

```bash
# What is running
curl -s localhost:8080/v1/components | jq

# Apply a plan (content, not a path)
curl -X POST --data-binary @plan.toml localhost:8080/v1/plan/apply

# Preview a plan
curl -X POST --data-binary @plan.toml "localhost:8080/v1/plan/apply?dry=true"

# Restart one component and wait until it is healthy again
curl -X POST "localhost:8080/v1/components/api:restart?wait=health&timeout=60s"

# With a token
curl -H "Authorization: Bearer $KEYSTONE_API_TOKEN" https://device/v1/plan/status
```

The restart endpoint takes `wait=pid` (default — return once a new PID exists) or
`wait=health` (return once it probes healthy), and `dry=true` to see what a restart
would cascade to without doing it.

## keystonectl

The same API with less typing:

```bash
keystonectl status
keystonectl components
keystonectl apply plan.toml
keystonectl restart api
keystonectl stop api          # one component
keystonectl stop-plan         # everything
```

It reads `KEYSTONE_API_TOKEN` from the environment and takes `--addr` for a remote
agent. See [the CLI reference](../../reference/cli/) for every subcommand.

## Bruno collection

`bruno/` in the repository is a ready-made [Bruno](https://www.usebruno.com/)
collection with every request above, which beats hand-writing curl while you are
exploring.
