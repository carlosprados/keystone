+++
title = "Examples"
weight = 25
+++

Complete, copy-pasteable deployments: the recipes, the plan, and the exact calls to
get them onto a device over each control plane.

Everything here uses one fictional stack, built up across the pages: a time-series
database, an HTTP API that depends on it, a telemetry agent, and a static web front
end in a container. It is deliberately the shape of a real edge deployment rather
than a hello-world.

{{% children type="flat" depth="1" description="true" %}}

## Conventions in these examples

- The agent runs with its working directory at `/var/lib/keystone`, so
  `runtime/…` paths below are relative to that.
- Recipes are named in reverse DNS (`com.acme.api`) and components get short names
  in the plan (`api`). The two are different things — see
  [The four words](../basics/key-ideas/).
- Where an artifact is downloaded, it is signed. Skipping that is a development
  shortcut, not a deployment pattern.
