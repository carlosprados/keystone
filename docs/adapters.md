# Control Plane Adapters

Keystone uses a pluggable adapter architecture for control plane communication. Multiple adapters can run simultaneously, allowing you to expose the agent through different protocols based on your infrastructure needs.

## Overview

| Adapter | Protocol | Use Case | Enabled By Default |
|---------|----------|----------|-------------------|
| **HTTP** | REST API | Local management, debugging, Prometheus scraping | Yes |
| **MQTT** | IoT messaging | IoT platforms, AWS IoT Core, edge gateways | No |

## Adapter Comparison

| Feature | HTTP | MQTT |
|---------|------|------|
| **Transport** | HTTP/1.1 | TCP/WebSocket |
| **Pattern** | Request/Response | Pub/Sub |
| **TLS Support** | Terminate at proxy | Yes (mTLS) |
| **Authentication** | Bearer token (required off-loopback) | User/Pass, Certificates, [enrolment](../site/content/security/enrolment.md) |
| **Persistence** | N/A | Broker-dependent |
| **Offline Queuing** | No | Broker-dependent |
| **Event Streaming** | No | Yes |
| **Best For** | Local/debug | Fleets, constrained devices, outbound-only links |

A NATS adapter existed until v0.13.0 and was removed: it had no users we know of, and every
change to the command protocol had to be made twice.

---

## HTTP Adapter

The HTTP adapter exposes a REST API for local management and integration with monitoring systems.

### Configuration

```bash
# Default: enabled on loopback only
./keystone --http 127.0.0.1:8080

# Disable HTTP adapter
./keystone --http ""

# Reachable off-host: a token is REQUIRED, or the agent refuses to start
export KEYSTONE_API_TOKEN="$(openssl rand -hex 32)"
./keystone --http 0.0.0.0:8080
```

### Authentication

The API can apply plans and run lifecycle hooks (arbitrary code), so it is a
privileged surface:

- Binds `127.0.0.1:8080` by default. Binding any non-loopback address requires
  a token (`--api-token` / `KEYSTONE_API_TOKEN`); without one the agent refuses
  to start.
- When a token is set, every endpoint except `/healthz` requires
  `Authorization: Bearer <token>` (constant-time compare). `keystonectl` sends
  it from `--token` / `KEYSTONE_API_TOKEN`.
- `POST /v1/plan/apply` accepts plan **content** only; the legacy `planPath`
  field is rejected (`400`).
- No built-in transport TLS yet — terminate TLS at a reverse proxy. See
  [security.md](security.md).

### CLI Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--http` | `127.0.0.1:8080` | HTTP listen address (empty to disable) |
| `--api-token` | _empty_ | Bearer token (or `KEYSTONE_API_TOKEN`); required for non-loopback bind |

### API Endpoints

#### Health & Monitoring

| Endpoint | Method | Description |
|----------|--------|-------------|
| `GET /healthz` | GET | Health check (JSON) |
| `GET /metrics` | GET | Prometheus metrics |
| `GET /` | GET | Landing page |

#### Components

| Endpoint | Method | Description |
|----------|--------|-------------|
| `GET /v1/components` | GET | List all managed components |
| `POST /v1/components/{name}:stop` | POST | Stop a specific component |
| `POST /v1/components/{name}:restart` | POST | Restart a component (and dependents) |
| `POST /v1/components/{name}:restart?dry=true` | POST | Dry-run: show restart order |

#### Deployment Plans

| Endpoint | Method | Description |
|----------|--------|-------------|
| `GET /v1/plan/status` | GET | Get current plan status |
| `GET /v1/plan/graph` | GET | Get dependency graph |
| `POST /v1/plan/apply` | POST | Apply a deployment plan |
| `POST /v1/plan/reconcile` | POST | Repair the plan in effect (restart dead components) |
| `POST /v1/plan/stop` | POST | Stop all components |

**Apply Plan Request:**
```json
{
  "planPath": "/path/to/plan.toml",
  "dry": false
}
```

