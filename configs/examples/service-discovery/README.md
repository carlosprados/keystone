# Two containers that talk to each other by name

The case this example exists for: a component needs to reach a sibling over the
network, and the sibling's container name is not usable as an address because
Keystone appends a timestamp to it on every start.

The address is the **network alias**, which defaults to the component name — so
`api` reaches `solver` at `solver:50051` without either recipe declaring
anything beyond the network.

## Before applying

Keystone does not create the network. Create it once, on the machine:

```bash
docker network create solver-net
```

Then apply:

```bash
./keystonectl apply configs/examples/service-discovery/plan.toml
```

## What to notice

- `network_mode = "solver-net"` is a user-defined network. The default `bridge`
  has no embedded DNS resolver, so names do not resolve there.
- `runtime = "docker"` is explicit. Under `auto`, a machine where containerd
  runs under a different namespace than Keystone's (Docker uses `moby`) will
  look for the image in an empty namespace and try to pull it from a public
  registry instead — a configuration mistake that surfaces very far from its
  cause.
- `solver` probes with `exec`, not `cmd:`. Its image has no shell.
- Both images are pinned by digest tag, not `latest`. A rollback re-applies the
  previous plan, and a moving tag would make it reload the same image while
  reporting success.
