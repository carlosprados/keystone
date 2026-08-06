+++
title = "Environment variables"
weight = 72
description = "Every KEYSTONE_* variable, grouped by what it affects."
+++

# Environment variables

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
| `KEYSTONE_MAX_EXTRACT_BYTES` | — | Cap on decompressed archive size |

## Artifacts

| Variable | Default | Effect |
|---|---|---|
| `KEYSTONE_ARTIFACT_CACHE_LIMIT_BYTES` | 2 GiB | Cache budget; oldest evicted first |
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

## JetStream

| Variable | Effect |
|---|---|
| `KEYSTONE_JOBS` | JetStream stream name for the job queue |

An invalid value (a non-numeric integer, an unparseable duration, a bad boolean) is
logged and ignored rather than crashing the agent — but check your logs, because
"ignored" means the default is in force.