Or with inline content:
```json
{
  "content": "[[components]]\nname = \"hello\"\nrecipe = \"hello.recipe.toml\"",
  "dry": false
}
```

#### Recipes

| Endpoint | Method | Description |
|----------|--------|-------------|
| `GET /v1/recipes` | GET | List stored recipes |
| `POST /v1/recipes` | POST | Add a new recipe (body: TOML content) |
| `POST /v1/recipes?force=true` | POST | Add/overwrite a recipe |
| `DELETE /v1/recipes/{name}/{version}` | DELETE | Delete a specific recipe |

### Example Usage

```bash
# Health check
curl -s localhost:8080/healthz | jq

# List components
curl -s localhost:8080/v1/components | jq

# Apply a plan (upload content; planPath is no longer accepted)
curl -X POST localhost:8080/v1/plan/apply --data-binary @configs/examples/plan.toml
# With a token configured, add: -H "Authorization: Bearer $KEYSTONE_API_TOKEN"

# Restart a component
curl -X POST localhost:8080/v1/components/myapp:restart

# Dry-run restart (see what would happen)
curl -X POST localhost:8080/v1/components/myapp:restart?dry=true | jq

# Stop all
curl -X POST localhost:8080/v1/plan/stop

# Prometheus metrics
curl -s localhost:8080/metrics | grep keystone_
```

---

## MQTT Adapter

The MQTT adapter provides IoT-friendly communication, compatible with popular MQTT brokers like Mosquitto, EMQX, HiveMQ, and cloud services like AWS IoT Core.

### Configuration

```bash
# Basic MQTT connection
./keystone --http :8080 \
  --mqtt-broker tcp://broker:1883 \
  --mqtt-tenant acme \
  --mqtt-device-id edge-001

# With TLS
./keystone --http :8080 \
  --mqtt-broker ssl://broker:8883 \
  --mqtt-tenant acme \
  --mqtt-device-id edge-001 \
  --mqtt-tls-ca /etc/keystone/certs/ca.crt

# With mTLS
./keystone --http :8080 \
  --mqtt-broker ssl://broker:8883 \
  --mqtt-tenant acme \
  --mqtt-device-id edge-001 \
  --mqtt-tls-cert /etc/keystone/certs/client.crt \
  --mqtt-tls-key /etc/keystone/certs/client.key \
  --mqtt-tls-ca /etc/keystone/certs/ca.crt

# With username/password
./keystone --http :8080 \
  --mqtt-broker tcp://broker:1883 \
  --mqtt-tenant acme \
  --mqtt-device-id edge-001 \
  --mqtt-user agent \
  --mqtt-pass secret

# With custom QoS and client ID
./keystone --http :8080 \
  --mqtt-broker tcp://broker:1883 \
  --mqtt-tenant acme \
  --mqtt-device-id edge-001 \
  --mqtt-client-id my-custom-client-id \
  --mqtt-qos 2

# Disable event publishing
./keystone --http :8080 \
  --mqtt-broker tcp://broker:1883 \
  --mqtt-tenant acme \
  --mqtt-device-id edge-001 \
  --mqtt-state-interval 0 \
  --mqtt-health-interval 0
```

### CLI Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--mqtt-broker` | (empty) | MQTT broker URL (empty to disable) |
| `--mqtt-tenant` | (required) | Tenant for topics, a lowercase DNS label |
| `--mqtt-device-id` | hostname | Device ID for topics |
| `--mqtt-client-id` | `keystone-{tenant}-{device-id}` | MQTT client ID |
| `--mqtt-tls-cert` | (empty) | Path to client TLS certificate |
| `--mqtt-tls-key` | (empty) | Path to client TLS key |
| `--mqtt-tls-ca` | (empty) | Path to CA certificate |
| `--mqtt-tls-verify` | `true` | Verify server certificate |
| `--mqtt-user` | (empty) | Username for auth |
| `--mqtt-pass` | (empty) | Password for auth |
| `--mqtt-qos` | `1` | QoS level for commands/responses (0, 1, 2) |
| `--mqtt-state-interval` | `10s` | State event publish interval (0 to disable) |
| `--mqtt-health-interval` | `30s` | Health event publish interval (0 to disable) |

