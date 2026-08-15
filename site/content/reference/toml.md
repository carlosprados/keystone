+++
title = "TOML cheat sheet"
weight = 74
description = "The four constructs every recipe and plan is made of, and where a misspelled field shows up."
+++

Recipes and plans are TOML, and nothing else. The whole format reduces to four
constructs — this page is those four, the shape they build, and the mistakes
worth knowing about.

{{% notice style="warning" title="Read this first" %}}
**A misspelled field name does not stop an apply.** Keystone ignores keys it does
not recognise, on purpose: one recipe is published to many agents, and an agent
older than a field has to run the recipe rather than refuse it. The cost is that
`restart_polciy = "never"` sets nothing, and the component falls back to the
default — `always`.

So the agent reports what it ignored instead of hiding it, and a **dry run
refuses it outright**. Run one before you trust a file you have edited by hand:

```bash
curl -X POST --data-binary @plan.toml "localhost:8080/v1/plan/apply?dry=true"
```
{{% /notice %}}

## The four constructs

### 1. A table is a section

Square brackets open a section. Every key below it belongs to that section, until
the next header.

```toml
[metadata]
name = "com.acme.api"
version = "1.4.0"
```

### 2. A dotted name is nesting

`[lifecycle.run.exec]` is not a table called "lifecycle.run.exec". It is `exec`
inside `run` inside `lifecycle`. The dots *are* the tree.

```toml
[lifecycle.run]              # lifecycle → run
type = "process"

[lifecycle.run.exec]         # lifecycle → run → exec
command = "./api"
```

Nothing is indented in TOML — the header names the depth. These two are the same
document:

```toml
[lifecycle.run.exec]
command = "./api"
```

```toml
[lifecycle]
[lifecycle.run]
[lifecycle.run.exec]
command = "./api"
```

### 3. Double brackets repeat

`[[artifacts]]` is an **array of tables**: write the header again and you add
another element. Four tables in Keystone repeat this way — `[[artifacts]]`,
`[[datasets]]`, `[[dependencies]]` and, in a plan, `[[components]]`.

```toml
[[artifacts]]
uri = "https://example.com/api.tar.gz"
sha256 = "8b1a…"

[[artifacts]]                # a second artifact, not a redefinition
uri = "https://example.com/assets.tar.gz"
sha256 = "44f0…"
```

Both halves of that are enforced. Single brackets where an array belongs fails
with `toml: cannot store a table in a slice`, and repeating a single-bracket
table fails with `toml: table metadata already exists` — TOML refuses to define
the same table twice.

### 4. Values

| You want | Write | Used by |
|---|---|---|
| Text | `name = "api"` | almost everything |
| Text with backslashes or `\n` left alone | `path = 'C:\logs'` (single quotes) | Windows paths, regexes |
| Several lines | `script = """` … `"""` (see below) | install and shutdown scripts |
| A list | `args = ["-c", "config.yaml"]` | `args`, `capabilities` |
| True or false | `unpack = true` | `unpack`, `privileged`, `no_new_privileges` |
| A number | `failure_threshold = 3` | thresholds, ports, limits |
| A duration | `interval = "10s"` — **a string** | `interval`, `timeout` |
| A set of key/value pairs | a nested table, below | `env`, `headers`, `labels` |

Key/value maps are just tables, so they get a header of their own:

```toml
[lifecycle.run.exec]
command = "./api"

[lifecycle.run.exec.env]     # env is a table inside exec
LOG_LEVEL = "info"
PORT = "8080"
```

Multi-line strings are what install hooks want. The opening `"""` should be
followed by a newline; the leading newline is discarded, the rest is kept
verbatim:

```toml
[lifecycle.install]
script = """
mkdir -p ./data
chmod 0750 ./data
"""
```

## The shape of a recipe

One component: where to get it, how to install it, how to run it, how to know it
is alive, and what must be ready first. Every block below is optional except
`[metadata]` and `[lifecycle.run]`.

```toml
[metadata]                          # required: name + version are the identity
name = "com.acme.api"
version = "1.4.0"

[[artifacts]]                       # repeatable, optional
uri = "https://example.com/api-1.4.0.tar.gz"
sha256 = "8b1a…"                    # required unless --insecure-skip-verify
unpack = true

[lifecycle.install]                 # optional, runs once
script = "chmod +x ./api"

[lifecycle.run]                     # required
type = "process"                    # "process" (default) or "container"
restart_policy = "on-failure"
max_retries = 5

[lifecycle.run.exec]                # for type = "process"
command = "./api"
args = ["--config", "config.yaml"]

[lifecycle.run.exec.env]
LOG_LEVEL = "info"

[lifecycle.run.security]            # process confinement, optional
user = "nobody:nogroup"
no_new_privileges = true
capabilities = ["CAP_NET_BIND_SERVICE"]

[lifecycle.run.health]              # optional
check = "http://127.0.0.1:8080/healthz"
interval = "10s"

[lifecycle.shutdown]                # optional
script = "./api --drain"

[[dependencies]]                    # repeatable, optional
name = "com.acme.db"
type = "hard"
```

