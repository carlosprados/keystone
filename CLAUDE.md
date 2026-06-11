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

# Single test
go test -v -run TestName ./internal/package/...

# Run with NATS
./keystone --http :8080 --nats-url nats://localhost:4222 --nats-device-id edge-001

# Run built-in demo (db -> cache -> api dependency chain)
go run ./cmd/keystone --demo
```

Releases via GoReleaser (`.goreleaser.yaml`), triggered by version tags. Builds Linux amd64/arm64/armv7, CGO disabled.

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

## Key Documentation

- `README.md` — Features, quick start, all CLI flags, environment variables
- `docs/security.md` — Security model: secure-by-default posture, auth, signing, `--insecure-skip-verify`, config reference
- `docs/adapters.md` — Adapter comparison, HTTP auth, NATS/MQTT configuration details
- `docs/containers.md` — Container recipe syntax and examples
- `docs/containerrunner-design.md` — Containerd integration design decisions
- `KeyStone.md` — Original architecture proposal and delivery plan
- `configs/examples/` — Example plans and recipes
- `configs/trust/README.md` — CA setup and recipe/artifact signing walkthrough
- `scripts/dev-sign.sh` — Dev helper: generate a throwaway CA and sign recipes/artifacts
