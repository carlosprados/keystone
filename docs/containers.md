# Container Support

Keystone supports running containerized workloads alongside native processes. This document explains how to configure and use container-based components.

## Overview

Keystone uses a **"processes first, containers when needed"** philosophy. By default, components run as native processes, but you can opt into container mode when you need:

- Dependency isolation
- Consistent runtime environments
- Pre-built images from registries
- Resource limits enforcement via cgroups

## Container Runtimes

Keystone supports two container runtime backends:

| Backend | Description | Priority |
|---------|-------------|----------|
| **containerd** | Direct API connection via socket | Primary (preferred) |
| **CLI Fallback** | Uses docker/nerdctl/podman CLI | Fallback if containerd unavailable |

### containerd (Primary)

When containerd is available, Keystone connects directly to the containerd socket for optimal performance and control.

**Requirements:**
- containerd daemon running
- Socket accessible (default: `/run/containerd/containerd.sock`)
- User has permissions to access the socket

**Verify containerd is running:**
```bash
sudo systemctl status containerd
# or
sudo ctr version
```

### CLI Fallback

If containerd socket is unavailable, Keystone automatically detects and uses an available CLI tool:

1. **nerdctl** (preferred) - containerd's Docker-compatible CLI
2. **docker** - Docker Engine CLI
3. **podman** - Podman CLI

**Verify CLI availability:**
```bash
which nerdctl docker podman
```

## Recipe Configuration

To run a component as a container, set `type = "container"` in the `[lifecycle.run]` section.

### Minimal Container Recipe

```toml
[metadata]
name = "com.example.myapp"
version = "1.0.0"

[lifecycle.run]
type = "container"
restart_policy = "always"

[lifecycle.run.container]
image = "docker.io/library/nginx:alpine"
```

### Complete Container Recipe

```toml
[metadata]
name = "com.example.myapp"
version = "1.0.0"
description = "My containerized application"
publisher = "example.com"
type = "container"

# Optional: download config files to mount into container
[[artifacts]]
uri = "https://example.com/configs/app.yaml"
sha256 = "abc123..."

# Optional: run host preparation before starting container
[lifecycle.install]
script = """
mkdir -p /data/myapp
chmod 755 /data/myapp
"""

[lifecycle.run]
type = "container"
restart_policy = "always"
max_retries = 5

[lifecycle.run.container]
# Required: container image reference
image = "docker.io/myorg/myapp:1.0.0"

# Image pull policy: "always", "never", "if-not-present" (default)
pull_policy = "if-not-present"

# Network mode: "host", "bridge", "none" (default: "bridge")
network_mode = "bridge"

# User to run as (uid:gid format)
user = "1000:1000"

# Run in privileged mode (use with caution)
privileged = false

# Container hostname
hostname = "myapp"

# Environment variables
[lifecycle.run.container.env]
LOG_LEVEL = "info"
DATABASE_URL = "postgres://localhost/mydb"
API_KEY = "${API_KEY}"  # Substituted from host environment

# Container labels
[lifecycle.run.container.labels]
"com.example.team" = "platform"
"com.example.environment" = "production"

# Volume mounts
[[lifecycle.run.container.mounts]]
source = "/data/myapp"          # Host path
target = "/app/data"            # Container path
type = "bind"                   # "bind", "volume", "tmpfs"
read_only = false

[[lifecycle.run.container.mounts]]
source = "myapp-logs"           # Named volume
target = "/var/log/myapp"
type = "volume"

[[lifecycle.run.container.mounts]]
source = ""                     # Not used for tmpfs
target = "/tmp"
type = "tmpfs"

# Port mappings (only for bridge network mode)
[[lifecycle.run.container.ports]]
host_ip = "0.0.0.0"             # Bind to all interfaces
host_port = 8080                # Host port
container_port = 80             # Container port
protocol = "tcp"                # "tcp" or "udp"

[[lifecycle.run.container.ports]]
host_port = 8443
container_port = 443
protocol = "tcp"

# Resource limits
[lifecycle.run.container.resources]
memory_mb = 512                 # Memory limit in MB
memory_swap = 1024              # Memory+swap limit (-1 for unlimited)
cpu_shares = 1024               # CPU shares (relative weight)
cpu_quota = 50000               # CPU quota in microseconds (50% of one core)
cpu_period = 100000             # CPU period in microseconds
pids_limit = 100                # Max number of processes

# Health check (works for both containers and processes)
[lifecycle.run.health]
check = "http://localhost:8080/health"
interval = "10s"
timeout = "3s"
failure_threshold = 3

[lifecycle.shutdown]
script = """
echo "Container stopped"
"""
```

