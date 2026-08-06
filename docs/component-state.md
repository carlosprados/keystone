# Component State and Reuse

`GET /v1/components` is meant to be trustworthy enough to alert on. This
document states what each component state means, what the agent guarantees
about it, and when a re-apply keeps a component running instead of restarting
it.

## States

| State | Meaning |
|-------|---------|
| `none` | Known from the plan, nothing installed or started yet |
| `installing` | Install hook / artifact download in progress |
| `stopped` | Not running. Either never started, stopped on request, or exited on its own with code 0 and no restart policy that applies |
| `running` | A managed instance is alive and supervised |
| `failed` | Terminal failure: crashed with no applicable restart policy, or exhausted `max_retries` |
| `stopping` | Stop in progress |

`last_health` is `healthy` / `unhealthy` / `unknown`. Only components that
declare `[lifecycle.run.health]` ever get a verdict; the rest stay `unknown`
forever, and that is not a problem.

## Liveness guarantees

- A component reported `running` has a live supervision loop attached: its
  health probe and restart policy are active.
- A component reported `running` with `pid > 0` has that PID alive. The agent
  never advertises a PID that no longer exists — when a process is gone, the
  PID is cleared to `0` and the state moves to `stopped` or `failed`.
- Every exit the runner will not retry updates the state, including a clean
  exit with code 0. A process that traps `SIGTERM` and shuts down gracefully
  under `restart_policy = "on-failure"` is reported `stopped`, not left at its
  last known good state.
- `last_health` is reset to `unknown` when a component exits: a component that
  is gone cannot still be healthy.

Container components report `pid = 0` (there is no host PID to probe), so for
those the supervision loop is the only liveness signal.

## Reuse on re-apply

Applying a plan is a reconcile, not a restart: components whose recipe identity
and digest are unchanged are candidates to be kept running as-is (they show up
as `no_touch` in the reconcile log). Keeping one is only safe when there is
something watching it, because a reused component gets no new supervision loop
— whatever restart policy and health probe it has must already be attached.

So a component is reused only when all of these hold:

1. it is reported `running`;
2. it has a live supervision loop;
3. its process is alive (when it runs as a process); and
4. it is reported `healthy`, if it declares a health check.

Otherwise the agent tears down what is left of the previous instance and starts
a fresh one. The decision is taken twice — when the reconcile is planned and
again immediately before the stack starts — because a component can die in
between; the log records the outcome:

```
[agent] component=api msg=reusing existing running instance (no restart)
[agent] component=api msg=reuse revoked, starting a fresh instance
```

Reuse is never assumed from cached state alone. Cached state records the last
observed transition, and a component can read `running/healthy` while its
process is already gone; adopting one of those would freeze the API on a lie
and leave the component unsupervised (see issue #10).

## Recovery after an agent crash

If the agent itself is killed (SIGKILL, OOM, segfault), its children are
reparented to init and survive with no supervisor. On the next boot the agent
reaps any orphan recorded in its snapshot, resets the persisted component
states, and re-applies the last plan from scratch. Persisted `running` states
are informational after a crash, never authoritative.
