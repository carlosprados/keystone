<p align="center">
  <img src="keystone_logo.png" alt="Keystone Logo" width="200"/>
</p>

# Keystone — Lightweight Edge Orchestration in Go

Keystone is a minimal, robust, and secure edge orchestration agent written in Go. It manages local components (native processes and containers), executes deployments atomically with rollback, and keeps devices converging to a desired state — even with flaky networks.

Why Keystone? Because edge fleets need something that is lightweight, predictable, and operable without dragging a full container stack everywhere. Keystone embraces "processes first, containers when needed," with a clean, pluggable design inspired by Greengrass. When you need containers, Keystone supports containerd natively or falls back to Docker/nerdctl/podman CLI.

## Highlights

- **Lightweight**: idle CPU ~0%, small RAM baseline (<40MB)
- **Solid**: atomic deployments, checkpoints, rollback, exponential backoff
- **Secure**: mTLS, artifact signatures (ECDSA/RSA), checksums
- **Portable**: Linux x86/ARM, single binary, no mandatory Docker/CRI
- **Connected**: HTTP REST, NATS (+ JetStream), MQTT adapters
- **Operable**: structured logs, Prometheus metrics, health endpoints, persistence

### Keystone vs. AWS Greengrass

| Feature          | AWS Greengrass        | **Keystone**              |
| :--------------- | :-------------------- | :------------------------ |
| **Runtime**      | Java (JVM) / C (Lite) | **Go (Native)**           |
| **RAM Baseline** | ~100MB+               | **< 40MB**                |
| **Complexity**   | High (Cloud-first)    | **Low (Lean & Simple)**   |
| **Setup**        | Heavy Bootstrap       | **Single Binary**         |
| **Control Plane**| AWS IoT Core only     | **HTTP, NATS, MQTT**      |
| **Offline Mode** | Limited               | **Full (JetStream jobs)** |

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

### 1b. Or Define a Container Recipe

Run containerized workloads when needed (uses containerd or docker/nerdctl/podman):

```toml
[metadata]
name = "com.example.nginx"
version = "1.0.0"

[lifecycle.run]
type = "container"
restart_policy = "always"

[lifecycle.run.container]
image = "docker.io/library/nginx:alpine"
pull_policy = "if-not-present"
network_mode = "bridge"

[[lifecycle.run.container.mounts]]
source = "/data/nginx/html"
target = "/usr/share/nginx/html"
read_only = true

[[lifecycle.run.container.ports]]
host_port = 8080
container_port = 80

[lifecycle.run.container.resources]
memory_mb = 256
cpu_shares = 512

[lifecycle.run.health]
check = "http://localhost:8080/"
interval = "10s"
```

For complete container documentation, see **[docs/containers.md](docs/containers.md)**.

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

**MVP Complete.** Keystone is ready for production evaluation. The agent includes a complete supervisor, process runner, artifact manager, deployment engine, and multiple control plane adapters (HTTP, NATS, MQTT). See the roadmap below for upcoming features.

## Quick Start

```bash
go run ./cmd/keystone --http :8080
```

