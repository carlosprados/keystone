# Control Plane Adapters

Keystone uses a pluggable adapter architecture for control plane communication. Multiple adapters can run simultaneously, allowing you to expose the agent through different protocols based on your infrastructure needs.

## Overview

| Adapter | Protocol | Use Case | Enabled By Default |
|---------|----------|----------|-------------------|
| **HTTP** | REST API | Local management, debugging, Prometheus scraping | Yes |
| **NATS** | Pub/Sub messaging | Cloud-scale fleet management, JetStream persistence | No |
| **MQTT** | IoT messaging | IoT platforms, AWS IoT Core, edge gateways | No |

## Adapter Comparison

| Feature | HTTP | NATS | MQTT |
|---------|------|------|------|
| **Transport** | HTTP/1.1 | TCP/WebSocket | TCP/WebSocket |
| **Pattern** | Request/Response | Pub/Sub + Request/Reply | Pub/Sub |
| **TLS Support** | Planned | Yes (mTLS) | Yes (mTLS) |
| **Authentication** | None (use reverse proxy) | NKey, Creds, Token, User/Pass | User/Pass, Certificates |
| **Persistence** | N/A | JetStream | Broker-dependent |
| **Offline Queuing** | No | Yes (JetStream) | Broker-dependent |
| **Event Streaming** | No | Yes | Yes |
| **Best For** | Local/debug | Large fleets, cloud | IoT, constrained devices |

---

## HTTP Adapter

The HTTP adapter exposes a REST API for local management and integration with monitoring systems.

### Configuration

```bash
# Default: enabled on port 8080
./keystone --http :8080

# Disable HTTP adapter
./keystone --http ""

# Custom port
./keystone --http :9090
```

### CLI Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--http` | `:8080` | HTTP listen address (empty to disable) |

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

# Apply a plan
curl -X POST localhost:8080/v1/plan/apply \
  -H 'Content-Type: application/json' \
  -d '{"planPath": "configs/examples/plan.toml"}'

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

## NATS Adapter

The NATS adapter enables asynchronous control plane communication through NATS messaging. It's ideal for large-scale fleet management with support for JetStream persistent job queues.

### Configuration

```bash
# Basic NATS connection
./keystone --http :8080 \
  --nats-url nats://control-plane:4222 \
  --nats-device-id edge-001

# With mTLS
./keystone --http :8080 \
  --nats-url nats://control-plane:4222 \
  --nats-device-id edge-001 \
  --nats-tls-cert /etc/keystone/certs/client.crt \
  --nats-tls-key /etc/keystone/certs/client.key \
  --nats-tls-ca /etc/keystone/certs/ca.crt

# With NKey authentication (recommended for production)
./keystone --http :8080 \
  --nats-url nats://control-plane:4222 \
  --nats-device-id edge-001 \
  --nats-nkey /etc/keystone/nats/device.nkey

# With credentials file (JWT + NKey)
./keystone --nats-url nats://control-plane:4222 \
  --nats-device-id edge-001 \
  --nats-creds /etc/keystone/nats/device.creds

# With token authentication
./keystone --nats-url nats://control-plane:4222 \
  --nats-device-id edge-001 \
  --nats-token mytoken

# With username/password
./keystone --nats-url nats://control-plane:4222 \
  --nats-device-id edge-001 \
  --nats-user agent --nats-pass secret

# With JetStream enabled
./keystone --http :8080 \
  --nats-url nats://control-plane:4222 \
  --nats-device-id edge-001 \
  --nats-jetstream \
  --nats-js-stream MYFLEET_JOBS \
  --nats-js-workers 2
```

### CLI Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--nats-url` | (empty) | NATS server URL (empty to disable) |
| `--nats-device-id` | hostname | Device ID for NATS subjects |
| `--nats-tls-cert` | (empty) | Path to client TLS certificate |
| `--nats-tls-key` | (empty) | Path to client TLS key |
| `--nats-tls-ca` | (empty) | Path to CA certificate |
| `--nats-tls-verify` | `true` | Verify server certificate |
| `--nats-creds` | (empty) | Path to credentials file (.creds) |
| `--nats-nkey` | (empty) | Path to NKey seed file |
| `--nats-token` | (empty) | Authentication token |
| `--nats-user` | (empty) | Username for auth |
| `--nats-pass` | (empty) | Password for auth |
| `--nats-state-interval` | `10s` | State event publish interval (0 to disable) |
| `--nats-health-interval` | `30s` | Health event publish interval (0 to disable) |
| `--nats-jetstream` | `false` | Enable JetStream job queue |
| `--nats-js-stream` | `KEYSTONE_JOBS` | JetStream stream name |
| `--nats-js-workers` | `1` | Number of job processor workers |

