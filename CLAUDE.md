# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Keystone is a lightweight edge orchestration agent written in Go (`github.com/carlosprados/keystone`). It manages local components (native processes by default, containers optional), executes deployments atomically with rollback, and converges devices to a desired state. Philosophy: "processes first, containers when needed."

Go 1.24+. Key deps: containerd v2, nats.go, paho.mqtt.golang, prometheus client, go-toml/v2.

## Build and Development Commands

Build tool is [Task](https://taskfile.dev/) (see `Taskfile.yml`). No Makefile.

```bash
task build          # Build all binaries (keystone, keystonectl, keystoneserver) with version ldflags
task run            # Run agent with HTTP on :8080
task dev            # Live reload via air (requires github.com/air-verse/air)
task test           # go test -v ./...
task fmt            # go fmt ./...
task vet            # go vet ./...
task hooks          # Setup git pre-commit hook (go fmt)

# Cut a release: bump the documented version, review, then tag
task release:prepare RELEASE=v0.3.1   # bumps site/hugo.toml, opens the PR
task release:tag RELEASE=v0.3.1       # after the merge: tags origin/main

# Single test
go test -v -run TestName ./internal/package/...

# Run with NATS
./keystone --http :8080 --nats-url nats://localhost:4222 --nats-device-id edge-001

# Run built-in demo (db -> cache -> api dependency chain)
go run ./cmd/keystone --demo
```

Releases via GoReleaser (`.goreleaser.yaml`), triggered by version tags. Builds Linux amd64/arm64/armv7, CGO disabled. The documented version in `site/hugo.toml` must be bumped and published **before** the tag exists — a tag cannot republish the docs site, and `release.yml` fails a tag that disagrees with it. Use the two tasks above rather than tagging by hand.

## Architecture

### Three Binaries

- **keystone** (`cmd/keystone/`): Main agent runtime — wires adapters, runs plans, manages components
- **keystonectl** (`cmd/keystonectl/`): CLI client — talks to agent via HTTP REST API
- **keystoneserver** (`cmd/keystoneserver/`): Simple HTTP file server for serving test artifacts

### Core Interfaces

All control flow is driven by three key interfaces:

1. **`adapter.Adapter`** (`internal/adapter/adapter.go`): Pluggable transport — `Name()`, `Start(ctx)`, `Stop(ctx)`. Implementations: HTTP, NATS, MQTT.

2. **`adapter.CommandHandler`** (`internal/adapter/adapter.go`): Business logic contract — `ApplyPlan`, `StopPlan`, `GetComponents`, `RestartComponent`, etc. Implemented by `Agent`.

3. **`runner.Runner`** (`internal/runner/runner.go`): Component execution — `Start(ctx, opts)`, `Stop(ctx, h, timeout)`, `RunManaged(...)`. Implementations: `ProcessRunner` (native processes), `ContainerRunner` (containerd + CLI fallback).

### Wiring (main.go)

```
Agent.New(httpAddr)
  → adapter.NewRegistry()
    ├→ httpadapter.New(cfg, agent)    [always, unless --http ""]
    ├→ natsadapter.New(cfg, agent)    [if --nats-url set]
    └→ mqttadapter.New(cfg, agent)    [if --mqtt-broker set]
  → Registry.StartAll(ctx)
  → <-signal → Registry.StopAll(shutdownCtx, 10s)
```

### Package Map

| Package | Role |
|---------|------|
| `internal/agent` | Top-level runtime, implements `CommandHandler`, coordinates all subsystems |
| `internal/adapter` | Transport abstraction + `Registry` for multi-adapter lifecycle |
| `internal/adapter/http` | REST API adapter (default :8080) |
| `internal/adapter/nats` | NATS adapter + JetStream job queue |
| `internal/adapter/mqtt` | MQTT adapter (Paho client, QoS, LWT) |
| `internal/supervisor` | Component lifecycle FSM (none→installing→starting→running→stopping→stopped/failed), DAG-based topological ordering for parallel startup |
| `internal/runner` | Runner interface + `ProcessRunner` (process groups, signals, health probes, restart policies) + `ContainerRunner` (containerd v2 client, CLI fallback to docker/nerdctl/podman) |
| `internal/recipe` | TOML recipe parsing (metadata, artifacts, lifecycle hooks, health, deps) |
| `internal/deploy` | TOML deployment plan parsing and execution |
| `internal/artifact` | Download with resume/retry/backoff, SHA-256 verification, signature verification, cache GC |
| `internal/store` | In-memory component state store + recipe store |
| `internal/security` | ECDSA/RSA detached signature verification |
| `internal/metrics` | Prometheus metrics + per-process resource metrics |
| `internal/state` | Deployment state persistence to `runtime/state/` |
| `internal/config` | .env file loading |
| `internal/validate` | Recipe/plan TOML validation |
| `internal/version` | Build version injection via ldflags |

### Data Flow

1. **Plan** (TOML) lists components and their recipe paths
2. Agent loads each **recipe**, resolves dependencies into a DAG
3. **Supervisor** starts components layer by layer (parallel within each layer)
4. **Runner** (Process or Container) spawns the workload, streams logs, runs health probes
5. **State** is persisted to `runtime/state/` for crash recovery

### API Endpoints

The HTTP adapter exposes: `/healthz`, `/metrics`, `/v1/components`, `/v1/plan/status`, `/v1/plan/apply`, `/v1/plan/stop`, `/v1/plan/graph`, `/v1/components/{name}:stop`, `/v1/components/{name}:restart`. A Bruno collection in `bruno/` provides ready-made requests.

### Configuration

All agent flags are discoverable via `./keystone --help`. Environment variables are documented in `README.md` (section "Environment Variables"). The agent loads `.env` from the working directory.

## Branches

**`main` is the only long-lived branch.** Work happens on short-lived
`keystone/<feature-name>` branches that reach `main` by pull request and are
deleted on merge. There is no `develop`, no release branch, no integration
branch.

That single branch is load-bearing in three places, which is why nothing else
gets to be long-lived:

- **CI** gates every PR into `main` and re-runs on the merge (`ci.yml`).
  Rulesets enforce it: no direct pushes, no history rewriting.
- **The docs site** publishes from `main` (`pages.yml`), and the site's
  "edit this page" link points there. Publishing branch and branch of record are
  the same by construction.
- **Releases** tag a commit on `main`, and release tags are immutable.

A second long-lived branch existed once. It received no pull requests, published
nothing, was tagged never, and had no protection — so it drifted 22 commits
behind while the docs' edit links still pointed at it. Every reader who clicked
"edit this page" got a stale copy, and nothing looked broken. If a branch is not
one of the three things above, it is short-lived.

## Documentation is part of every change

**Doctrine, not a suggestion: no change ships without a documentation review.**

Before opening a PR, check each of these and state in the PR body which ones you
touched and which you deliberately did not:

| If you changed | Review |
|---|---|
| An HTTP route, or any type a handler encodes | `internal/adapter/http/routes.go` is the single source of truth; run `task openapi` and commit the regenerated `site/content/reference/api/openapi.yaml`. CI fails if it drifts |
| A recipe or plan field | `site/content/concepts/recipes.md`, `site/content/reference/schemas.md`, and the examples that use it |
| Component state, reuse or supervision behaviour | `site/content/concepts/component-state.md` and `reconcile-and-reuse.md` — those pages are a contract, not a description |
| A flag or environment variable | Run `task cli-docs` and commit the regenerated `site/content/reference/cli.md` and `keystonectl.md`; CI fails if either drifts. Then `env.md` and the README |
| A `keystonectl` command, its help or its examples | Same: `site/content/reference/keystonectl.md` is generated from the Cobra tree in `internal/cli`. Never edit that page by hand |
| Security posture, defaults, or confinement | `docs/security.md` **and** `site/content/security/` |
| An adapter's topics, subjects or payloads | `site/content/control-planes/` and the matching example page |
| Anything a user would follow step by step | The examples chapter — the walkthroughs must still work verbatim |

Rules that follow from this:

- **A behaviour change with no documentation change is suspect.** Either the
  behaviour was undocumented (fix that) or the docs are now wrong.
- **Prefer generated over written.** The OpenAPI document and the CLI reference are
  derived from the code. When you can generate a fact, generate it.
- **Verify the claim, do not repeat it.** Several pages have been wrong because a
  response shape or a subcommand was assumed. Read the struct, run the command.
- **Say what is not covered.** A limitation written down is a known gap; an
  undocumented one is a surprise in production.

## Key Documentation

- `README.md` — Features, quick start, all CLI flags, environment variables
- `docs/security.md` — Security model: secure-by-default posture, auth, signing, `--insecure-skip-verify`, config reference
- `docs/component-state.md` — Component states, liveness guarantees of `/v1/components`, reuse rules on re-apply
- `docs/adapters.md` — Adapter comparison, HTTP auth, NATS/MQTT configuration details
- `docs/containers.md` — Container recipe syntax and examples
- `docs/containerrunner-design.md` — Containerd integration design decisions
- `KeyStone.md` — Original architecture proposal and delivery plan
- `configs/examples/` — Example plans and recipes
- `configs/trust/README.md` — CA setup and recipe/artifact signing walkthrough
- `scripts/dev-sign.sh` — Dev helper: generate a throwaway CA and sign recipes/artifacts
- `scripts/release.sh` — Release helper behind `task release:prepare` / `task release:tag`; why the order matters is in its header