`[lifecycle.run.exec]` and `[lifecycle.run.container]` are the two halves of one
choice: `type = "process"` uses the first, `type = "container"` the second. A
container recipe replaces the `exec` and `security` blocks with:

```toml
[lifecycle.run]
type = "container"

[lifecycle.run.container]
image = "docker.io/library/nginx:1.27"

[[lifecycle.run.container.ports]]   # repeatable — double brackets
host_port = 8080
container_port = 80
```

Note the last header: `ports` and `mounts` repeat, so they take double brackets
*and* keep the full dotted path.

Field-by-field types and defaults are in
[Recipe and plan schema](../schemas/); the prose walkthrough is in
[Recipes](../../concepts/recipes/).

## The shape of a plan

A plan is a list of names bound to recipes. That is all it is.

```toml
[[components]]
name = "db"                                  # unique within the plan
recipe = "recipes/db.toml"                   # a path…

[[components]]
name = "api"
recipe = "com.acme.api:1.4.0"                # …or name:version from the store
```

There is no ordering in a plan. Order comes from `[[dependencies]]` in the
recipes — see [Dependencies](../../concepts/dependencies/).

## Traps

### An unknown key does not stop the apply

Keystone accepts a key it does not recognise. That is deliberate, and it is what
makes a fleet upgradeable: one recipe is published to many devices, and an agent
whose binary predates a field must still run the recipe rather than refuse it.
`[artifacts.delta]` reached existing fleets on exactly that property.

The cost is that a typo looks identical to a field from the future. The field you
meant stays unset, and the **default takes over**:

```toml
[lifecycle.run]
restart_polciy = "never"       # typo: ignored → restart_policy stays empty
```

An empty `restart_policy` resolves to `always`. The component you wanted dead
after one run restarts forever.

What the agent will not do is hide it. Every key it did not understand is named,
with its line, in three places:

| Where | What happens |
|---|---|
| **A dry run** | **Refuses the plan.** This is the authoring path: you are one edit away from a fix and nothing is deployed yet |
| The agent's log | `WARNING ignoring recipe recipes/api.toml (component api): "lifecycle.run.restart_polciy" (line 6)` |
| `GET /v1/plan/status` | an `unknownFields` array, for an operator who is not reading stdout |

If `unknownFields` is non-empty on a device, either that agent is older than the
recipe — which is fine and expected mid-rollout — or somebody has a typo.

### A key can land in the wrong table without complaint

A key belongs to the header above it. This is valid TOML, and it lands as an
unknown field for the same reason the typo does:

```toml
[lifecycle.run.exec]
command = "./api"
restart_policy = "never"       # this is exec.restart_policy — nothing reads it
```

Put scalars **before** the sub-table headers, or you will keep adding keys to the
last table you opened rather than the one you meant. A dry run catches this one
too.

### What is rejected outright

A wrong **value** never gets the benefit of the doubt, at any stage — there is no
fleet-compatibility argument for a value the schema already enumerates:

```
restart_policy = "sometimes"
  → invalid recipe: … value must be one of "never", "on-failure", "always"

version = 1.4
  → toml: float cannot be assigned to string

[artifacts]                    # single brackets on a repeatable table
  → toml: cannot store a table in a slice
```

### `capabilities = []` is not the same as omitting it

Declaring the key — even empty — drops every capability from the bounding set.
Omitting it leaves capabilities untouched. `nil` and `[]` differ on purpose; see
[Process privileges](../../security/process-privileges/).

### Versions are strings

`version = "1.4.0"` is a string. `version = 1.4` is a float and is rejected;
`version = 1.4.0` is not valid TOML at all.

## Checking a file before you deploy it

```bash
curl -X POST --data-binary @plan.toml "localhost:8080/v1/plan/apply?dry=true"
```

A dry run parses the plan, loads every recipe, verifies signatures, enforces the
schema and computes the reconcile plan — without installing or starting anything.
It catches malformed TOML, bad types, invalid enum values, missing required
fields, unresolvable dependencies **and every key the agent does not recognise**,
which a real apply deliberately tolerates.

That last one is why a dry run is worth running on a file you have hand-edited
even when you are sure of it: it is the only place a misspelled field is an
error rather than a line in a log.

```
unknown fields, which the agent would ignore:
  recipe recipes/api.toml (component api): "lifecycle.run.restart_polciy" (line 6)
```

One caveat worth stating: a dry run is checked against **the agent you ran it
on**. A recipe using a field newer than that agent's binary will be rejected by
it and accepted by a newer one — which is the same tolerance seen from the other
end. Run the dry run against an agent at least as new as the recipe.
