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
- Operable: structured logs, Prometheus metrics, health endpoints

## Project Status

MVP in progress. This repo contains the initial agent skeleton, a simple health endpoint, a minimal supervisor core, and example recipe files. Expect rapid iteration as we build the ProcessRunner, artifact manager, and deployment engine.

## Quick Start

Build and run the local agent:

```
go run ./cmd/keystone --http :8080
```

Probe the health endpoint:

```
curl -s localhost:8080/healthz | jq
```

You should see a JSON response with status and uptime.

Run the built-in demo stack (db -> cache -> api):

```
go run ./cmd/keystone --demo
```

Scrape metrics (Prometheus format):

```
curl -s localhost:8080/metrics | head
```

## Roadmap (MVP → v1.0)

- Phase 0: Agent skeleton, config base, /healthz, disk layout
- Phase 1: Supervisor + ProcessRunner, lifecycle hooks, health checks
- Phase 2: Deployments + rollback, checkpoints, Prometheus metrics
- Phase 3: Security and offline operation (mTLS, signatures)
- Phase 4: Optional ContainerRunner (containerd/nerdctl)
- Phase 5: Self-update and canary rings

See KeyStone.md for the architecture proposal and delivery plan.

Implemented preview pieces in this repo:

- Basic supervisor and DAG execution model
- In-memory component store and `/v1/components`
- `/metrics` (Prometheus) with simple component state gauge
- Recipe loader (TOML) and artifact manager (download, verify, unpack)

## Apply a Deployment Plan (End-to-End Preview)

Use a minimal TOML plan to run real processes via the ProcessRunner.

Example plan (see `configs/examples/plan.toml`):

```
[[components]]
name = "hello"
recipe = "configs/examples/com.example.hello.recipe.toml"
```

Apply it:

```
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

### keystonectl (CLI)

Build and use the local CLI for convenience:

```
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

Graph and dry-run from API directly:

```
curl -s localhost:8080/v1/plan/graph | jq
curl -s -X POST localhost:8080/v1/components/hello:restart?dry=true | jq
curl -s -X POST localhost:8080/v1/plan/apply -H 'Content-Type: application/json' \
  -d '{"planPath":"configs/examples/plan.toml","dry":true}'
```

## Git hooks

Run once after cloning to enable the repo’s versioned hooks:

```
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
