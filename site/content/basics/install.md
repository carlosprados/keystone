+++
title = "Install"
weight = 13
description = "Get the binaries, or build them from source."
+++

# Install

Keystone is a single static binary per architecture. No runtime dependencies —
`CGO` is disabled at build time.

## From releases

Grab the archive for your architecture from the
[releases page](https://github.com/carlosprados/keystone/releases). Builds are
published for Linux `amd64`, `arm64` and `armv7`, which covers most edge hardware
from an industrial PC down to a Raspberry Pi.

```bash
tar xzf keystone_*_linux_arm64.tar.gz
sudo install -m 0755 keystone keystonectl /usr/local/bin/
keystone --version
```

{{% notice style="warning" title="Verify what you install" %}}
Released binaries are **not signed yet** (no cosign signature, no SBOM). Until
that lands, check the published checksums and fetch over HTTPS only. It is tracked
as a known limitation in the [security model](../../security/).
{{% /notice %}}

## From source

Requires Go 1.24+ and [Task](https://taskfile.dev/).

```bash
git clone https://github.com/carlosprados/keystone.git
cd keystone
task build          # builds keystone, keystonectl, keystoneserver
task test           # go test ./...
```

`task` with no arguments lists every available target. There is no Makefile.

## The three binaries

| Binary | What it is for |
|---|---|
| `keystone` | The agent. This is what runs on the device. |
| `keystonectl` | Command-line client. Talks to an agent over its HTTP API. |
| `keystoneserver` | A tiny static file server, handy for serving test artifacts while you develop. |

## Running it

```bash
keystone --http 127.0.0.1:8080
```

That is a working agent with the HTTP adapter on loopback. It has no plan yet, so
it is supervising nothing.

{{% notice style="note" %}}
Binding anything other than loopback **requires** an API token — the agent refuses
to start otherwise, rather than quietly exposing an unauthenticated control plane
on the network. See [API authentication](../../security/api-auth/).
{{% /notice %}}

For a real deployment, run it under systemd; there is a unit example in
[Running under systemd](../../operations/systemd/).
