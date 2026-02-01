# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Keystone is a lightweight edge orchestration agent written in Go. It manages local components (native processes by default, containers optional), executes deployments atomically with rollback, and converges devices to a desired state. The philosophy is "processes first, containers when needed."

## Build and Development Commands

```bash
# Build all binaries (keystone, keystonectl, keystoneserver)
task build

# Run the agent (HTTP only, default)
task run                    # or: go run ./cmd/keystone --http :8080

# Run with NATS adapter enabled
./keystone --http :8080 --nats-url nats://localhost:4222 --nats-device-id edge-001

# Development with live reload (requires air)
task dev

# Run tests
task test                   # or: go test -v ./...

# Format and vet
task fmt
task vet

# Setup git hooks (pre-commit runs go fmt)
task hooks
```

### Agent Flags

```
--http string              HTTP listen address (default ":8080", empty to disable)
--nats-url string          NATS server URL (empty to disable)
--nats-device-id string    Device ID for NATS subjects (default: hostname)
--nats-tls-cert string     Path to NATS client TLS certificate
--nats-tls-key string      Path to NATS client TLS key
--nats-tls-ca string       Path to NATS CA certificate
--nats-tls-verify          Verify NATS server TLS certificate (default: true)
--nats-creds string        Path to NATS credentials file (.creds)
--nats-nkey string         Path to NATS NKey seed file
--nats-token string        NATS authentication token
--nats-user string         NATS username
--nats-pass string         NATS password
--nats-state-interval      Interval for state events (default 10s)
--nats-health-interval     Interval for health events (default 30s)
--nats-jetstream           Enable JetStream persistent job queue
--nats-js-stream string    JetStream stream name (default "KEYSTONE_JOBS")
--nats-js-workers int      Number of job processor workers (default 1)
--demo                     Run built-in demo stack
--version                  Print version and exit
```

### Running a Single Test

```bash
go test -v -run TestName ./internal/package/...
```

### CLI Usage

```bash
./keystonectl status              # Show plan status
./keystonectl components          # List components
./keystonectl apply <plan.toml>   # Apply a deployment plan
./keystonectl apply-dry <plan>    # Dry-run showing execution order
./keystonectl stop <name>         # Stop a component
./keystonectl restart <name>      # Restart a component
./keystonectl graph               # Show dependency graph
```

## Architecture

### Core Components

- **Agent** (`internal/agent/`): Top-level runtime that coordinates all subsystems, manages state persistence, and handles plan application. Implements the `adapter.CommandHandler` interface.

- **Adapter System** (`internal/adapter/`): Pluggable transport layer architecture:
  - `adapter.CommandHandler`: Interface that decouples transport from business logic
  - `adapter.Adapter`: Interface for control plane adapters (Start/Stop/Name)
  - `adapter.Registry`: Manages multiple adapters lifecycle
  - `adapter/http/`: HTTP REST adapter implementation
  - `adapter/nats/`: NATS messaging adapter for async control plane communication

- **Supervisor** (`internal/supervisor/`): Manages component lifecycle with a simple FSM (none → installing → starting → running → stopping → stopped/failed). Uses DAG-based topological ordering for parallel layer startup.

- **ProcessRunner** (`internal/runner/`): Starts native processes with signal handling (process groups), log streaming, health probes (HTTP/TCP/cmd), and restart policies (never/on-failure/always).

- **Recipe** (`internal/recipe/`): TOML-based component descriptors containing metadata, artifacts, lifecycle hooks (install/run/shutdown), health checks, and resource limits.

- **Artifact Manager** (`internal/artifact/`): Downloads artifacts with retries, verifies SHA-256 checksums, supports signature verification, unpacks archives, and garbage collects old versions.

- **Deploy** (`internal/deploy/`): Loads TOML deployment plans that specify which components to run and their recipes.

### Data Flow

1. A **plan** (TOML) lists components and their recipe paths
2. The agent loads each **recipe** and resolves dependencies
3. **Supervisor** builds a DAG and starts components layer by layer
4. **ProcessRunner** spawns processes, streams logs, runs health probes
5. **State** is persisted to `runtime/state/` for recovery after restart

### Key Directories

- `cmd/keystone/`: Main agent binary
- `cmd/keystonectl/`: CLI for interacting with the agent API
- `cmd/keystoneserver/`: Simple HTTP server for serving test artifacts
- `internal/`: All internal packages (agent, supervisor, runner, recipe, artifact, etc.)
- `runtime/`: Working directories (components, artifacts, state, recipes)
- `configs/examples/`: Example plans and recipes

### Recipe Format (TOML)

Recipes define component metadata, artifacts to download, lifecycle hooks, health checks, and dependencies:

```toml
[metadata]
name = "com.example.hello"
version = "1.0.0"

[[artifacts]]
uri = "https://example.com/hello.tar.gz"
sha256 = "..."
unpack = true

[lifecycle.run.exec]
command = "./hello"
args = ["--interval", "30s"]

[lifecycle.run.health]
check = "http://127.0.0.1:8080/healthz"
interval = "10s"
```

