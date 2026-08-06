+++
title = "Component state"
weight = 24
description = "What each state means, and the liveness guarantees you can rely on."
+++

# Component state

`GET /v1/components` is meant to be trustworthy enough to alert on. This page is
the contract.

{{% notice style="primary" title="Like you're five" %}}
If the toy is broken, the note that comes back says *broken*. It never says
*"playing happily"* about a toy that is lying on the floor in pieces. Even when
that is more embarrassing.
{{% /notice %}}

## The states

| State | Meaning |
|---|---|
| `none` | Known from the plan; nothing installed or started yet |
| `stopped` | Not running: never started, stopped on request, or exited cleanly with no applicable restart policy |
| `running` | A managed instance is alive **and supervised** |
| `failed` | Terminal: crashed with no applicable restart policy, or exhausted `max_retries` |

The supervisor also has transient states — `installing`, `starting`, `stopping` —
which appear in the log as `[supervisor] component=… state=…`. They are *not*
published for plan components: as far as the API is concerned a component goes from
`none` to `running` (or `failed`). Only the `--demo` stack publishes raw supervisor
states.

`last_health` is `healthy`, `unhealthy` or `unknown`. Only components that declare
`[lifecycle.run.health]` ever get a verdict; the rest stay `unknown` forever, which
is not a problem.

## The guarantees

These hold at every write path — the runner's exit callback, an explicit stop, and
the periodic state poller:

- A component reported `running` **has a live supervision loop**: its health probe
  and restart policy are active.
- A component reported `running` with `pid > 0` **has that PID alive**. Keystone
  never advertises a PID that no longer exists; when a process is gone the PID is
  cleared to `0` and the state moves to `stopped` or `failed`.
- **Every exit updates the state, including a clean one.** A process that traps
  `SIGTERM` and shuts down gracefully under `restart_policy = "on-failure"` is
  reported `stopped`, not left at its last good state.
- `last_health` is reset to `unknown` when a component exits. Something that is
  gone cannot still be healthy.
- A failed apply unwinds through the same teardown as an explicit stop, so a
  component the agent gave up on does not linger as `running`.

{{% notice style="note" title="Containers are the exception" %}}
Container components report `pid = 0` — there is no host PID to probe — so for them
the supervision loop is the only liveness signal available. A container that dies
while its loop is alive can still read `running`. This is tracked as
[issue #13](https://github.com/carlosprados/keystone/issues/13).
{{% /notice %}}

## Why this is spelled out

These guarantees exist because they were once absent. In production, an InfluxDB
component was killed during a re-apply; the agent kept advertising it as
`running` / `healthy` with a PID that no longer existed, and a downstream data
pipeline was silently broken for minutes — the alerting looked green the whole
time.

The cause was two-fold: the reconcile logic decided a component could be reused by
reading cached state alone, and the runner's exit callback only updated the store
when the exit was an *error*. A clean exit therefore froze the record at
`running/healthy` forever, which then made the corpse look like a valid candidate
for reuse.

Both are fixed
([#10](https://github.com/carlosprados/keystone/issues/10)), and the rules above
are what the fix guarantees. The lesson generalises: **cached state is not
liveness**. Ask the kernel.

## Watching it

```bash
watch -n1 'curl -s localhost:8080/v1/components | jq -r ".[] | \"\(.name) \(.state) pid=\(.pid) \(.last_health)\""'
```

Prometheus metrics carry the same information for alerting — see
[Metrics](../../operations/metrics/).
