# Does restarting the agent restart everything it supervises?

It should not, and this proves it either way in about two minutes.

The claim being tested is the one that decides whether updating the agent costs
an outage: a component that outlived the previous agent is **supervised again**,
not started again. The evidence is a PID that does not change.

## What the canary is

A process that does nothing durably, prints its own PID once, and holds no
state. It declares a health probe on purpose — a component without one would
look identical whether re-adoption worked or not, because its health reads
`unknown` either way. With a probe, the agent has to genuinely be watching for
it to report healthy after the restart.

Removing it is deleting this directory. It writes nothing elsewhere.

## Before anything: the recipe has to be signed

Keystone refuses a recipe loaded from a file unless it carries a valid detached
signature. That is not a detail to work around — it is the reason the recipe
cannot quietly become something else between writing it and running it.

The obvious shortcut, `--insecure-skip-verify`, is a flag on the **agent**, not
on `keystonectl`. It disables integrity checking for everything that agent ever
loads, not just this canary, and it is exactly the kind of "temporary" that
stays. On a device running anything real, do not.

Sign it instead:

```bash
scripts/dev-sign.sh configs/examples/readoption-canary/es.keystone.readopt-canary.recipe.toml

# check it the way the agent will, before trusting the device to
keystonectl verify   --trust-bundle configs/trust/ca.pem   --cert configs/trust/leaf.pem   configs/examples/readoption-canary/es.keystone.readopt-canary.recipe.toml
```

The agent needs the trust material, usually through its environment file:

```
KEYSTONE_TRUST_BUNDLE=/etc/keystone/ca.pem
KEYSTONE_LEAF_CERT=/etc/keystone/leaf.pem
```

The keys `dev-sign.sh` produces are throwaway and not secret. A CA installed for
an experiment should come back out when the experiment ends: whatever it signs,
that device will accept.

## Paths in the plan

`plan.toml` here points at the recipe by a path relative to the repository root,
which works when you run from a checkout and nowhere else. On a device, give the
component its own directory and use absolute paths:

```toml
[[components]]
name = "readopt-canary"
recipe = "/opt/keystone/canary/es.keystone.readopt-canary.recipe.toml"
```

The signature (`<recipe>.sig`) has to travel with it.

## The check

**Read the PID the same way before and after.** "It kept its PID" is exactly the
kind of claim that gets believed without looking.

```bash
PID_CMD='curl -s localhost:8080/v1/components | jq -r ".[] | select(.name==\"readopt-canary\") | \"\(.pid) \(.state) \(.last_health)\""'

keystonectl apply configs/examples/readoption-canary/plan.toml
sleep 15
eval "$PID_CMD"          # before: <pid> running healthy

# NOT `systemctl restart`, and NOT without --kill-whom=main. See below.
sudo systemctl kill -s SIGKILL --kill-whom=main keystone
sleep 20
eval "$PID_CMD"          # after:  SAME pid, running healthy
```

### Why `--kill-whom=main`

Without it, `systemctl kill` signals **every process in the unit's cgroup** — the
default is `--kill-whom=all`. `KillMode=process` governs `stop`, not `kill`. So
the plain command kills the canary along with the agent, nothing survives, and
the result reads exactly like broken re-adoption. A field test got that false
negative. `--kill-whom=main` kills the agent alone, which is what a crash is.

### Why not `systemctl restart`

Because it does not test this, and a field test spent an afternoon finding out.

`restart` sends SIGTERM, and on SIGTERM the agent runs its own shutdown: it
stops its components through their lifecycle hooks, in dependency order. That
is correct — `systemctl stop` sends the same signal, the agent cannot tell the
two apart, and leaving processes alive that nothing watches is worse than
restarting them. But it means nothing survives to be adopted, and the canary
comes back with a new PID having proved nothing.

Re-adoption applies where the agent did **not** get to shut down cleanly:

- it was killed (`SIGKILL`, OOM, a crash), which is what the command above
  simulates;
- or it exited to **replace its own binary**, where it deliberately leaves
  components running because it is coming straight back.

The second is the case that matters in production, and it is the reason the
feature exists: without it, every self-update restarts everything the agent
supervises.

Confirm from outside the agent too, since the whole point is not to take its
word for it:

```bash
ps -o pid,ppid,lstart,args -p <pid>
journalctl -u keystone | grep -E 'readopt-canary|adopted existing process'
```

`lstart` is the tiebreaker: an unchanged PID with a start time *after* the
restart would mean the PID was reused, not that the process survived.

## What each outcome means

| Result | Meaning |
|---|---|
| Same PID, `adopted existing process` in the log | Working. Supervision resumed without an interruption |
| New PID, `reaping orphan` in the log | The process survived and was not adopted: the reconcile saw the component as changed. Up to v0.12.1 this was every case — the check asked for a supervised component, and after a crash there is none |
| New PID, no adoption line at all | The process died with the agent. Check `KillMode=process` is in the unit, that the kill used `--kill-whom=main`, and that the agent was killed rather than asked to stop — a clean shutdown stops components on purpose |
| Same PID but health stays `unknown` | Worse than a restart: the process is alive and **nobody is supervising it** |

That last row is the one to watch. Re-adoption is only worth having if the
health probe and restart policy come back with it; a live process that nothing
watches, reported as running, is the failure this whole design is against.

## Requirements

`KillMode=process` in the unit (`configs/systemd/keystone-ab.service` has it).

Survivors are recognised by being reparented to PID 1. Under a **subreaper** —
a user session's `systemd --user`, a container with `tini` or `dumb-init` — they
are reparented to it instead, and are then neither adopted nor reaped. Run this
under the system manager, as the unit above does.
Without it systemd kills every process in the agent's cgroup on restart, the
canary dies with the agent, and re-adoption cannot happen — the feature would
look broken while doing exactly what it was asked.
