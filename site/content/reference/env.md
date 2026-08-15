+++
title = "Environment variables"
weight = 73
description = "Every KEYSTONE_* variable, grouped by what it affects."
+++

The agent loads a `.env` file from its working directory at startup, before
anything else, so these can live there or in a systemd `EnvironmentFile`.

**A flag always wins over its environment variable.**

## Security

| Variable | Default | Effect |
|---|---|---|
| `KEYSTONE_API_TOKEN` | — | Bearer token for the HTTP API. Required for a non-loopback bind |
| `KEYSTONE_TRUST_BUNDLE` | — | PEM file of CAs used to verify artifact and recipe signatures |
| `KEYSTONE_LEAF_CERT` | — | Provisioned signing leaf certificate |
| `KEYSTONE_INSECURE_SKIP_VERIFY` | `false` | Disables mandatory artifact integrity. Development only |
| `KEYSTONE_MAX_REQUEST_BYTES` | 4 MiB | HTTP request body cap (413 on overflow) |
| `KEYSTONE_MAX_EXTRACT_BYTES` | 2 GiB | Cap on decompressed archive size, so a small archive cannot fill the disk |

## Artifacts

| Variable | Default | Effect |
|---|---|---|
| `KEYSTONE_ARTIFACT_CACHE_LIMIT_BYTES` | 2 GiB | Cache budget; oldest evicted first |
| `KEYSTONE_DELTA_MAX_BASE_BYTES` | 256 MiB | Largest artifact the delta path will attempt. Patching costs memory proportional to the artifact, so past this size downloading it is cheaper than reconstructing it. `0` disables the limit |
| `KEYSTONE_ARTIFACT_DOWNLOAD_TIMEOUT` | `30m` | Per-artifact download timeout. Accepts `5m`, `1h` |

## Lifecycle

| Variable | Default | Effect |
|---|---|---|
| `KEYSTONE_INSTALL_TIMEOUT` | `2m` | Bound on the install phase of a layer |
| `KEYSTONE_DEVICE_ID` | hostname | Device identity, used by the messaging adapters |

## Containers

| Variable | Default | Effect |
|---|---|---|
| `KEYSTONE_CONTAINERD_SOCKET` | `/run/containerd/containerd.sock` | containerd endpoint |
| `KEYSTONE_CONTAINERD_NAMESPACE` | `keystone` | containerd namespace |
| `KEYSTONE_CONTAINER_SNAPSHOTTER` | — | Snapshotter override (e.g. `native`, `overlayfs`) |
| `KEYSTONE_CONTAINER_REGISTRY` | — | Default registry for unqualified image names |
| `KEYSTONE_IMAGE_VOLUME_DIR` | — | Where image volumes are materialised |
| `KEYSTONE_CNI_CONF_DIR` | — | CNI configuration directory |
| `KEYSTONE_CNI_PLUGIN_DIRS` | — | CNI plugin search path |
| `KEYSTONE_CNI_NETNS_DIR` | — | CNI network namespace directory |

## Periodic reconcile

Off unless you ask for it. See [reconcile and reuse]({{% relref "/concepts/reconcile-and-reuse" %}}).

| Variable | Flag | Default |
|---|---|---|
| `KEYSTONE_RECONCILE_INTERVAL` | `--reconcile-interval` | `0` (disabled) |
| `KEYSTONE_RECONCILE_JITTER` | `--reconcile-jitter` | 10% of the interval |

The jitter offset is derived from the device ID (`KEYSTONE_DEVICE_ID`, or the
hostname), not drawn at random, so a device lands in the same slot on every run
and a fleet still spreads across the window. Setting the jitter to `0`
explicitly is honoured — that is the right value for a single device.

## MQTT

Every MQTT flag has an environment equivalent, which is how you normally configure
it under systemd:

| Variable | Flag |
|---|---|
| `KEYSTONE_MQTT_BROKER` | `--mqtt-broker` |
| `KEYSTONE_MQTT_DEVICE_ID` | `--mqtt-device-id` |
| `KEYSTONE_MQTT_CLIENT_ID` | `--mqtt-client-id` |
| `KEYSTONE_MQTT_USER` / `KEYSTONE_MQTT_PASS` | `--mqtt-user` / `--mqtt-pass` |
| `KEYSTONE_MQTT_TLS_CERT` / `_KEY` / `_CA` | the matching `--mqtt-tls-*` |
| `KEYSTONE_MQTT_TLS_VERIFY` | `--mqtt-tls-verify` |
| `KEYSTONE_MQTT_QOS` | `--mqtt-qos` |
| `KEYSTONE_MQTT_STATE_INTERVAL` | `--mqtt-state-interval` |
| `KEYSTONE_MQTT_HEALTH_INTERVAL` | `--mqtt-health-interval` |

An invalid value (a non-numeric integer, an unparseable duration, a bad boolean) is
logged and ignored rather than crashing the agent — but check your logs, because
"ignored" means the default is in force.

{{% notice style="note" title="KEYSTONE_JOBS is not one of these" %}}
`KEYSTONE_JOBS` is the *default value* of the `--nats-js-stream` flag — the name
of the JetStream stream. The agent never reads it from the environment.
{{% /notice %}}

## keystonectl

The client reads three of its own, and they are the ones worth exporting for a
device you work on regularly. They are not part of the agent's `.env`.

| Variable | Flag | Default |
|---|---|---|
| `KEYSTONE_ADDR` | `--addr` | `http://127.0.0.1:8080` |
| `KEYSTONE_API_TOKEN` | `--token` | (none) |
| `KEYSTONE_SSH` | `--ssh` | (direct connection) |

```bash
export KEYSTONE_SSH=ops@edge-001:52022
export KEYSTONE_ADDR=http://127.0.0.1:9180   # resolved on the SSH host
keystonectl components
```

`KEYSTONE_API_TOKEN` is shared with the agent, which reads it as the token to
require. See [the CLI reference](../keystonectl/#reaching-an-agent-bound-to-loopback) for
what `--ssh` does and what it needs.