For development with live reload (requires [air](https://github.com/air-verse/air)):

```bash
task dev
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
- [x] **Phase 3**: Security hardening — mTLS adapters, artifact signatures (ECDSA/RSA)
- [x] **Phase 4**: Control plane adapters — HTTP REST, NATS (+ JetStream), MQTT
- [x] **Phase 5**: Robustness — download resume, exponential backoff, graceful shutdown
- [x] **Phase 6**: ContainerRunner — containerd client, CLI fallback (docker/nerdctl/podman)
- [ ] **Phase 7**: Self-update and canary rings

See [KeyStone.md](KeyStone.md) for the architecture proposal and delivery plan.

### Implemented Features

| Category | Features |
|----------|----------|
| **Supervisor** | DAG execution, parallel layer startup, FSM lifecycle, dependency ordering |
| **ProcessRunner** | Process management, log streaming, health probes (HTTP/TCP/cmd), restart policies, exponential backoff |
| **ContainerRunner** | containerd client, CLI fallback (docker/nerdctl/podman), image pull, mounts, ports, resource limits |
| **Deployment Engine** | TOML plans and recipes, environment variable substitution, dry-run mode |
| **Artifact Manager** | Secure download with resume, SHA-256 verification, detached signatures, GC, cache limits |
| **Security** | Trust bundles (PEM), ECDSA/RSA signature verification, mTLS support |
| **Observability** | Prometheus metrics, structured logging, health endpoints, per-process metrics |
| **Persistence** | Automatic state snapshotting, recovery on restart, atomic writes |
| **Control Plane** | HTTP REST API, NATS adapter (+ JetStream jobs), MQTT adapter (QoS, LWT) |
| **Robustness** | Download resume (HTTP Range), exponential backoff with jitter, context propagation, graceful shutdown |

## Apply a Deployment Plan (End-to-End Preview)

Use a minimal TOML plan to run real processes via the ProcessRunner.

Example plan (see `configs/examples/plan.toml`):

```toml
[[components]]
name = "keystone-server"
recipe = "configs/examples/com.keystone.server.recipe.toml"
```

Apply it:

```bash
# 1. Start the agent (now remote-first)
task build
./keystone --http :8080

# 2. Apply the plan remotely using the CLI
./keystonectl apply configs/examples/plan.toml
```

Notes:

- The example recipe uses the built-in `keystoneserver` binary.
- Artifact management and detached signatures are supported but optional for this simple example.
- ProcessRunner applies basic `RLIMIT_NOFILE`; cgroups integration is a safe no-op placeholder for now.

## Quick Usage

- Start agent:
  - `./keystone --http :8080`
- Apply plan:
  - `./keystonectl apply configs/examples/plan.toml`
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

### API Testing with Bruno

We provide a [Bruno](https://usebruno.com/) collection for testing the agent API.

1. Install Bruno.
2. Open the app and select **Open Collection**.
3. Select the `bruno/` folder in this repository.
4. Use the **local** environment to set the `base_url`.

Graph and dry-run from API directly:

```bash
curl -s localhost:8080/v1/plan/graph | jq
curl -s -X POST localhost:8080/v1/components/hello:restart?dry=true | jq
curl -s -X POST localhost:8080/v1/plan/apply -H 'Content-Type: application/json' \
  -d '{"planPath":"configs/examples/plan.toml","dry":true}'
```

## Releases

Keystone uses [GoReleaser](https://goreleaser.com/) for automated builds and releases. To trigger a new release:

1. Tag the commit: `git tag -a v0.1.0 -m "Release v0.1.0"`
2. Push the tag: `git push origin v0.1.0`

The GitHub Action will automatically build the binaries for multiple architectures (`amd64`, `arm64`, `armv7`) and create a GitHub Release with the artifacts.

## Configuration

### Environment Variables

Keystone supports loading environment variables from a `.env` file in the current working directory.

| Variable                              | Description                                                            |
| ------------------------------------- | ---------------------------------------------------------------------- |
| `KEYSTONE_ARTIFACT_CACHE_LIMIT_BYTES` | Max size of `runtime/artifacts` (default: 2GiB).                       |
| `KEYSTONE_ARTIFACT_DOWNLOAD_TIMEOUT`  | Artifact download timeout (default: 30m). Supports "5m", "1h", etc.    |
| `KEYSTONE_TRUST_BUNDLE`               | Path to CA trust bundle (PEM) for signature verification.              |
| `KEYSTONE_LEAF_CERT`                  | Default certificate (PEM) for signature verification if not in recipe. |
| `KEYSTONE_GITHUB_TOKEN`               | Default token for GitHub artifact downloads (if not in recipe).        |
| `KEYSTONE_DEVICE_ID`                  | Device ID for NATS/MQTT topics (default: hostname).                    |
| `KEYSTONE_INSTALL_TIMEOUT`            | Install phase timeout (default: 2m). Supports duration strings.        |
| `KEYSTONE_CONTAINERD_SOCKET`          | containerd socket path (default: `/run/containerd/containerd.sock`).   |
| `KEYSTONE_CONTAINERD_NAMESPACE`       | containerd namespace for containers (default: `keystone`).             |
| `KEYSTONE_CONTAINER_SNAPSHOTTER`      | Snapshotter for container images (default: `overlayfs`).               |
| `KEYSTONE_CONTAINER_REGISTRY`         | Default container registry (default: `docker.io`).                     |
| `KEYSTONE_CNI_CONF_DIR`               | CNI config directory for bridge mode (default: `/etc/cni/net.d`).      |
| `KEYSTONE_CNI_PLUGIN_DIRS`            | CNI plugin dirs (default: `/opt/cni/bin:/usr/lib/cni`).                |
| `KEYSTONE_CNI_NETNS_DIR`              | Netns dir for bridge mode (default: `/var/run/netns`).                 |

### Robust Artifact Downloads

Keystone features a robust artifact download system designed for unreliable edge networks:

| Feature | Description |
|---------|-------------|
| **Resume Support** | Automatic resume via HTTP Range headers if download is interrupted |
| **Exponential Backoff** | Retries with jitter to avoid thundering herd (1s-30s, 10 attempts) |
| **Progress Tracking** | Real-time download progress with speed and ETA |
| **Timeout Control** | Configurable connect, read, and overall timeouts |
| **Atomic Operations** | Downloads to `.partial` file, renamed on completion |
| **SHA-256 Verification** | Post-download integrity check |
| **Error Classification** | Distinguishes fatal (4xx) from retryable (5xx, network) errors |
| **Rate Limit Handling** | Respects `Retry-After` headers for 429 responses |

## Control Plane Adapters

Keystone uses a pluggable adapter architecture for control plane communication. Multiple adapters can run simultaneously.

| Adapter | Protocol | Use Case | Default |
|---------|----------|----------|---------|
| **HTTP** | REST API | Local management, debugging, Prometheus | Enabled (`:8080`) |
| **NATS** | Pub/Sub | Cloud-scale fleet management | Disabled |
| **MQTT** | IoT messaging | AWS IoT Core, edge gateways | Disabled |

For complete adapter documentation, see **[docs/adapters.md](docs/adapters.md)**.

### HTTP Adapter (Default)

The HTTP adapter exposes a REST API for local management:

```bash
# Default: enabled on port 8080
./keystone --http :8080

# Disable HTTP (use only messaging adapters)
./keystone --http "" --nats-url nats://server:4222 --nats-device-id edge-001
```

**Key Endpoints:**
| Endpoint | Description |
|----------|-------------|
| `GET /healthz` | Health check |
| `GET /metrics` | Prometheus metrics |
| `GET /v1/components` | List components |
| `POST /v1/plan/apply` | Apply deployment plan |
| `POST /v1/components/{name}:restart` | Restart component |

### NATS Adapter

Enable NATS for asynchronous fleet management with optional JetStream persistence:

```bash
# Basic NATS
./keystone --http :8080 \
  --nats-url nats://control-plane:4222 \
  --nats-device-id edge-001

# With mTLS and JetStream
./keystone --http :8080 \
  --nats-url nats://control-plane:4222 \
  --nats-device-id edge-001 \
  --nats-tls-cert /etc/keystone/certs/client.crt \
  --nats-tls-key /etc/keystone/certs/client.key \
  --nats-tls-ca /etc/keystone/certs/ca.crt \
  --nats-jetstream
```

**Key Features:**
- mTLS, NKey, Token, User/Pass authentication
- JetStream for durable job queues (survives disconnections)
- Subjects: `keystone.{deviceId}.cmd.*`, `keystone.{deviceId}.events.*`

**Authentication Priority:** NKey > Credentials > Token > User/Pass

### MQTT Adapter

Enable MQTT for IoT-friendly communication with brokers like Mosquitto, EMQX, or AWS IoT Core:

```bash
# Basic MQTT
./keystone --http :8080 \
  --mqtt-broker tcp://broker:1883 \
  --mqtt-device-id edge-001

# With TLS and auth
./keystone --http :8080 \
  --mqtt-broker ssl://broker:8883 \
  --mqtt-device-id edge-001 \
  --mqtt-tls-ca /etc/keystone/certs/ca.crt \
  --mqtt-user agent --mqtt-pass secret
```

**Key Features:**
- mTLS, User/Pass authentication
- Configurable QoS (0, 1, 2)
- Last Will and Testament for online/offline detection
- Topics: `keystone/{deviceId}/cmd/*`, `keystone/{deviceId}/resp/*`, `keystone/{deviceId}/events/*`

### Running Multiple Adapters

```bash
# HTTP + NATS + MQTT simultaneously
./keystone --http :8080 \
  --nats-url nats://nats.internal:4222 --nats-device-id edge-001 \
  --mqtt-broker tcp://mqtt.internal:1883 --mqtt-device-id edge-001
```

### All CLI Flags

<details>
<summary>HTTP Adapter Flags</summary>

| Flag | Default | Description |
|------|---------|-------------|
| `--http` | `:8080` | HTTP listen address (empty to disable) |

</details>

<details>
<summary>NATS Adapter Flags</summary>

| Flag | Default | Description |
|------|---------|-------------|
| `--nats-url` | (empty) | NATS server URL (empty to disable) |
| `--nats-device-id` | hostname | Device ID for subjects |
| `--nats-tls-cert` | (empty) | Client TLS certificate path |
| `--nats-tls-key` | (empty) | Client TLS key path |
| `--nats-tls-ca` | (empty) | CA certificate path |
| `--nats-tls-verify` | `true` | Verify server certificate |
| `--nats-creds` | (empty) | Credentials file path (.creds) |
| `--nats-nkey` | (empty) | NKey seed file path |
| `--nats-token` | (empty) | Authentication token |
| `--nats-user` | (empty) | Username |
| `--nats-pass` | (empty) | Password |
| `--nats-state-interval` | `10s` | State event interval (0 to disable) |
| `--nats-health-interval` | `30s` | Health event interval (0 to disable) |
| `--nats-jetstream` | `false` | Enable JetStream |
| `--nats-js-stream` | `KEYSTONE_JOBS` | JetStream stream name |
| `--nats-js-workers` | `1` | Job processor workers |

</details>

<details>
<summary>MQTT Adapter Flags</summary>

| Flag | Default | Description |
|------|---------|-------------|
| `--mqtt-broker` | (empty) | MQTT broker URL (empty to disable) |
| `--mqtt-device-id` | hostname | Device ID for topics |
| `--mqtt-client-id` | `keystone-{device-id}` | MQTT client ID |
| `--mqtt-tls-cert` | (empty) | Client TLS certificate path |
| `--mqtt-tls-key` | (empty) | Client TLS key path |
| `--mqtt-tls-ca` | (empty) | CA certificate path |
| `--mqtt-tls-verify` | `true` | Verify server certificate |
| `--mqtt-user` | (empty) | Username |
| `--mqtt-pass` | (empty) | Password |
| `--mqtt-qos` | `1` | QoS level (0, 1, 2) |
| `--mqtt-state-interval` | `10s` | State event interval (0 to disable) |
| `--mqtt-health-interval` | `30s` | Health event interval (0 to disable) |

</details>

## Git hooks

Run once after cloning to enable the repo’s versioned hooks:

```bash
task hooks   # or: ./scripts/setup-git-hooks.sh
```

This sets `core.hooksPath` to `.githooks`, where the `pre-commit` hook runs `go fmt ./...` and stages formatting changes automatically.

## Core Concepts

| Concept | Description |
|---------|-------------|
| **Recipe** | TOML file describing a component: artifacts, lifecycle hooks, health checks, resources |
| **Deployment Plan** | TOML file listing components to run, resolved as a DAG with dependencies |
| **Supervisor** | Enforces lifecycle (install → start → running → stop) and restart policies |
| **Runner** | Executes components: ProcessRunner (native) or ContainerRunner (containerd/CLI) |
| **Artifact Manager** | Downloads, verifies (SHA-256 + signatures), caches, and garbage collects artifacts |
| **Adapter** | Pluggable control plane interface (HTTP, NATS, MQTT) for remote management |

## Systemd Unit (example)

See `configs/systemd/keystone.service` for a hardened example unit file.

## Community

We welcome contributors who care about reliability at the edge.

- Issues: bug reports, design discussions, small enhancements
- PRs: focused, well-tested changes; prefer conventional commits
- Security: please report privately first when appropriate

## License

📃 Apache-2.0
