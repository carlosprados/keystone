# Periodic reconcile: design

A clock-driven adapter that re-runs the current plan on an interval, so a
device repairs itself without anyone sending it a message.

This is the smallest of the pull-mode phases and the only one that depends on
nothing else. It does **not** refresh datasets — see
[datasets-and-signing-design.md](datasets-and-signing-design.md) for why a
periodic reconcile cannot do that on its own. The two phases share one piece:
the monotonic scheduler and its device-derived jitter, introduced here and
reused there.

Status: **design, not implemented.**

---

## What it fixes

When `RunManaged` exhausts `maxRetries`, `handleComponentExit` records the
component as `failed` (`internal/agent/agent.go:1477-1501`) and **it stays that
way until a message arrives**. On a gateway behind an OT firewall with no
control-plane connectivity, that is forever. The component is dead, the agent
knows it is dead, and nothing tries again.

The repair mechanism already exists and needs no new logic. A reconcile calls
`componentIsReusable` for every component the plan says should be running
(`internal/agent/plan_reconcile.go:493-513`); a component that is not running,
not supervised, or whose PID is gone fails that check and lands in
`startTargets`. Running components pass it and are left strictly alone.

So the whole feature is: call the thing that already repairs, on a timer,
safely.

## Why an adapter

`adapter.Adapter` is "a transport that turns external events into
`CommandHandler` calls" (`internal/adapter/adapter.go:14-28`). A clock is a
transport whose events happen to be scheduled. The fit is exact, and it buys:

- lifecycle handled by `Registry.StartAll`/`StopAll`, including the shutdown
  timeout already wired in `cmd/keystone/main.go`;
- registration behind a flag, identical in shape to NATS and MQTT;
- tests against a fake `CommandHandler`, the pattern the NATS and MQTT adapter
  tests already use;
- **no new code in `internal/agent/agent.go`**, which is already 1979 lines.

## The one interface change: `ReconcileNow`

The tempting version — the adapter reads `GetPlanStatus().PlanPath` and calls
`ApplyPlan(path, false)` — is wrong, and the reason is worth stating precisely
because it is invisible from outside the agent.

`ApplyPlan` enters `applyPlanReconcileUnlocked(path, dry, allowRollback=true)`.
That function captures `oldPlanPath := a.planPath` before applying
(`plan_reconcile.go:104-107`) and, on failure, rolls back to it
(`:154-171`) — by calling `stopPlanInternal(false)`, **which stops every
component in the plan**, and then re-applying.

On a normal apply that is correct: the previous plan is a different, known-good
plan. On a *reconcile of the same plan* the "previous plan" is the plan that
just failed. So a failing reconcile would stop the entire healthy stack and
re-apply the same failing plan — and on a timer, it would do that again at
every tick, for as long as the cause persists. A recipe whose signing
certificate expired overnight would take down every component on the device
every fifteen minutes.

Hence: **add `ReconcileNow() (*ReconcileResult, error)` to `CommandHandler`**,
implemented by the agent, which internally calls
`applyPlanReconcileUnlocked(path, false, /*allowRollback=*/false)`.

Keeping the decision inside the agent also keeps the adapter ignorant of the
plan state machine — which has six states today and will have more — and gives
the other transports something genuinely useful: a `POST /v1/plan/reconcile`
and a `keystonectl reconcile` that mean "repair now", distinct from "apply this
plan".

```go
type ReconcileResult struct {
    Skipped  bool     `json:"skipped"`
    Reason   string   `json:"reason,omitempty"`   // why it was skipped
    Repaired []string `json:"repaired,omitempty"` // components restarted
    Duration string   `json:"duration"`
}
```

`Repaired` is what justifies the feature's existence in an operator's eyes, so
it is worth returning rather than only logging.

Cost of the change: `CommandHandler` is implemented by `Agent` and by the fakes
in the HTTP, NATS and MQTT adapter tests. Those need the new method. Small,
mechanical, and localised.

## What it must refuse to do

**Never resurrect a plan an operator stopped.** Someone stops the plan to work
on the device; fifteen minutes later the agent quietly starts everything again.
That is the failure that would make people disable the feature permanently.

The predicate already exists and already encodes exactly this judgment:
`shouldResumeLastPlan` (`agent.go:1656-1663`) refuses `stopped` and `dry-run`
and accepts everything else, including the interrupted `applying`. `StopPlan`
sets `stopped` (`handler.go:128-132`), so the two line up.

`ReconcileNow` reuses that predicate rather than restating it. The question
"should I bring this plan up without being asked?" is the same question at boot
and at every tick, and it must not be possible for the two answers to drift
apart.

Also skipped, and reported rather than treated as an error:

- an apply already in progress — `applyInProgress` (`agent.go:49-50`) is a
  `CompareAndSwap` guard; the tick reports `skipped` and waits for the next one;