Environment variable equivalents are supported (flags take precedence):
`KEYSTONE_MQTT_BROKER`, `KEYSTONE_MQTT_TENANT`, `KEYSTONE_MQTT_DEVICE_ID`, `KEYSTONE_MQTT_CLIENT_ID`,
`KEYSTONE_MQTT_TLS_CERT`, `KEYSTONE_MQTT_TLS_KEY`, `KEYSTONE_MQTT_TLS_CA`,
`KEYSTONE_MQTT_TLS_VERIFY`, `KEYSTONE_MQTT_USER`, `KEYSTONE_MQTT_PASS`,
`KEYSTONE_MQTT_QOS`, `KEYSTONE_MQTT_STATE_INTERVAL`, `KEYSTONE_MQTT_HEALTH_INTERVAL`.

### Topic Patterns

All topics use the pattern `keystone/{tenant}/{deviceId}/*`. The tenant is required
(`--mqtt-tenant`, or an enrolment):

#### Command Topics (Agent Subscribes)

| Topic | Description |
|-------|-------------|
| `keystone/{tenant}/{deviceId}/cmd/apply` | Apply a deployment plan |
| `keystone/{tenant}/{deviceId}/cmd/stop` | Stop all components |
| `keystone/{tenant}/{deviceId}/cmd/status` | Get plan status |
| `keystone/{tenant}/{deviceId}/cmd/components` | Get components list |
| `keystone/{tenant}/{deviceId}/cmd/graph` | Get dependency graph |
| `keystone/{tenant}/{deviceId}/cmd/restart` | Restart a component |
| `keystone/{tenant}/{deviceId}/cmd/stop-comp` | Stop a specific component |
| `keystone/{tenant}/{deviceId}/cmd/health` | Get health status |
| `keystone/{tenant}/{deviceId}/cmd/recipes` | List recipes |
| `keystone/{tenant}/{deviceId}/cmd/add-recipe` | Add a recipe |

#### Response Topics (Agent Publishes)

| Topic | Description |
|-------|-------------|
| `keystone/{tenant}/{deviceId}/resp/apply` | Apply response |
| `keystone/{tenant}/{deviceId}/resp/stop` | Stop response |
| `keystone/{tenant}/{deviceId}/resp/status` | Status response |
| `keystone/{tenant}/{deviceId}/resp/components` | Components response |
| `keystone/{tenant}/{deviceId}/resp/graph` | Graph response |
| `keystone/{tenant}/{deviceId}/resp/restart` | Restart response |
| `keystone/{tenant}/{deviceId}/resp/stop-comp` | Stop component response |
| `keystone/{tenant}/{deviceId}/resp/health` | Health response |
| `keystone/{tenant}/{deviceId}/resp/recipes` | Recipes response |
| `keystone/{tenant}/{deviceId}/resp/add-recipe` | Add recipe response |

#### Event Topics (Agent Publishes)

| Topic | Description |
|-------|-------------|
| `keystone/{tenant}/{deviceId}/events/state` | Component state updates |
| `keystone/{tenant}/{deviceId}/events/health` | Health status updates |
| `keystone/{tenant}/{deviceId}/status` | LWT: "online" / "offline" |

### Message Formats

All requests include an optional `correlationId` for matching responses:

**Request (to cmd topic):**
```json
{
  "correlationId": "req-12345",
  "component": "myapp",
  "wait": "health",
  "timeout": "60s"
}
```

**Response (from resp topic):**
```json
{
  "correlationId": "req-12345",
  "success": true,
  "data": {
    "component": "myapp",
    "pid": 1234,
    "dependents": {}
  }
}
```

