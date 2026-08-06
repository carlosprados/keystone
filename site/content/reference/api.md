+++
title = "HTTP API"
weight = 73
description = "Endpoints, parameters and response shapes."
+++

# HTTP API

Base URL is wherever `--http` points; `127.0.0.1:8080` by default. Every endpoint
except `/healthz` requires `Authorization: Bearer <token>` when a token is
configured.

## GET /healthz

```json
{ "status": "ok", "uptime": "4h12m3s", "closed": false, "time_utc": "2026-08-06T12:00:00Z" }
```

Exempt from authentication, and exposes no component detail — safe for a load
balancer or a systemd health check.

## GET /metrics

Prometheus exposition format. See [Metrics](../../operations/metrics/).

## GET /v1/components

```json
[
  {
    "name": "api",
    "state": "running",
    "restarts": 1,
    "last_health": "healthy",
    "pid": 40219,
    "recipe": "recipes/com.acme.api.toml",
    "version": "1.4.0"
  }
]
```

`state` is one of `none`, `stopped`, `running`, `failed`. `last_health` is
`healthy`, `unhealthy` or `unknown`. `pid` is `0` for container components and for
anything not running. The guarantees these fields carry are in
[Component state](../../concepts/component-state/).

## GET /v1/plan/status

```json
{
  "planPath": "plan.toml",
  "status": "running",
  "components": [
    { "name": "api", "state": "running", "restarts": 0, "last_health": "healthy", "pid": 40219 }
  ]
}
```

`error` is present only when the last operation failed. Note that this endpoint
carries the component list too, so a fleet poller needs one request, not two.

## GET /v1/plan/graph

```json
{
  "nodes": ["database", "api"],
  "edges": { "database": ["api"] },
  "order": ["database", "api"]
}
```

`edges` maps a dependency to the components that depend on it. `order` is a valid
start order.

## POST /v1/plan/apply

Body: the plan TOML. Query: `dry=true` to preview.

```bash
curl -X POST --data-binary @plan.toml localhost:8080/v1/plan/apply
```

`202 Accepted` on success, `500` with the error text on failure (including a
rollback summary), `400` if the body is not a usable plan.

{{% notice style="note" %}}
A JSON body with `planPath` is **rejected**. Upload content — letting the API name
a local path would turn it into a file-read primitive.
{{% /notice %}}

## POST /v1/plan/stop

Stops every component in reverse dependency order and marks the plan `stopped`,
which also means it will not be resumed on the next boot.

## GET /v1/recipes

Lists `name:version` entries in the agent's recipe store.

## POST /v1/recipes

Body: recipe TOML. Stores it so plans can refer to it as `name:version`. Recipes
added this way are trusted through API authentication rather than a file signature.
`?force=true` overwrites an existing version.

## DELETE /v1/recipes/{name}/{version}

Removes a recipe from the store. Name and version are validated against an
allow-list — no path traversal.

## POST /v1/components/{name}:stop

Stops one component. Its dependents are **not** stopped.

## POST /v1/components/{name}:restart

| Parameter | Values | Meaning |
|---|---|---|
| `wait` | `pid` *(default)*, `health` | Return when a new PID exists, or when it probes healthy |
| `timeout` | duration, e.g. `60s` | How long to wait |
| `dry` | `true` | Report what would be restarted, change nothing |

```json
{
  "component": "api",
  "pid": 41002,
  "dependents": { "dashboard": 41055 },
  "wait": "pid",
  "timeout": "30s"
}
```

`dependents` maps each cascaded component to its new PID, so one call tells you
everything that moved. Dependents are restarted according to each edge's
[dependency type](../../concepts/dependencies/) — `hard` and `soft` cascade,
`ordering` does not.

With `dry=true` the shape is different — the plan, not the result:

```json
{ "stopOrder": ["dashboard", "api"], "startOrder": ["api", "dashboard"] }
```
