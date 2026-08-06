+++
title = "Secure by default"
weight = 41
description = "What is locked down out of the box, and the one escape hatch."
+++

# Secure by default

The defaults assume a hostile network and an unattended device. Nothing needs to be
switched on to be safe; things need to be switched *off* to be convenient.

{{% notice style="primary" title="Like you're five" %}}
The toy box is locked. It only opens for a note that has your signature on it, and
only if the note came through the little slot at the front — not through the window.
{{% /notice %}}

## Controls at a glance

| Surface | Control | Default |
|---|---|---|
| HTTP bind | Loopback only; a non-loopback address **requires** a token | secure |
| HTTP auth | Bearer token, constant-time compare, `/healthz` exempt | on when a token is set |
| Plan submission | Content upload only; a remote `planPath` is rejected | secure |
| Request size | Capped, 413 on overflow (4 MiB default) | secure |
| Slowloris | Read/header/idle timeouts set | secure |
| Artifact integrity | SHA-256 **and** detached signature, mandatory | fail-closed |
| Recipe integrity | File-loaded recipes need a valid signature before any hook runs | fail-closed |
| Archive extraction | Zip-slip containment, setuid/world-write stripping, size cap | secure |
| Recipe name/version | Allow-list validated, no path traversal | secure |
| Schema | Recipe and plan schemas enforced, not best-effort | secure |
| Process privileges | Per-component user, capability allow-list, `no_new_privileges` | inherits the agent unless declared |

## Fail-closed, everywhere

The recurring rule: **when a security control cannot be applied, the operation
fails**. It never degrades to the insecure path with a warning.

- An artifact whose signature does not verify fails the apply — on the restart path
  too, not just the first install.
- A recipe file without a valid signature is refused *before* its install hook runs,
  because that hook is arbitrary shell.
- A privilege restriction that cannot be enforced refuses to start the component,
  rather than running it unconfined.

Warnings get ignored; failures get fixed.

## The one escape hatch

```bash
keystone --insecure-skip-verify          # or KEYSTONE_INSECURE_SKIP_VERIFY=true
```

Disables the mandatory artifact integrity policy. It logs, at startup, every time:

```
[agent] WARNING: artifact integrity verification is DISABLED
(--insecure-skip-verify); downloaded artifacts are NOT authenticated.
Do not use in production.
```

Use it for local development and demos. It does not disable recipe signature
checking on file-loaded recipes, and it does not open the HTTP API.

## Threat model in one paragraph

Keystone assumes the **artifact store and the network are untrusted**: anything
downloaded must prove its origin cryptographically. It assumes the **control plane
is authenticated** — by a token over HTTP, or by broker ACLs and TLS for NATS and
MQTT. It assumes the **local filesystem is trusted**: whoever can write
`runtime/state` or the recipe store already owns the device. And it assumes
**components are semi-trusted**: they can be confined, but Keystone is not a
sandbox — a determined workload with capabilities can still hurt the host.

## Known limitations

Honest list, tracked as follow-ups:

- **NATS/MQTT `planPath`**: the rejection is implemented for HTTP only. Those
  adapters are off by default and rely on broker ACLs.
- **Release signing**: published binaries are not signed yet (no cosign, no SBOM).
- **Container confinement**: `privileged` containers and host mounts are not gated
  by policy.
- **Child environment**: recipe-supplied env is not stripped of `LD_PRELOAD` /
  `LD_LIBRARY_PATH` for process workloads.
- **State snapshot integrity**: `runtime/state` is not integrity-protected.

Treat the device's local filesystem and your broker ACLs as part of the trust
boundary until these close.