**Authentication Priority:** NKey > Credentials > Token > Username/Password

### Subject Patterns

All subjects use the pattern `keystone.{deviceId}.*`:

#### Command Subjects (Request/Reply)

| Subject | Description |
|---------|-------------|
| `keystone.{deviceId}.cmd.apply` | Apply a deployment plan |
| `keystone.{deviceId}.cmd.stop` | Stop all components |
| `keystone.{deviceId}.cmd.status` | Get plan status |
| `keystone.{deviceId}.cmd.graph` | Get dependency graph |
| `keystone.{deviceId}.cmd.restart` | Restart a component |
| `keystone.{deviceId}.cmd.stop-comp` | Stop a specific component |
| `keystone.{deviceId}.cmd.health` | Get health status |
| `keystone.{deviceId}.cmd.recipes` | List recipes |
| `keystone.{deviceId}.cmd.add-recipe` | Add a recipe |

#### Event Subjects (Publish)

| Subject | Description |
|---------|-------------|
| `keystone.{deviceId}.events.state` | Component state updates |
| `keystone.{deviceId}.events.health` | Health status updates |

### Message Formats

**Apply Request:**
```json
{
  "planPath": "/path/to/plan.toml",
  "dry": false
}
```

**Restart Request:**
```json
{
  "component": "myapp",
  "wait": "health",
  "timeout": "60s",
  "dry": false
}
```

**Response Format:**
```json
{
  "success": true,
  "data": { ... },
  "error": ""
}
```

### JetStream Job Queue

JetStream provides durable job processing for scenarios where reliability is critical:

- **Persistence**: Jobs survive network outages and agent restarts
- **At-least-once delivery**: Failed jobs automatically retry (default: 5 attempts)
- **Acknowledgment**: Jobs removed only after successful processing
- **Results stream**: Results published to `keystone.{deviceId}.jobs.results`

**Supported Job Types:** `apply`, `stop`, `restart`, `stop-comp`, `add-recipe`, `delete-recipe`

### Security Features

| Feature | Description |
|---------|-------------|
| **mTLS** | Mutual TLS with client certificates |
| **NKey** | Ed25519 key-based authentication |
| **Credentials** | JWT + NKey combined file |
| **Token** | Simple token authentication |
| **User/Pass** | Basic authentication |
| **TLS 1.2+** | Enforced minimum TLS version |

---

## MQTT Adapter

The MQTT adapter provides IoT-friendly communication, compatible with popular MQTT brokers like Mosquitto, EMQX, HiveMQ, and cloud services like AWS IoT Core.

### Configuration

```bash
# Basic MQTT connection
./keystone --http :8080 \
  --mqtt-broker tcp://broker:1883 \
  --mqtt-device-id edge-001

# With TLS
./keystone --http :8080 \
  --mqtt-broker ssl://broker:8883 \
  --mqtt-device-id edge-001 \
  --mqtt-tls-ca /etc/keystone/certs/ca.crt

# With mTLS
./keystone --http :8080 \
  --mqtt-broker ssl://broker:8883 \
  --mqtt-device-id edge-001 \
  --mqtt-tls-cert /etc/keystone/certs/client.crt \
  --mqtt-tls-key /etc/keystone/certs/client.key \
  --mqtt-tls-ca /etc/keystone/certs/ca.crt

# With username/password
./keystone --http :8080 \
  --mqtt-broker tcp://broker:1883 \
  --mqtt-device-id edge-001 \
  --mqtt-user agent \
  --mqtt-pass secret

# With custom QoS and client ID
./keystone --http :8080 \
  --mqtt-broker tcp://broker:1883 \
  --mqtt-device-id edge-001 \
  --mqtt-client-id my-custom-client-id \
  --mqtt-qos 2

# Disable event publishing
./keystone --http :8080 \
  --mqtt-broker tcp://broker:1883 \
  --mqtt-device-id edge-001 \
  --mqtt-state-interval 0 \
  --mqtt-health-interval 0
```

