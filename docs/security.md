# Security Model

Keystone installs and runs software on edge devices, so it treats recipes and
artifacts as **code** and the control plane as a **privileged surface**. As of
v0.2.1 the agent is **secure by default and fails closed**: if it cannot
authenticate what it is about to run, it refuses to run it. A single escape
hatch, `--insecure-skip-verify`, relaxes verification for local development.

This document describes the trust model, every control, and how to operate the
agent securely.

> Scope note: v0.2.1 closed the six critical findings of the 2026 security
> audit. Some hardening is still open — see [Known limitations](#known-limitations).

---

## Threat model

The attacker we defend against can reach the device over the network and/or
influence the recipes, plans, and artifacts the agent processes. The worst
outcomes we prevent:

- **Remote code execution** via the control-plane API.
- **Running tampered or unauthenticated code** (artifacts or recipe lifecycle
  scripts).
- **Escaping the runtime directory** when writing recipes or extracting archives.
- **Denial of service** through unbounded request bodies or decompression bombs.

Two trust boundaries matter:

1. **The HTTP control plane** — authenticated by a bearer token (or restricted
   to loopback). Anything that arrives through an authenticated request is
   treated as operator-authorized.
2. **The cryptographic trust bundle** (`KEYSTONE_TRUST_BUNDLE`) — the root of
   trust for verifying signatures on recipes loaded from disk and on artifacts.

---

## Controls at a glance

| Surface | Control | Default |
|---------|---------|---------|
| HTTP API bind | Loopback (`127.0.0.1:8080`); non-loopback requires a token | secure |
| HTTP API auth | Bearer token, constant-time compare, `/healthz` exempt | on when token set |
| Plan submission | Content-only; remote `planPath` rejected | secure |
| Request size | `http.MaxBytesReader`, 413 on overflow (4 MiB) | secure |
| Slowloris | `ReadHeaderTimeout` / `ReadTimeout` / `IdleTimeout` | secure |
| Artifact integrity | Mandatory `sha256` + detached signature, apply **and** restart | secure (fail-closed) |
| Recipe integrity | File-loaded recipes require a detached signature before any hook | secure (fail-closed) |
| Archive extraction | Zip-slip containment, setuid/world-write mode stripping, size cap | secure |
| Recipe name/version | Allowlist validator, no path traversal | secure |
| Schema | Recipe/plan JSON Schema enforced (not best-effort) | secure |
| Dev escape | `--insecure-skip-verify` / `KEYSTONE_INSECURE_SKIP_VERIFY=true` | off |

---

## HTTP control plane

The REST API can apply plans and run lifecycle hooks, i.e. it can execute
arbitrary code as the agent's user. It is therefore locked down:

- **Bind:** defaults to `127.0.0.1:8080`. Binding any non-loopback address
  (e.g. `--http 0.0.0.0:8080`) **requires** a token; without one the agent
  refuses to start.
- **Token:** set `--api-token` or `KEYSTONE_API_TOKEN`. When set, every endpoint
  except `/healthz` requires `Authorization: Bearer <token>`, compared in
  constant time. `keystonectl` sends the token automatically from `--token` or
  `KEYSTONE_API_TOKEN`.
- **No `planPath`:** the API accepts plans as uploaded content only. The legacy
  `{"planPath": "..."}` form (which let a caller load an arbitrary server-side
  file) is rejected with `400`.

```bash
# Remote-reachable, authenticated:
export KEYSTONE_API_TOKEN="$(openssl rand -hex 32)"
./keystone --http 0.0.0.0:8080
KEYSTONE_API_TOKEN="$KEYSTONE_API_TOKEN" ./keystonectl --addr http://host:8080 status
```

> HTTP transport-level TLS is not yet built in; terminate TLS at a reverse proxy
> or use the token over a trusted link. NATS/MQTT support TLS natively (see
> [adapters.md](adapters.md)).

---

## Artifact integrity

Every artifact a recipe declares is verified on **both** plan apply and
component restart, through one shared code path. By default (fail-closed):

- `sha256` must be present and match the downloaded bytes.
- `sig_uri` (a detached signature) must be present and verify.
- `KEYSTONE_TRUST_BUNDLE` must be configured; the signing cert must chain to it.

Missing any of these aborts the install. The certificate comes from the recipe
(`cert_uri`), `KEYSTONE_LEAF_CERT`, or a sibling file.

```toml
[[artifacts]]
uri      = "https://example.com/hello-linux-amd64"
sha256   = "9f86d0..."
sig_uri  = "https://example.com/hello-linux-amd64.sig"
cert_uri = "https://example.com/leaf.pem"   # optional; else KEYSTONE_LEAF_CERT
unpack   = false
```

---

## Recipe integrity

A recipe's `install` / `run` / `shutdown` scripts are arbitrary shell — the
recipe **is** code. Recipes loaded from a **filesystem path** must therefore be
signed before any hook runs:

- A sibling `<recipe>.sig` (detached SHA-256 signature) must exist.
- A cert is taken from `<recipe>.crt` or `KEYSTONE_LEAF_CERT`, chaining to the
  trust bundle.

Recipes resolved from the **recipe store** (`name:version`, i.e. pushed via the
authenticated API with `keystonectl upload-recipe`) are **not** re-verified:
the API authentication is their trust boundary.

See [Signing walkthrough](#signing-walkthrough) below.

---

## Archive extraction

When an artifact has `unpack = true`, extraction is hardened:

- **Zip-slip:** entries whose path escapes the target directory are rejected.
- **Permissions:** archive-supplied mode bits are masked (`& 0o755`), stripping
  setuid/setgid/sticky and group/other write — a malicious archive cannot drop a
  setuid binary.
- **Decompression bombs:** total uncompressed size is capped
  (`KEYSTONE_MAX_EXTRACT_BYTES`, default 2 GiB).

---

## Input handling

- **Recipe `name`/`version`** are validated against an allowlist
  (`[A-Za-z0-9._+-]`, no `..`, no separators) before they become filesystem
  paths, preventing traversal out of `runtime/`.
- **Recipe and plan schemas** are enforced (the JSON Schema result is no longer
  discarded); malformed documents are rejected at load.
- **Request bodies** are capped (`KEYSTONE_MAX_REQUEST_BYTES`, default 4 MiB).

---

## The `--insecure-skip-verify` escape hatch

For local development and the bundled demo, `--insecure-skip-verify` (or
`KEYSTONE_INSECURE_SKIP_VERIFY=true`) disables the mandatory artifact and recipe
verification. The agent logs a loud warning at startup. **Never use it in
production** — it turns the fail-closed posture back into fail-open.

---

## Signing walkthrough

A dev helper generates a throwaway CA and signs files for you:

```bash
# 1. Generate a dev CA + leaf and sign the example recipe.
scripts/dev-sign.sh configs/examples/com.keystone.server.recipe.toml

# 2. Point the agent at the trust material and run it SECURELY (no skip flag).
export KEYSTONE_TRUST_BUNDLE=configs/trust/ca.pem
export KEYSTONE_LEAF_CERT=configs/trust/leaf.pem
task build
./keystone --http 127.0.0.1:8080

# 3. Apply the signed plan.
./keystonectl apply configs/examples/plan.toml
```

For production, replace the dev CA with your real signing CA and keep private
keys off the device. The keys generated by `dev-sign.sh` are gitignored and must
never reach production. For the raw OpenSSL commands see
[configs/trust/README.md](../configs/trust/README.md).

---

## Configuration reference

### Flags (agent)

| Flag | Default | Purpose |
|------|---------|---------|
| `--http` | `127.0.0.1:8080` | HTTP listen address (`""` disables). Non-loopback needs a token. |
| `--api-token` | _empty_ | Bearer token for the API (or `KEYSTONE_API_TOKEN`). |
| `--insecure-skip-verify` | `false` | Disable mandatory artifact/recipe verification (dev only). |

### Environment variables (security-relevant)

| Variable | Purpose |
|----------|---------|
| `KEYSTONE_API_TOKEN` | Bearer token for the HTTP API; required for non-loopback bind. |
| `KEYSTONE_INSECURE_SKIP_VERIFY` | `true` disables artifact/recipe verification (dev only). |
| `KEYSTONE_TRUST_BUNDLE` | CA bundle (PEM) that signatures must chain to. |
| `KEYSTONE_LEAF_CERT` | Default signing certificate when not provided per-recipe/artifact. |
| `KEYSTONE_MAX_REQUEST_BYTES` | Max HTTP request body (default 4 MiB). |
| `KEYSTONE_MAX_EXTRACT_BYTES` | Max uncompressed size per archive (default 2 GiB). |

The full environment table is in the [README](../README.md#environment-variables).

---

## Deploying with systemd

The reference unit (`configs/systemd/keystone.service`) runs the agent as a
dedicated non-root user with a hardened sandbox (`NoNewPrivileges`,
`ProtectSystem=strict`, `ProtectHome`, `PrivateTmp`). It loads
`/etc/keystone/keystone.env` for `KEYSTONE_API_TOKEN` and trust paths so the
remotely-reachable API is authenticated. Review and adjust before production.

---

## Known limitations

Honest list of what is **not** yet covered (tracked as follow-ups):

- **NATS/MQTT `planPath`:** the `planPath` rejection is implemented for HTTP
  only. NATS/MQTT still accept it; they are disabled by default and rely on
  broker ACLs.
- **Release signing:** released binaries are not yet signed (no cosign/SBOM).
- **Workload privilege dropping:** spawned processes inherit the agent's
  privileges; there is no per-component uid/gid drop yet, and `privileged`
  containers / host mounts are not gated by policy.
- **Child environment:** recipe-supplied env is not stripped of `LD_PRELOAD` /
  `LD_LIBRARY_PATH` for process workloads.
- **State snapshot integrity:** `runtime/state` is not integrity-protected.

Treat the device's local filesystem and the broker's ACLs as part of your trust
boundary until these are closed.