### Environment Variables

- `KEYSTONE_ARTIFACT_CACHE_LIMIT_BYTES`: Max artifact cache size (default: 2GiB)
- `KEYSTONE_ARTIFACT_DOWNLOAD_TIMEOUT`: Download timeout (default: 30m)
- `KEYSTONE_TRUST_BUNDLE`: Path to CA trust bundle for signature verification
- `KEYSTONE_LEAF_CERT`: Default certificate for signature verification
- `KEYSTONE_GITHUB_TOKEN`: Token for GitHub artifact downloads
- `KEYSTONE_INSTALL_TIMEOUT`: Timeout for install phase (default: 2m)

### Artifact Download System

The artifact manager (`internal/artifact/`) provides robust downloads for edge deployments:

- **Resume**: Automatic resume via HTTP Range headers (partial files saved as `.partial`)
- **Retry**: Exponential backoff with jitter (default: 10 attempts, 1s-30s)
- **Timeouts**: Configurable connect (30s), read (60s), overall (30m)
- **Progress**: Real-time logging with speed and percentage
- **Atomic**: Download to temp file, rename on success
- **Verification**: SHA-256 check after download

Key functions in `internal/artifact/download.go`:
- `DownloadWithResume()`: Main download function with full retry/resume
- `DownloadConfig`: Configurable timeouts, retries, and resume settings
- `DefaultDownloadConfig()`: Sensible defaults for edge networks

### NATS Adapter

The NATS adapter enables asynchronous control plane communication. Subjects follow the pattern `keystone.{deviceId}.cmd.*` for commands and `keystone.{deviceId}.events.*` for events.

**Commands (Request/Reply):**
- `keystone.{deviceId}.cmd.apply` - Apply a deployment plan
- `keystone.{deviceId}.cmd.stop` - Stop all components
- `keystone.{deviceId}.cmd.status` - Get plan status
- `keystone.{deviceId}.cmd.restart` - Restart a component
- `keystone.{deviceId}.cmd.health` - Get health status

**Events (Publish):**
- `keystone.{deviceId}.events.state` - Component state changes
- `keystone.{deviceId}.events.health` - Periodic health updates

**Example client usage:**
```go
nc, _ := nats.Connect("nats://localhost:4222")
req := nats.ApplyRequest{PlanPath: "plan.toml"}
data, _ := json.Marshal(req)
resp, _ := nc.Request("keystone.device-001.cmd.apply", data, 30*time.Second)
```

### JetStream Job Queue

When JetStream is enabled (`--nats-jetstream`), the adapter creates a durable job queue for persistent job processing:

- **Stream**: `KEYSTONE_JOBS` (configurable) with WorkQueue retention
- **Subject**: `keystone.{deviceId}.jobs` for incoming jobs
- **Results**: `keystone.{deviceId}.jobs.results` for job results
- **Job types**: `apply`, `stop`, `restart`, `stop-comp`, `add-recipe`, `delete-recipe`

**Job structure:**
```json
{
  "id": "job-123",
  "type": "apply",
  "deviceId": "edge-001",
  "payload": {"planPath": "/path/to/plan.toml", "dry": false},
  "createdAt": "2024-01-01T00:00:00Z",
  "priority": 0,
  "metadata": {"source": "control-plane"}
}
```

**Features:**
- At-least-once delivery with configurable max retries (default: 5)
- Acknowledgment-based processing (NAK with delay for failures)
- Configurable worker count for parallel job processing
- Jobs survive network outages and agent restarts

### NATS Security

The NATS adapter supports multiple authentication methods (in priority order):

1. **NKey** (`--nats-nkey`): Ed25519 key-based auth, most secure
2. **Credentials** (`--nats-creds`): JWT + NKey combined file
3. **Token** (`--nats-token`): Simple token auth
4. **Username/Password** (`--nats-user`, `--nats-pass`): Basic auth

**TLS Configuration:**
- `--nats-tls-cert` / `--nats-tls-key`: Client certificate for mTLS
- `--nats-tls-ca`: CA certificate for server verification
- `--nats-tls-verify`: Enable/disable server cert verification (default: true)

**Example with mTLS + NKey:**
```bash
./keystone --http :8080 \
  --nats-url tls://control-plane:4222 \
  --nats-device-id edge-001 \
  --nats-tls-ca /etc/keystone/certs/ca.crt \
  --nats-tls-cert /etc/keystone/certs/client.crt \
  --nats-tls-key /etc/keystone/certs/client.key \
  --nats-nkey /etc/keystone/nats/device.nkey
```

## API Testing

A Bruno collection is provided in `bruno/` for testing the agent API. The agent exposes:

- `GET /healthz`: Health check
- `GET /metrics`: Prometheus metrics
- `GET /v1/components`: List components
- `GET /v1/plan/status`: Plan status
- `POST /v1/plan/apply`: Apply a plan
- `POST /v1/components/{name}:stop`: Stop component
- `POST /v1/components/{name}:restart`: Restart component
