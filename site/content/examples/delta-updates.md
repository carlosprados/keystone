+++
title = "Updating over a metered link"
weight = 255
description = "Ship a 34 MB release as a 1 MB patch: a delta server, a recipe, and what the agent does with them."
+++

A component is already installed and a new version is out. The whole archive is
34 MB; the change between the two is 1 MB. This walkthrough sets up the delta
server, points a recipe at it, and shows the agent patching instead of
downloading.

The numbers here are real, measured on two consecutive Keystone releases while
writing this page. Your artifacts will differ — see
[what to expect](#what-to-expect).

## What you need

- The two versions of your artifact, **uncompressed** — `.tar`, not `.tar.gz`.
  The publisher keeps publishing `.tar.gz` exactly as before; the uncompressed
  form is what the patch is computed over, and it never has to be published.
- A host to run the delta server on, reachable from the devices.
- [ota-updater](https://github.com/carlosprados/ota-updater) — one implementation
  of the delta route the agent expects. Any server exposing
  `GET /delta/{from}/{to}` works.

## 1. Prepare the two versions

The agent patches the *uncompressed* archive, so that is what the server needs.
If you publish `.tar.gz`, decompress each release once:

```bash
gunzip -c myapp-1.0.0.tar.gz > myapp-1.0.0.tar
gunzip -c myapp-1.1.0.tar.gz > myapp-1.1.0.tar

sha256sum myapp-1.0.0.tar myapp-1.1.0.tar
```

Keep the digest of the **new** one. That is what goes in the recipe, and it is
what the agent verifies the patched result against.

## 2. Run the delta server

```bash
git clone https://github.com/carlosprados/ota-updater && cd ota-updater
go build -o update-server ./cmd/update-server
go run ./tools/keygen -out ./keys

mkdir -p store/binaries store/deltas
```

`server.yaml`, with the first release declared:

```yaml
http:
  addr: "127.0.0.1:8099"
  write_timeout: "10m"       # a 1 MB patch over a 20 kbps link takes ~7 minutes
store:
  binaries_dir: "./store/binaries"
  deltas_dir: "./store/deltas"
  state_file: "./store/artifacts.json"
crypto:
  private_key: "./keys/server.key"
artifacts:
  - name: "myapp"
    os: "linux"
    arch: "amd64"
    version: "1.0.0"
    binary: "./myapp-1.0.0.tar"
default_artifact: "myapp/linux/amd64"
admin:
  token: "0123456789abcdef0123456789abcdef"   # openssl rand -hex 16
```

```bash
./update-server -config ./server.yaml
curl -s http://127.0.0.1:8099/health
```

## 3. Publish the new version

The previous release stays in the store as a source to patch *from*; the new one
becomes the target:

```bash
curl -s -X POST http://127.0.0.1:8099/admin/artifacts \
  -H "Authorization: Bearer 0123456789abcdef0123456789abcdef" \
  -H "Content-Type: application/json" \
  -d '{"name":"myapp","os":"linux","arch":"amd64",
       "version":"1.1.0","binary":"./myapp-1.1.0.tar"}'
```

The response echoes the new `target_hash` and lists the previous one under
`history` — that pair is what a patch can be computed between.

## 4. Point the recipe at it

Only the `[artifacts.delta]` block is new. Everything else, including the
`.tar.gz` the artifact is normally downloaded from, stays as it is:

```toml
[metadata]
name = "com.example.myapp"
version = "1.1.0"

[[artifacts]]
uri = "https://downloads.example.com/myapp-1.1.0.tar.gz"
sha256 = "9f2c8b1e…"                # digest of the .tar.gz, as always
sig_uri = "https://downloads.example.com/myapp-1.1.0.tar.gz.sig"
unpack = true

[artifacts.delta]
server = "http://127.0.0.1:8099"    # the delta server
sha256 = "2631f4a7…"                # digest of myapp-1.1.0.tar, from step 1

[lifecycle.run.exec]
command = "./myapp"
```

Re-sign the recipe after editing it — the block is part of the signed bytes:

```bash
./scripts/dev-sign.sh myapp.recipe.toml
```

## 5. Apply

```bash
./keystonectl apply plan.toml
```

**On a device installing for the first time** there is nothing to patch from, so
the agent downloads the whole archive and says so:

```
[artifact] delta unavailable for https://downloads.example.com/myapp-1.1.0.tar.gz
          (no local base version to patch from); downloading the whole artifact
```

**On a device that already runs 1.0.0** the agent decompresses the copy it has,
asks for the patch and reconstructs the release:

```
[artifact] patch not ready yet; retrying in 15s (2/4)
[artifact] delta applied: base myapp-1.0.0.tar.gz (cd17c9e446c2) + 1034559 B patch -> 34207744 B
[artifact] https://downloads.example.com/myapp-1.1.0.tar.gz installed from a delta patch
```

That first line is normal and worth understanding: the very first request for a
patch that has never been asked for finds nothing cached, so the server answers
"not yet" **and starts computing it**. Generating one took about 10 seconds for a
34 MB artifact. The agent waits and asks again; every device after this one finds
it ready.

## What to expect

| | Size | Share |
|---|---:|---:|
| The archive, downloaded whole | 13.4 MB | 100 % |
| The patch, two consecutive releases | 1.0 MB | 8 % |
| The patch, across a Go toolchain change | 6.3 MB | 47 % |

**The saving is not a constant.** A release that relinks or reorders the binary —
a toolchain bump is the usual cause — produces a much larger patch, because Go
binaries move far more than the size of the source change suggests. Both rows
above are the same pair of tools, measured a few releases apart.

Below roughly 1 MB of artifact the machinery is not worth it: the patch approaches
the artifact, and you have added a server for nothing.

## When it does not work, nothing breaks

Every one of these downloads the whole artifact and continues:

- no previous version on disk;
- the server holds no patch from *this* device's version;
- the patch fails to apply, or produces bytes that do not match the recipe's
  digest;
- the base is larger than `KEYSTONE_DELTA_MAX_BASE_BYTES`;
- the server moved to a patch format this agent does not implement.

None of them fails the apply, and the reason is always in the log. A delta is an
optimisation; the download is the contract.

## Before you enable it on a small device

Patching is done in memory. The base is memory-mapped rather than read onto the
heap, but the reconstructed archive is not, so peak use is roughly the size of
the artifact on top of the agent's usual footprint. On a 34 MB artifact that is
~34 MB. `KEYSTONE_DELTA_MAX_BASE_BYTES` (256 MiB by default) is the cap.

The trust model changes too — the patched bytes are attested by whoever signed
the recipe rather than by the artifact's own signature. That is spelled out in
[Artifacts](../../internals/artifacts/#delta-downloads); read it before enabling
this in a deployment that relies on a separate publisher key.