### CLI Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--mqtt-broker` | (empty) | MQTT broker URL (empty to disable) |
| `--mqtt-device-id` | hostname | Device ID for topics |
| `--mqtt-client-id` | `keystone-{device-id}` | MQTT client ID |
| `--mqtt-tls-cert` | (empty) | Path to client TLS certificate |
| `--mqtt-tls-key` | (empty) | Path to client TLS key |
| `--mqtt-tls-ca` | (empty) | Path to CA certificate |
| `--mqtt-tls-verify` | `true` | Verify server certificate |
| `--mqtt-user` | (empty) | Username for auth |
| `--mqtt-pass` | (empty) | Password for auth |
| `--mqtt-qos` | `1` | QoS level for commands/responses (0, 1, 2) |
| `--mqtt-state-interval` | `10s` | State event publish interval (0 to disable) |
| `--mqtt-health-interval` | `30s` | Health event publish interval (0 to disable) |

### Topic Patterns

All topics use the pattern `keystone/{deviceId}/*`:

#### Command Topics (Agent Subscribes)

| Topic | Description |
|-------|-------------|
| `keystone/{deviceId}/cmd/apply` | Apply a deployment plan |
| `keystone/{deviceId}/cmd/stop` | Stop all components |
| `keystone/{deviceId}/cmd/status` | Get plan status |
| `keystone/{deviceId}/cmd/graph` | Get dependency graph |
| `keystone/{deviceId}/cmd/restart` | Restart a component |
| `keystone/{deviceId}/cmd/stop-comp` | Stop a specific component |
| `keystone/{deviceId}/cmd/health` | Get health status |
| `keystone/{deviceId}/cmd/recipes` | List recipes |
| `keystone/{deviceId}/cmd/add-recipe` | Add a recipe |

#### Response Topics (Agent Publishes)

| Topic | Description |
|-------|-------------|
| `keystone/{deviceId}/resp/apply` | Apply response |
| `keystone/{deviceId}/resp/stop` | Stop response |
| `keystone/{deviceId}/resp/status` | Status response |
| `keystone/{deviceId}/resp/graph` | Graph response |
| `keystone/{deviceId}/resp/restart` | Restart response |
| `keystone/{deviceId}/resp/stop-comp` | Stop component response |
| `keystone/{deviceId}/resp/health` | Health response |
| `keystone/{deviceId}/resp/recipes` | Recipes response |
| `keystone/{deviceId}/resp/add-recipe` | Add recipe response |

#### Event Topics (Agent Publishes)

| Topic | Description |
|-------|-------------|
| `keystone/{deviceId}/events/state` | Component state updates |
| `keystone/{deviceId}/events/health` | Health status updates |
| `keystone/{deviceId}/status` | LWT: "online" / "offline" |

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
- **Topic:** `keystone/{deviceId}/status`
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
# HTTP for local debugging + NATS for fleet management
./keystone --http :8080 \
  --nats-url nats://control-plane:4222 \
  --nats-device-id edge-001 \
  --nats-jetstream

# HTTP for metrics + MQTT for IoT platform
./keystone --http :8080 \
  --mqtt-broker ssl://iot.example.com:8883 \
  --mqtt-device-id edge-001 \
  --mqtt-tls-ca /etc/keystone/certs/iot-ca.crt

# All three adapters
./keystone --http :8080 \
  --nats-url nats://nats.internal:4222 \
  --nats-device-id edge-001 \
  --mqtt-broker tcp://mqtt.internal:1883 \
  --mqtt-device-id edge-001
```

## Environment Variables

Some adapter settings can be configured via environment variables:

| Variable | Description |
|----------|-------------|
| `KEYSTONE_DEVICE_ID` | Default device ID for NATS/MQTT (if not specified via flags) |

## Troubleshooting

### Common Issues

**NATS: Connection refused**
- Check that the NATS server is running and accessible
- Verify firewall rules allow traffic on port 4222

**MQTT: TLS handshake failed**
- Ensure CA certificate matches the broker's certificate
- Check that the broker URL uses `ssl://` for TLS connections

**Events not publishing**
- Verify the publish interval is not set to 0
- Check adapter logs for connection status

### Debug Logging

Adapter activities are logged with prefixes:
- `[http]` - HTTP adapter events
- `[nats]` - NATS adapter events
- `[mqtt]` - MQTT adapter events

Example:
```
[nats] connected to nats://control-plane:4222 as edge-001
[nats] subscribed to keystone.edge-001.cmd.apply
[mqtt] connected to tcp://broker:1883 as keystone-edge-001
[mqtt] subscribed to keystone/edge-001/cmd/apply
```