## Container Configuration Reference

### Image Settings

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `image` | string | (required) | Full image reference (registry/repo:tag) |
| `pull_policy` | string | `"if-not-present"` | When to pull: `always`, `never`, `if-not-present` |

### Network Settings

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `network_mode` | string | `"bridge"` | Network mode: `host`, `bridge`, `none` |
| `hostname` | string | container ID | Container hostname |
| `ports` | array | `[]` | Port mappings (bridge mode only) |

**Network Modes:**
- **host**: Container shares host network namespace (best performance, no port mapping needed)
- **bridge**: Container gets its own network namespace with NAT (requires port mappings)
- **none**: No network access

### Security Settings

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `user` | string | image default | User to run as (`uid`, `uid:gid`, or `username`) |
| `privileged` | bool | `false` | Run with elevated privileges |

### Resource Limits

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `memory_mb` | int64 | unlimited | Memory limit in megabytes |
| `memory_swap` | int64 | unlimited | Memory+swap limit (-1 for unlimited swap) |
| `cpu_shares` | int64 | 1024 | CPU shares (relative weight) |
| `cpu_quota` | int64 | unlimited | CPU quota in microseconds per period |
| `cpu_period` | int64 | 100000 | CPU period in microseconds |
| `pids_limit` | int64 | unlimited | Maximum number of processes |

### Volume Mounts

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `source` | string | (required) | Host path or volume name |
| `target` | string | (required) | Container path |
| `type` | string | `"bind"` | Mount type: `bind`, `volume`, `tmpfs` |
| `read_only` | bool | `false` | Mount as read-only |

### Port Mappings

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `host_ip` | string | `"0.0.0.0"` | Host IP to bind |
| `host_port` | int | (required) | Port on host |
| `container_port` | int | (required) | Port in container |
| `protocol` | string | `"tcp"` | Protocol: `tcp` or `udp` |

## Health Checks

Health checks work the same for containers and processes:

```toml
[lifecycle.run.health]
check = "http://localhost:8080/health"  # HTTP probe
# check = "tcp://localhost:3306"        # TCP probe
# check = "cmd:curl -f http://localhost/health"  # Command probe
interval = "10s"
timeout = "3s"
failure_threshold = 3
```

**Command probes** execute inside the container via `exec`.

## Environment Variables

### Host Environment

| Variable | Default | Description |
|----------|---------|-------------|
| `KEYSTONE_CONTAINERD_SOCKET` | `/run/containerd/containerd.sock` | containerd socket path |
| `KEYSTONE_CONTAINERD_NAMESPACE` | `keystone` | containerd namespace |
| `KEYSTONE_CONTAINER_SNAPSHOTTER` | `overlayfs` | Snapshotter for images |
| `KEYSTONE_CONTAINER_REGISTRY` | `docker.io` | Default registry |

### Container Environment

Environment variables in the recipe can reference host environment variables using `${VAR}` syntax:

```toml
[lifecycle.run.container.env]
API_KEY = "${API_KEY}"           # From host environment
DATABASE_URL = "${DB_URL:-default}"  # With default value
```

## Examples

### Web Server (nginx)

```toml
[metadata]
name = "com.example.nginx"
version = "1.0.0"

[lifecycle.run]
type = "container"
restart_policy = "always"

[lifecycle.run.container]
image = "docker.io/library/nginx:alpine"
network_mode = "host"

[lifecycle.run.health]
check = "http://localhost:80/"
interval = "10s"
```

### Database (PostgreSQL)