- no plan applied yet (`planPath == ""`).

## Cost of a reconcile that finds nothing wrong

Not zero, and this decides the default. Each pass calls `loadPlannedState`,
which for **every** component:

- re-reads the recipe and re-verifies its detached signature
  (`resolveRecipeRef` → `verifyRecipeFileSignature`, `plan_reconcile.go:519-549`);
- re-computes the SHA-256 of the recipe file to get its digest;

and then, at the end of a successful apply, `applyPlan` runs `artifact.GC` and
`EnforceCacheLimit` over `runtime/artifacts` (`agent.go:787-799`).

On a small gateway with twenty components that is real periodic work — RSA
signature verification is not free on ARMv7. Two consequences:

1. **Off by default.** `--reconcile-interval 0` disables it, and 0 is the
   default. A feature that switches itself on changes the behaviour of every
   device in the fleet the moment the binary is updated; opting in is the
   difference between a fix and an incident.
2. **Suggest 15m, not 1m.** The failure being repaired is a component that
   died and will not come back on its own. Minutes of extra downtime on that
   are irrelevant; the periodic cost is not.

Worth measuring during implementation rather than assuming: the per-pass cost
on a real plan, so the documentation can state a number instead of a warning.

## Scheduling, jitter and backoff

A monotonic `time.Ticker`. Not a wall clock, and not cron: a device without an
RTC steps its clock by decades when NTP first syncs, and every wall-clock
scheduler mishandles that in its own way.

**Jitter derived from the device ID**, not from a random source: hash the ID to
a stable offset within the interval. A thousand devices then spread evenly, and
each one lands in the same slot every time, which is what makes a report
reproducible when something goes wrong at 03:14 on one gateway. This is the
piece the dataset phase reuses.

**The first tick waits a full interval.** `agent.New` already launches a resume
apply in a goroutine when the snapshot says so (`agent.go:162-172`); a
reconcile firing immediately at startup would race it for `applyInProgress` and
report a pointless skip.

**Exponential backoff on consecutive failures**, capped, resetting to the
normal interval after one success. A recipe whose certificate expired will fail
every pass; hammering it every fifteen minutes produces noise in the logs and
work on the device, and fixes nothing.

## Configuration

| Flag | Env | Default |
|---|---|---|
| `--reconcile-interval` | `KEYSTONE_RECONCILE_INTERVAL` | `0` (disabled) |
| `--reconcile-jitter` | `KEYSTONE_RECONCILE_JITTER` | `10%` of the interval |

Both follow the existing `applyDurationEnv` pattern in `main.go:132-146`, where
an explicitly-set flag always beats the environment.

## Observability

```
keystone_reconcile_total{result}          # ok | skipped | failed
keystone_reconcile_duration_seconds
keystone_reconcile_repairs_total{component}
keystone_reconcile_last_timestamp
```

`keystone_reconcile_repairs_total` is the one that answers "is this feature
earning its keep?" — a device that never repairs anything and a device that
repairs the same component nightly are two very different situations, and only
this counter distinguishes them.

`PlanStatus` gains `lastReconcile` and `lastReconcileResult`, which means
`internal/adapter/http/routes.go` changes and `task openapi` must be run.

## Tests worth writing

- A reconcile pass over a healthy plan restarts **nothing**. This is the test
  that matters most: the whole feature is only safe because reuse works, and a
  regression here would restart production stacks on a timer.
- A component killed out of band is restarted by the next tick.
- A plan stopped by the operator is not resurrected, at any number of ticks.
- A tick during an apply reports `skipped`, not an error.
- A failing reconcile does **not** stop healthy components — the direct
  regression test for the rollback path described above.
- Backoff grows on consecutive failures and resets after a success.
- Jitter is deterministic for a given device ID and stays inside the interval.

## Documentation this touches

`task cli-docs` regenerates `site/content/reference/cli.md`; `task openapi`
regenerates the API reference. Then `site/content/reference/env.md`, the README
flag table, `site/content/operations/metrics.md`, and
`site/content/concepts/reconcile-and-reuse.md` — that page is a contract and
this phase gives it a second trigger, so it needs the new one described rather
than merely mentioned.

---

## Technical debt found while designing this

**The rollback path treats a re-apply of the same plan as if it were a
rollback to a different one.** Described in full above. It is latent today: the
boot resume calls `ApplyPlan(planPath, false)` with rollback enabled
(`agent.go:168`), so a resume that fails already stops every component and
re-applies the same plan once. Once is survivable and mostly invisible;
on a timer it would not be.

Proposed fix, separate from this phase and not to be bundled into it: make
`applyPlanReconcileUnlocked` skip the rollback when `oldPlanPath == planPath`
and the plan mapping is unchanged, since there is by definition nothing to roll
back to. That also makes the boot resume safer without any caller changing.
Needs its own test: a failing resume must leave healthy components running.
