+++
title = "Metrics and alerting"
weight = 61
description = "What Keystone exports, and the alerts actually worth having."
+++

Prometheus metrics on `GET /metrics`, no authentication required beyond whatever
protects the rest of the HTTP adapter.

## Exported metrics

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `keystone_component_state` | gauge | `name`, `state` | `1` for the component's current state, `0` for the others |
| `keystone_component_state_health` | gauge | `name`, `state`, `health` | `1` for the current state+health combination |
| `keystone_component_restarts_total` | counter | `name` | Restarts since the agent started |
| `keystone_component_healthy` | gauge | `name` | `1` healthy, `0` unhealthy |
| `keystone_component_cpu_percent` | gauge | `name` | CPU percent, process components only |
| `keystone_component_memory_rss_bytes` | gauge | `name` | Resident memory, process components only |

Plus the Go runtime and process collectors Prometheus adds by default.

## Alerts worth having

**A component is not running.** The one that matters most, and it is trustworthy
because of the [state guarantees](../../concepts/component-state/):

```yaml
- alert: KeystoneComponentDown
  expr: keystone_component_state{state="running"} == 0
        unless on(name) keystone_component_state{state="running"} == 1
  for: 2m
  annotations:
    summary: "{{ $labels.name }} is not running"
```

**Crash looping.** A component that restarts repeatedly is up but not well:

```yaml
- alert: KeystoneComponentFlapping
  expr: increase(keystone_component_restarts_total[15m]) > 5
  for: 5m
```

**Unhealthy but running.** The process is alive and failing its own probe — often
the earliest signal of a dependency problem:

```yaml
- alert: KeystoneComponentUnhealthy
  expr: keystone_component_healthy == 0
  for: 5m
```

**Memory creep.** Cheap to add, and it catches the leak before the OOM killer
does:

```yaml
- alert: KeystoneComponentMemoryGrowth
  expr: keystone_component_memory_rss_bytes > 500e6
  for: 30m
```

## Scraping edge devices

Prometheus pulling from a fleet behind NAT usually does not work. Two options that
do:

- **Push**: a Prometheus agent or Grafana Alloy on the device with remote-write.
- **Events**: enable the NATS or MQTT adapter and consume
  `events.state` / `events.health` centrally. You lose PromQL over raw samples but
  gain a view that works through NAT and survives the device being offline (via
  JetStream or a retaining broker).

## Do not alert on the agent alone

`/healthz` tells you the *agent* is alive. An agent can be perfectly healthy while
supervising nothing at all — for instance after an operator called `/v1/plan/stop`,
which the agent deliberately remembers across reboots. Alert on component state,
and on plan status, not just on agent liveness.
