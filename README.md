<p align="center">
  <img src="keystone_logo.png" alt="Keystone Logo" width="200"/>
</p>

# Keystone — Lightweight Edge Orchestration in Go

Keystone is a minimal, robust, and secure edge orchestration agent written in Go. It manages local components (native processes by default; containers optional later), executes deployments atomically with rollback, and keeps devices converging to a desired state — even with flaky networks.

Why Keystone? Because edge fleets need something that is lightweight, predictable, and operable without dragging a full container stack everywhere. Keystone embraces “processes first, containers when needed,” with a clean, pluggable design inspired by Greengrass.

## Highlights

- Lightweight: idle CPU ~0%, small RAM baseline
- Solid: atomic deployments, checkpoints, rollback
- Secure: least privilege, mTLS-ready, checksums/signatures
- Portable: Linux x86/ARM, no mandatory Docker/CRI
- Operable: structured logs, Prometheus metrics, health endpoints, persistence

### Keystone vs. AWS Greengrass

| Feature          | AWS Greengrass        | **Keystone**            |
| :--------------- | :-------------------- | :---------------------- |
| **Runtime**      | Java (JVM) / C (Lite) | **Go (Native)**         |
| **RAM Baseline** | ~100MB+               | **< 40MB**              |
| **Complexity**   | High (Cloud-first)    | **Low (Lean & Simple)** |
| **Setup**        | Heavy Bootstrap       | **Single Binary**       |

## Quick Look: How it Works

Keystone uses simple **TOML recipes** to define components and **deployment plans** to keep your devices in sync.

### 1. Define a Recipe

Describe how to install and run your process in `com.example.hello.recipe.toml`:

```toml
[metadata]
name = "com.example.hello"
version = "1.0.0"

[[artifacts]]
uri = "https://example.com/artifacts/hello.tar.gz"
sha256 = "..."
unpack = true

[lifecycle.run.exec]
command = "./hello"
args = ["--interval", "30s"]
```

### 2. Apply a Plan

List the components you want to run in `plan.toml`:

```toml
[[components]]
name = "hello"
recipe = "com.example.hello.recipe.toml"
```

Deploy it with a single command:

```bash
./keystonectl apply plan.toml
```

### 3. Check Status

See everything running at a glance:

```bash
./keystonectl status
```

## Project Status

MVP in progress. This repo contains the initial agent skeleton, a simple health endpoint, a minimal supervisor core, and example recipe files. Expect rapid iteration as we build the ProcessRunner, artifact manager, and deployment engine.

## Quick Start

Build and run the local agent:

```bash
go run ./cmd/keystone --http :8080
```

Probe the health endpoint:

```bash
curl -s localhost:8080/healthz | jq
```

You should see a JSON response with status and uptime.

Run the built-in demo stack (db -> cache -> api):

```bash
go run ./cmd/keystone --demo
```

Scrape metrics (Prometheus format):

```bash
curl -s localhost:8080/metrics | head
```

## Roadmap (MVP → v1.0)

- [x] **Phase 0**: Agent skeleton, config base, /healthz, persistent state snapshotting
- [x] **Phase 1**: Supervisor + ProcessRunner, lifecycle hooks, health checks (HTTP/TCP/Shell)
- [x] **Phase 2**: DAG-based deployments, layer-wise rollback, Prometheus metrics
- [ ] **Phase 3**: Security hardening (mTLS, artifact signatures) — *Signatures implemented*
- [ ] **Phase 4**: Optional ContainerRunner (containerd/nerdctl)
- [ ] **Phase 5**: Self-update and canary rings

See [KeyStone.md](KeyStone.md) for the architecture proposal and delivery plan.

### Implemented Features (Current Status)

- **Supervisor**: DAG execution model with parallel layer startup and FSM lifecycle.
- **ProcessRunner**: Full management of native processes with log streaming and health probes.
- **Deployment Engine**: TOML-based plans and recipes with environment variable substitution.
- **Artifact Manager**: Secure download, SHA-256 verification, detatched signatures, and GC.
- **Security**: Trust bundle loading and ECDSA/RSA signature verification for artifacts.
- **Observability**: Prometheus endpoint with state, readiness, and per-process metrics.
- **Persistence**: Automatic state snapshotting and recovery in `runtime/state`.

## Apply a Deployment Plan (End-to-End Preview)

Use a minimal TOML plan to run real processes via the ProcessRunner.

Example plan (see `configs/examples/plan.toml`):

```toml
[[components]]
name = "hello"
recipe = "configs/examples/com.example.hello.recipe.toml"
```

Apply it:

```bash
go run ./cmd/keystone --apply configs/examples/plan.toml --http :8080
```

Notes:

