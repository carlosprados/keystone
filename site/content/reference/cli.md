+++
title = "Command-line flags"
weight = 71
description = "Every flag of the agent, and the keystonectl commands."
+++

# Command-line flags

## keystone

The authoritative list is always `keystone --help` on the binary you are running.
This is that output:

```text
Usage of /tmp/claude-1000/-home-charlie-Dropbox-Charlie-1-Projects-keystone/545b403b-58b1-4684-a5ca-8c7c5c815a78/scratchpad/ks-help:
  -api-token string
    	Bearer token required for the HTTP API (or KEYSTONE_API_TOKEN); required to bind a non-loopback address
  -demo
    	Run a built-in demo: start a mock 3-component stack
  -http string
    	HTTP listen address (empty to disable) (default "127.0.0.1:8080")
  -insecure-skip-verify
    	Disable mandatory artifact integrity checks (sha256 + signature). Dev/demo only (or KEYSTONE_INSECURE_SKIP_VERIFY=true)
  -mqtt-broker string
    	MQTT broker URL (empty to disable MQTT adapter)
  -mqtt-client-id string
    	MQTT client ID (defaults to keystone-{device-id})
  -mqtt-device-id string
    	Device ID for MQTT topics (required if MQTT enabled)
  -mqtt-health-interval duration
    	Interval for publishing health events (0 to disable) (default 30s)
  -mqtt-pass string
    	MQTT password
  -mqtt-qos int
    	Default QoS level for commands and responses (0, 1, or 2) (default 1)
  -mqtt-state-interval duration
    	Interval for publishing state events (0 to disable) (default 10s)
  -mqtt-tls-ca string
    	Path to MQTT CA certificate
  -mqtt-tls-cert string
    	Path to MQTT client TLS certificate
  -mqtt-tls-key string
    	Path to MQTT client TLS key
  -mqtt-tls-verify
    	Verify MQTT server TLS certificate (default true)
  -mqtt-user string
    	MQTT username
  -nats-creds string
    	Path to NATS credentials file (.creds)
  -nats-device-id string
    	Device ID for NATS subjects (required if NATS enabled)
  -nats-health-interval duration
    	Interval for publishing health events (0 to disable) (default 30s)
  -nats-jetstream
    	Enable JetStream for persistent job queue
  -nats-js-stream string
    	JetStream stream name for jobs (default "KEYSTONE_JOBS")
  -nats-js-workers int
    	Number of concurrent job processor workers (default 1)
  -nats-nkey string
    	Path to NATS NKey seed file
  -nats-pass string
    	NATS password
  -nats-state-interval duration
    	Interval for publishing state events (0 to disable) (default 10s)
  -nats-tls-ca string
    	Path to NATS CA certificate
  -nats-tls-cert string
    	Path to NATS client TLS certificate
  -nats-tls-key string
    	Path to NATS client TLS key
  -nats-tls-verify
    	Verify NATS server TLS certificate (default true)
  -nats-token string
    	NATS authentication token
  -nats-url string
    	NATS server URL (empty to disable NATS adapter)
  -nats-user string
    	NATS username
  -version
    	Print version and exit
```

### The ones you will actually use

| Flag | Why |
|---|---|
| `--http` | Listen address. `""` disables the HTTP adapter entirely |
| `--api-token` | Required to bind anything but loopback |
| `--insecure-skip-verify` | Development only. Disables mandatory artifact integrity |
| `--nats-url` / `--nats-device-id` | Enable the NATS adapter |
| `--mqtt-broker` / `--mqtt-device-id` | Enable the MQTT adapter |
| `--demo` | Run a built-in mock 3-component stack. Good for a first look |
| `--version` | Print version and commit |

Flags always win over the equivalent environment variable.

## keystonectl

```bash
keystonectl version                       # client version and commit
keystonectl status                        # plan status
keystonectl components                    # every component with state, PID, health
keystonectl graph                         # dependency graph and start order

keystonectl apply plan.toml               # upload and apply a plan
keystonectl apply plan.toml --dry         # same, previewing only
keystonectl apply-dry plan.toml           # ditto
keystonectl stop-plan                     # stop every component

keystonectl stop <component>              # stop one component
keystonectl restart <component>           # restart one, cascading per dependency type
keystonectl restart-dry <component>       # what a restart would touch

keystonectl recipes                       # list the recipe store
keystonectl upload-recipe api.toml        # add a recipe (--force to overwrite)
keystonectl sha256 dist/api.tar.gz        # compute a digest for a recipe artifact
```

Note `stop-plan` (everything) versus `stop <component>` (one thing) — they are
different commands, and the plural mistake is an outage.

Two global flags: `--addr` (default `http://127.0.0.1:8080`) for a remote agent, and
`--token`, which falls back to `KEYSTONE_API_TOKEN`.

The CLI is a thin wrapper over the HTTP API, so the wait-for-health behaviour of
`POST /v1/components/{name}:restart?wait=health` is available with curl but not yet
exposed as a `keystonectl` flag.

## keystoneserver

A minimal static file server, for serving test artifacts during development:

```bash
keystoneserver --addr :9000 --root ./dist
```

## Task targets

Development uses [Task](https://taskfile.dev/), not make:

```bash
task build     # all three binaries, with version ldflags
task run       # agent with HTTP on :8080
task dev       # live reload via air
task test      # go test -v ./...
task fmt       # go fmt ./...
task vet       # go vet ./...
task hooks     # install the go fmt pre-commit hook
```
