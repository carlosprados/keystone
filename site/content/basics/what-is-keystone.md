+++
title = "What is Keystone?"
weight = 11
description = "The problem it solves, and what it deliberately is not."
+++

Keystone is an **agent**: a small program that runs on a device and takes care of
the other programs on that device.

You never log into the device to start things by hand. You describe what should be
running, and the agent makes reality match the description.

{{% notice style="primary" title="Like you're five" %}}
A **plan** is your shopping list. A **recipe** is how to make one thing on the
list. A **component** is the thing once it is made and running. The **agent** is
the cook who reads the list, follows each recipe, and keeps an eye on the pans.
{{% /notice %}}

## The problem

You have a hundred devices in a hundred factories. Each needs a handful of
programs: a data collector, a small database, an API, a metrics agent. They must
start in the right order — the API is useless before the database is up. Programs
crash. Networks drop mid-update. Nobody is on site.

Doing this by hand does not scale, and the usual cloud answer (Kubernetes) is far
too heavy for a box with 512 MB of RAM and no reliable uplink.

## What Keystone does

| Job | How |
|---|---|
| Install software | Downloads **artifacts**, checks their SHA-256 and signature, unpacks them, runs an install hook |
| Start it in order | Builds a dependency graph and starts each layer in parallel |
| Keep it alive | Health probes plus a restart policy per component |
| Update safely | A new plan is reconciled: only what changed is touched, and a failed apply rolls back |
| Report the truth | `GET /v1/components` never claims a dead component is running |
| Survive reboots | State is persisted; on boot the agent reaps orphans and re-applies the last plan |
| Reduce privilege | Per-component user, capability allow-list and `no_new_privileges` |

## What it is not

- **Not a scheduler.** Keystone does not decide *which* device runs what. It does
  what its plan says. Placement is your fleet manager's job.
- **Not a container platform.** It can run containers, but a container runtime is
  optional. There is no image registry, no CNI orchestration beyond the basics, no
  service mesh.
- **Not multi-tenant.** One agent, one plan, one device.
- **Not a cluster.** Devices do not talk to each other or elect leaders.

That narrowness is the point: it is why a single static binary can do the job on a
device that could never run anything bigger.

## How you talk to it

Three interchangeable front doors, called **adapters**, all driving the same logic:

- **HTTP** — a small REST API. On by default, bound to loopback.
- **NATS** — subject-based messaging, with an optional JetStream job queue.
- **MQTT** — broker topics, QoS, last-will. The classic IoT choice.

You can enable several at once. See [Control planes](../../control-planes/).