```toml
[metadata]
name = "com.example.postgres"
version = "15.0"

[lifecycle.run]
type = "container"
restart_policy = "always"

[lifecycle.run.container]
image = "docker.io/library/postgres:15-alpine"
user = "999:999"

[lifecycle.run.container.env]
POSTGRES_USER = "app"
POSTGRES_PASSWORD = "${POSTGRES_PASSWORD}"
POSTGRES_DB = "myapp"

[[lifecycle.run.container.mounts]]
source = "/data/postgres"
target = "/var/lib/postgresql/data"
type = "bind"

[[lifecycle.run.container.ports]]
host_port = 5432
container_port = 5432

[lifecycle.run.container.resources]
memory_mb = 1024

[lifecycle.run.health]
check = "tcp://localhost:5432"
interval = "5s"
```

### Custom Application

```toml
[metadata]
name = "com.example.api"
version = "2.1.0"

[[artifacts]]
uri = "https://example.com/configs/api.yaml"
sha256 = "..."

[lifecycle.install]
script = """
mkdir -p /data/api/config
# With unpack=false, Keystone stages single-file artifacts into the component
# working directory, so api.yaml is available here directly.
cp ./api.yaml /data/api/config/
"""

[lifecycle.run]
type = "container"
restart_policy = "on-failure"
max_retries = 3

[lifecycle.run.container]
image = "ghcr.io/myorg/api:2.1.0"
pull_policy = "always"
network_mode = "bridge"

[lifecycle.run.container.env]
CONFIG_PATH = "/config/api.yaml"
LOG_LEVEL = "info"

[[lifecycle.run.container.mounts]]
source = "/data/api/config"
target = "/config"
read_only = true

[[lifecycle.run.container.ports]]
host_port = 8080
container_port = 8080

[lifecycle.run.container.resources]
memory_mb = 512
cpu_shares = 512
pids_limit = 50

[lifecycle.run.health]
check = "http://localhost:8080/health"
interval = "10s"
failure_threshold = 3
```

## Mixed Deployments

You can mix processes and containers in the same deployment plan:

```toml
# plan.toml
[[components]]
name = "database"
recipe = "recipes/postgres-container.recipe.toml"

[[components]]
name = "cache"
recipe = "recipes/redis-container.recipe.toml"

[[components]]
name = "api"
recipe = "recipes/api-process.recipe.toml"  # Native process
```

## Troubleshooting

### containerd Connection Failed

```
failed to connect to containerd at /run/containerd/containerd.sock: ...
```

**Solutions:**
1. Verify containerd is running: `sudo systemctl status containerd`
2. Check socket permissions: `ls -la /run/containerd/containerd.sock`
3. Add user to containerd group or run as root
4. Use custom socket path: `KEYSTONE_CONTAINERD_SOCKET=/path/to/socket`

### Image Pull Failed

```
failed to pull image docker.io/library/nginx:latest: ...
```

**Solutions:**
1. Check network connectivity
2. Verify image reference is correct
3. For private registries, configure credentials
4. Use `pull_policy = "never"` with pre-pulled images

### Container Exits Immediately

**Check logs:**
```bash
journalctl -u keystone -f  # Agent logs
sudo ctr -n keystone tasks ls  # List running tasks
sudo ctr -n keystone containers ls  # List containers
```

**Common causes:**
- Missing required environment variables
- Invalid command or entrypoint
- Permission issues with mounts

### No CLI Available

```
no container CLI found (nerdctl, docker, podman)
```

**Solutions:**
1. Install containerd and access the socket directly
2. Install nerdctl: `go install github.com/containerd/nerdctl/cmd/nerdctl@latest`
3. Install Docker or Podman

## Security Considerations

1. **Avoid privileged mode** unless absolutely necessary
2. **Use non-root users** when possible (`user = "1000:1000"`)
3. **Mount volumes read-only** when write access isn't needed
4. **Limit resources** to prevent resource exhaustion
5. **Use trusted images** from verified registries
6. **Keep images updated** for security patches

## Performance Tips

1. **Use host network** for latency-sensitive applications
2. **Pre-pull images** to avoid startup delays
3. **Use overlay mounts** instead of bind mounts when possible
4. **Set appropriate resource limits** to prevent noisy neighbors
5. **Use alpine-based images** for smaller footprint