- The example recipe points to a placeholder artifact URL; replace with a real binary and SHA256 for a working flow.
- Install script runs in the component work dir and can mark binaries executable.
- ProcessRunner applies basic `RLIMIT_NOFILE`; cgroups integration is a safe no-op placeholder for now.

## Quick Usage

- Start agent with plan:
  - `go run ./cmd/keystone --apply configs/examples/plan.toml --http :8080`
- Health and discovery:
  - `curl -s localhost:8080/healthz | jq`
  - `curl -s localhost:8080/v1/components | jq`
  - `curl -s localhost:8080/v1/plan/status | jq`
- Stop all components:
  - `curl -X POST localhost:8080/v1/plan/stop -i`
- Stop or restart a single component:
  - `curl -X POST localhost:8080/v1/components/hello:stop -i`
  - `curl -X POST localhost:8080/v1/components/hello:restart -i`
- Metrics (Prometheus):
  - `curl -s localhost:8080/metrics | head`

### Signed Artifacts

- Provide a trust bundle (PEM) via `KEYSTONE_TRUST_BUNDLE` and a leaf certificate via `KEYSTONE_LEAF_CERT`, or include `cert_uri` in the recipe.
- Add `sig_uri` to each artifact entry in the recipe to enable signature verification.
- Signature format: detached signature over SHA-256 of the artifact, produced with OpenSSL (`openssl dgst -sha256 -sign ...`).
- See `configs/trust/README.md` for a quick, dev-friendly CA and signing walkthrough.

### Artifact Download Headers (TOML)

- Configure per-artifact HTTP headers directly in the recipe under `[[artifacts]].headers`.
- For GitHub artifacts (`github.com` or `api.github.com`), set `github_token` (at the same level as `uri`) to inject `Authorization: Bearer <token>` when no `Authorization` header is provided.

Example snippet inside a recipe:

```toml
[[artifacts]]
uri = "https://api.github.com/repos/org/repo/actions/artifacts/123/zip"
sha256 = "sha256:<...>"
unpack = true
github_token = ""
[artifacts.headers]
Accept = "application/vnd.github+json"   # para endpoint de Actions /artifacts/{id}/zip (302 hacia S3)
```

### keystonectl (CLI)

Build and use the local CLI for convenience:

```bash
go build -o keystonectl ./cmd/keystonectl
./keystonectl status
./keystonectl components
./keystonectl stop-plan
./keystonectl stop hello
./keystonectl restart hello
./keystonectl graph
./keystonectl restart-dry hello
./keystonectl apply-dry configs/examples/plan.toml
```

### keystoneserver

A simple HTTP server to serve local artifacts for testing:

```bash
go run ./cmd/keystoneserver --root ./artifacts --addr :9000
```

- Accessible at `http://localhost:9000/<path>`
- Includes a `/healthz` endpoint.

Graph and dry-run from API directly:

```bash
curl -s localhost:8080/v1/plan/graph | jq
curl -s -X POST localhost:8080/v1/components/hello:restart?dry=true | jq
curl -s -X POST localhost:8080/v1/plan/apply -H 'Content-Type: application/json' \
  -d '{"planPath":"configs/examples/plan.toml","dry":true}'
```

## Configuration

### Environment Variables

Keystone supports loading environment variables from a `.env` file in the current working directory.

| Variable                              | Description                                                            |
| ------------------------------------- | ---------------------------------------------------------------------- |
| `KEYSTONE_ARTIFACT_CACHE_LIMIT_BYTES` | Max size of `runtime/artifacts` (default: 2GiB).                       |
| `KEYSTONE_TRUST_BUNDLE`               | Path to CA trust bundle (PEM) for signature verification.              |
| `KEYSTONE_LEAF_CERT`                  | Default certificate (PEM) for signature verification if not in recipe. |
| `KEYSTONE_GITHUB_TOKEN`               | Default token for GitHub artifact downloads (if not in recipe).        |

## Git hooks

Run once after cloning to enable the repo’s versioned hooks:

```bash
make hooks   # or: ./scripts/setup-git-hooks.sh
```

This sets `core.hooksPath` to `.githooks`, where the `pre-commit` hook runs `go fmt ./...` and stages formatting changes automatically.

## Concepts (Preview)

- Recipe (TOML): describes a component (artifacts, lifecycle, security, resources)
- Supervisor: enforces lifecycle and restart policy per component
- Deployment plan: desired set (components+versions+overrides) resolved as a DAG
- Artifact manager: downloads, verifies, caches, and garbage collects

## Systemd Unit (example)

See `configs/systemd/keystone.service` for a hardened example unit file.

## Community

We welcome contributors who care about reliability at the edge.

- Issues: bug reports, design discussions, small enhancements
- PRs: focused, well-tested changes; prefer conventional commits
- Security: please report privately first when appropriate

## License

📃 Apache-2.0
