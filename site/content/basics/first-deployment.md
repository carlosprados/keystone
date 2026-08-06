+++
title = "Your first deployment"
weight = 14
description = "A two-component stack on your own machine, start to finish."
+++

# Your first deployment

Ten minutes, no devices, no containers. We will deploy two toy components with a
dependency between them, watch them run, kill one, and watch Keystone put it back.

## 1. A working directory

The agent keeps its state and installed components under `runtime/` in whatever
directory you start it from, so give it its own.

```bash
mkdir -p ~/keystone-demo/recipes && cd ~/keystone-demo
```

## 2. Two recipes

`recipes/clock.toml` — a component that just keeps running:

```toml
[metadata]
name = "clock"
version = "1.0.0"

[lifecycle.run]
restart_policy = "always"

[lifecycle.run.exec]
command = "/bin/sh"
args = ["-c", "while true; do date; sleep 5; done"]
```

`recipes/reporter.toml` — a component that depends on the first:

```toml
[metadata]
name = "reporter"
version = "1.0.0"

[lifecycle.run]
restart_policy = "always"

[[dependencies]]
name = "clock"

[lifecycle.run.exec]
command = "/bin/sh"
args = ["-c", "while true; do echo reporting; sleep 7; done"]
```

The `[[dependencies]]` block refers to the **recipe** name `clock`. Keystone maps
it to whichever component in the plan uses that recipe.

## 3. A plan

`plan.toml`:

```toml
[[components]]
name = "clock"
recipe = "recipes/clock.toml"

[[components]]
name = "reporter"
recipe = "recipes/reporter.toml"
```

## 4. Start the agent

```bash
keystone --http 127.0.0.1:8080 --insecure-skip-verify
```

{{% notice style="warning" %}}
`--insecure-skip-verify` turns off the mandatory artifact signature checks. These
recipes have no artifacts to verify, but the flag keeps the demo friction-free.
**Never use it on a real device** — see
[Secure defaults](../../security/secure-defaults/).
{{% /notice %}}

## 5. Apply the plan

In another terminal:

```bash
curl -X POST --data-binary @plan.toml http://127.0.0.1:8080/v1/plan/apply
```

The agent uploads and parses the plan, resolves the graph, and starts `clock`
first, then `reporter`. The log tells the story:

```
[agent] reconcile stop_order=[] start_order=[clock reporter] no_touch=[]
[supervisor] layer=0 components=[clock] msg=starting layer
[supervisor] component=clock state=running
[supervisor] layer=1 components=[reporter] msg=starting layer
[supervisor] component=reporter state=running
[supervisor] all components running
```

## 6. Look at what is running

```bash
curl -s http://127.0.0.1:8080/v1/components | jq
```

```json
[
  { "name": "clock",    "state": "running", "restarts": 0, "pid": 40211, "last_health": "unknown" },
  { "name": "reporter", "state": "running", "restarts": 0, "pid": 40219, "last_health": "unknown" }
]
```

`last_health` is `unknown` because neither recipe declares a health check — that
is normal, not a problem. See [Health checks](../../internals/runners/).

## 7. Break something

```bash
kill -9 40211        # the clock's PID
sleep 3
curl -s http://127.0.0.1:8080/v1/components | jq '.[0]'
```

```json
{ "name": "clock", "state": "running", "restarts": 1, "pid": 41002 }
```

New PID, `restarts: 1`. The restart policy was `always`, so the runner brought it
back. Nothing you had to do.

## 8. Re-apply the same plan

```bash
curl -X POST --data-binary @plan.toml http://127.0.0.1:8080/v1/plan/apply
curl -s http://127.0.0.1:8080/v1/components | jq '.[].pid'
```

The PIDs do **not** change. Applying an unchanged plan is a no-op: Keystone
reconciles rather than restarts. This matters on a device you cannot afford to
disturb. See [Reconcile and reuse](../../concepts/reconcile-and-reuse/).

## 9. Stop everything

```bash
curl -X POST http://127.0.0.1:8080/v1/plan/stop
```

## Using the CLI instead

Everything above works through `keystonectl` too:

```bash
keystonectl status
keystonectl apply plan.toml
keystonectl restart clock
```

## What to read next

- [Recipes](../../concepts/recipes/) — every field you can write.
- [How it works inside](../../internals/) — what the agent did during that apply.
- [Security](../../security/) — what to change before this touches a real device.
