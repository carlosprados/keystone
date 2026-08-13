+++
title = "TOML cheat sheet"
weight = 74
description = "The four constructs every recipe and plan is made of, and the mistakes the parser will not catch for you."
+++

Recipes and plans are TOML, and nothing else. The whole format reduces to four
constructs — this page is those four, the shape they build, and the handful of
mistakes that do **not** produce an error message.

{{% notice style="warning" title="Read this first" %}}
**A misspelled field name is not an error.** Keystone ignores keys it does not
recognise, so `restart_polciy = "never"` parses cleanly, sets nothing, and the
component ends up with the default — `always`. You asked for a process that never
restarts and you got one that restarts forever, silently. See
[Traps](#traps-the-parser-will-not-catch) before you trust a file you have edited by hand.
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
another element. Three tables in Keystone repeat this way — `[[artifacts]]`,
`[[dependencies]]` and, in a plan, `[[components]]`.

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

## Traps the parser will not catch

### An unknown key is silently ignored

The decoder does not run in strict mode and the schema does not forbid extra
properties, so anything Keystone does not recognise is dropped without a word.
The failure is not that the field is missing — it is that the **default takes
over**, and the defaults are deliberately permissive:

```toml
[lifecycle.run]
restart_polciy = "never"       # typo: ignored → restart_policy stays empty
```

An empty `restart_policy` resolves to `always`. The component you wanted dead
after one run restarts forever.

### A key can land in the wrong table without complaint

A key belongs to the header above it. This is valid TOML, and it does the wrong
thing for the same reason as the typo:

```toml
[lifecycle.run.exec]
command = "./api"
restart_policy = "never"       # this is exec.restart_policy — nothing reads it
```

Put scalars **before** the sub-table headers, or you will keep adding keys to the
last table you opened rather than the one you meant.

### What *is* caught

Not everything slips through. A wrong **value** is rejected loudly, even though a
wrong **key** is not:

```
restart_policy = "sometimes"
  → invalid recipe: … value must be one of "never", "on-failure", "always"

version = 1.4
  → toml: float cannot be assigned to string
```

So: types and enumerations are enforced, field names are not.

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
fields and unresolvable dependencies.

**It does not catch a misspelled field name**, for the reason above. For a file
you have hand-edited, the check that works is reading back what the agent
actually loaded — `GET /v1/plan/status` and the component's behaviour — rather
than trusting that a clean apply means the file said what you meant.