**State Event:**
```json
{
  "timestamp": "2024-01-15T10:30:00Z",
  "deviceId": "edge-001",
  "planStatus": "running",
  "planPath": "/etc/keystone/plan.toml",
  "components": [
    {"name": "myapp", "state": "running", "pid": 1234}
  ]
}
```

### QoS Levels

| QoS | Guarantee | Use Case |
|-----|-----------|----------|
| 0 | At most once (fire & forget) | Telemetry, non-critical events |
| 1 | At least once | Commands, state updates (default) |
| 2 | Exactly once | Critical operations |

### Last Will and Testament (LWT)

The MQTT adapter automatically configures an LWT message:
- **Topic:** `keystone/{tenant}/{deviceId}/status`
- **Online Payload:** `"online"` (published on connect)
- **Offline Payload:** `"offline"` (published by broker on disconnect)
- **Retained:** Yes (subscribers see current status immediately)

This allows monitoring systems to detect agent connectivity status in real-time.

### Security Features

| Feature | Description |
|---------|-------------|
| **mTLS** | Mutual TLS with client certificates |
| **User/Pass** | Username/password authentication |
| **TLS 1.2+** | Enforced minimum TLS version |
| **Auto-Reconnect** | Automatic reconnection with exponential backoff |
| **Clean Session** | Configurable session persistence |

---

## Running Multiple Adapters

Adapters can run simultaneously. A typical production setup might use:

```bash
# HTTP for metrics + MQTT for IoT platform
./keystone --http :8080 \
  --mqtt-broker ssl://iot.example.com:8883 \
  --mqtt-tenant acme \
  --mqtt-device-id edge-001 \
  --mqtt-tls-ca /etc/keystone/certs/iot-ca.crt
```

## Environment Variables

Adapter settings configurable via environment variables (flags take precedence):

| Variable | Description |
|----------|-------------|
| `KEYSTONE_DEVICE_ID` | Default device ID for MQTT (if not specified via flags). |
| `KEYSTONE_MQTT_BROKER` | MQTT broker URL (used if `--mqtt-broker` is not passed). |
| `KEYSTONE_MQTT_TENANT` | MQTT tenant; required unless an enrolment provides it. |
| `KEYSTONE_MQTT_DEVICE_ID` | MQTT-specific device ID (used if `--mqtt-device-id` is not passed). |
| `KEYSTONE_MQTT_CLIENT_ID` | MQTT client ID (used if `--mqtt-client-id` is not passed). |
| `KEYSTONE_MQTT_TLS_CERT` | Path to MQTT client TLS certificate. |
| `KEYSTONE_MQTT_TLS_KEY` | Path to MQTT client TLS private key. |
| `KEYSTONE_MQTT_TLS_CA` | Path to MQTT CA certificate. |
| `KEYSTONE_MQTT_TLS_VERIFY` | Verify MQTT broker certificate (`true`/`false`). |
| `KEYSTONE_MQTT_USER` | MQTT username. |
| `KEYSTONE_MQTT_PASS` | MQTT password. |
| `KEYSTONE_MQTT_QOS` | MQTT QoS level for commands/responses (0, 1, 2). |
| `KEYSTONE_MQTT_STATE_INTERVAL` | MQTT state event interval (`10s`, `30s`, `0` to disable). |
| `KEYSTONE_MQTT_HEALTH_INTERVAL` | MQTT health event interval (`30s`, `0` to disable). |

## Troubleshooting

### Common Issues

**MQTT: TLS handshake failed**
- Ensure CA certificate matches the broker's certificate
- Check that the broker URL uses `ssl://` for TLS connections

**Events not publishing**
- Verify the publish interval is not set to 0
- Check adapter logs for connection status

### Debug Logging

Adapter activities are logged with prefixes:
- `[http]` - HTTP adapter events
- `[mqtt]` - MQTT adapter events

Example:
```
[mqtt] connected to tcp://broker:1883 as keystone-acme-edge-001
[mqtt] subscribed to keystone/acme/edge-001/cmd/apply
```
