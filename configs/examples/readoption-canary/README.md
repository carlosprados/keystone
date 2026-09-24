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

sudo systemctl restart keystone
sleep 15
eval "$PID_CMD"          # after:  SAME pid, running healthy
```

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
| New PID, `reaping orphan` in the log | The component was not adopted. Either the reconcile saw it as changed, or the process did not survive |
| New PID, no adoption line at all | The process died with the agent — check `KillMode=process` is in the unit, because systemd's default kills the whole cgroup |
| Same PID but health stays `unknown` | Worse than a restart: the process is alive and **nobody is supervising it** |

That last row is the one to watch. Re-adoption is only worth having if the
health probe and restart policy come back with it; a live process that nothing
watches, reported as running, is the failure this whole design is against.

## Requirements

`KillMode=process` in the unit (`configs/systemd/keystone-ab.service` has it).
Without it systemd kills every process in the agent's cgroup on restart, the
canary dies with the agent, and re-adoption cannot happen — the feature would
look broken while doing exactly what it was asked.
