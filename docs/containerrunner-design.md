# ContainerRunner Design Document

## Overview

This document describes the design for Phase 6: ContainerRunner implementation using containerd/nerdctl. The goal is to enable Keystone to manage containerized components alongside native processes.

## Research Summary

### containerd Client API

containerd provides a Go client library (`github.com/containerd/containerd/v2/client`) with:

- **Client**: Connects to containerd socket (`/run/containerd/containerd.sock`)
- **Container**: Represents a container (metadata, spec, snapshots)
- **Task**: Represents a running container process
- **Image**: Container images from registries

Key lifecycle:
```
Client.Pull(image) → Client.NewContainer() → Container.NewTask() → Task.Start()
                                                                 → Task.Wait()
                                                                 → Task.Kill()
                                                                 → Task.Delete()
                                                                 → Container.Delete()
```

### nerdctl

nerdctl is a Docker-compatible CLI for containerd. It's primarily a CLI tool, not a library. For programmatic use, we should use the containerd client directly, with nerdctl as an optional CLI fallback.

**Sources:**
- [containerd Go client](https://pkg.go.dev/github.com/containerd/containerd/v2/client)
- [containerd GitHub](https://github.com/containerd/containerd)
- [nerdctl GitHub](https://github.com/containerd/nerdctl)

---

## Architecture

### Current ProcessRunner Architecture

```
┌─────────────────────────────────────────────────────────┐
│                        Agent                            │
│  (applyPlan, component state, health callbacks)         │
└──────────────────────────┬──────────────────────────────┘
                           │
                           ▼
                    ┌─────────────┐
                    │ProcessRunner│
                    │  (native)   │
                    └──────┬──────┘
                           │
                           ▼
                    ┌─────────────┐
                    │ProcessHandle│
                    │ (PID, Cmd)  │
                    └─────────────┘
```

### Proposed Architecture with ContainerRunner

```
┌─────────────────────────────────────────────────────────┐
│                        Agent                            │
│  (applyPlan, component state, health callbacks)         │
└──────────────────────────┬──────────────────────────────┘
                           │
              ┌────────────┴────────────┐
              │     Runner Interface    │
              └────────────┬────────────┘
                           │
         ┌─────────────────┼─────────────────┐
         │                 │                 │
         ▼                 ▼                 ▼
  ┌─────────────┐   ┌─────────────┐   ┌─────────────┐
  │ProcessRunner│   │ContainerRun │   │  CLIRunner  │
  │  (native)   │   │ (containerd)│   │(docker/nerd)│
  └──────┬──────┘   └──────┬──────┘   └──────┬──────┘
         │                 │                 │
         ▼                 ▼                 ▼
  ┌─────────────┐   ┌─────────────┐   ┌─────────────┐
  │ProcessHandle│   │ContainerHnd │   │  CLIHandle  │
  │ (PID, Cmd)  │   │(ContainerID)│   │ (ID, Name)  │
  └─────────────┘   └─────────────┘   └─────────────┘
```

---

## Implementation Strategy

### Phase 1: Define Runner Interface

Create a common interface that both ProcessRunner and ContainerRunner implement.

```go
// internal/runner/runner.go

package runner

import (
    "context"
    "time"
)

// Handle represents a running component (process or container)
type Handle interface {
    // ID returns the unique identifier (PID for processes, container ID for containers)
    ID() string

    // Name returns the component name
    Name() string

    // StartedAt returns when the component started
    StartedAt() time.Time

    // Done returns a channel that signals when the component exits
    Done() <-chan error

    // Type returns "process" or "container"
    Type() string
}

// Runner manages the lifecycle of components
type Runner interface {
    // Start launches a component and returns a handle
    Start(ctx context.Context, opts Options) (Handle, error)

    // Stop gracefully stops a component
    Stop(ctx context.Context, h Handle, timeout time.Duration) error

    // RunManaged starts a component with health monitoring and restart policies
    RunManaged(
        ctx context.Context,
        name string,
        opts Options,
        hc HealthConfig,
        policy RestartPolicy,
        maxRetries int,
        onStart func(Handle),
        onHealth func(bool),
        onExit func(error),
    ) error
}

// Options for starting a component
type Options struct {
    // Common fields
    Name       string
    Env        []string
    WorkingDir string

    // Process-specific
    Command string
    Args    []string
    NoFile  uint64 // RLIMIT_NOFILE

    // Container-specific
    Image       string            // e.g., "docker.io/library/nginx:latest"
    Registry    string            // Registry URL (optional)
    PullPolicy  string            // "always", "never", "if-not-present"
    Mounts      []Mount           // Volume mounts
    Ports       []PortMapping     // Port mappings
    Resources   ResourceLimits    // CPU/memory limits
    NetworkMode string            // "host", "bridge", "none"
    User        string            // User to run as
    Privileged  bool              // Privileged mode
}

// Mount represents a volume mount
type Mount struct {
    Source   string // Host path or volume name
    Target   string // Container path
    ReadOnly bool
    Type     string // "bind", "volume", "tmpfs"
}

// PortMapping represents a port mapping
type PortMapping struct {
    HostIP        string
    HostPort      uint16
    ContainerPort uint16
    Protocol      string // "tcp", "udp"
}

// ResourceLimits for container resources
type ResourceLimits struct {
    CPUShares  int64  // CPU shares (relative weight)
    CPUQuota   int64  // CPU quota in microseconds
    CPUPeriod  int64  // CPU period in microseconds
    MemoryMB   int64  // Memory limit in MB
    MemorySwap int64  // Memory+Swap limit (-1 for unlimited swap)
    PidsLimit  int64  // Max number of PIDs
}
```

### Phase 2: Refactor ProcessRunner

Update ProcessRunner to implement the Runner interface.

```go
// internal/runner/processrunner.go

// ProcessHandle implements Handle for native processes
type ProcessHandle struct {
    pid       int
    cmd       *exec.Cmd
    name      string
    startedAt time.Time
    done      chan error
}

func (h *ProcessHandle) ID() string           { return strconv.Itoa(h.pid) }
func (h *ProcessHandle) Name() string         { return h.name }
func (h *ProcessHandle) StartedAt() time.Time { return h.startedAt }
func (h *ProcessHandle) Done() <-chan error   { return h.done }
func (h *ProcessHandle) Type() string         { return "process" }

// PID returns the process ID (process-specific)
func (h *ProcessHandle) PID() int { return h.pid }

// Cmd returns the exec.Cmd (process-specific)
func (h *ProcessHandle) Cmd() *exec.Cmd { return h.cmd }

// ProcessRunner implements Runner for native processes
type ProcessRunner struct{}

func NewProcessRunner() *ProcessRunner { return &ProcessRunner{} }

func (r *ProcessRunner) Start(ctx context.Context, opts Options) (Handle, error) {
    // existing implementation, return ProcessHandle
}

func (r *ProcessRunner) Stop(ctx context.Context, h Handle, timeout time.Duration) error {
    ph, ok := h.(*ProcessHandle)
    if !ok {
        return fmt.Errorf("invalid handle type for ProcessRunner")
    }
    // existing implementation
}

func (r *ProcessRunner) RunManaged(...) error {
    // existing implementation
}
```

### Phase 3: Implement ContainerRunner

Create ContainerRunner using containerd client.

```go
// internal/runner/containerrunner.go

package runner

import (
    "context"
    "fmt"
    "time"

    "github.com/containerd/containerd/v2/client"
    "github.com/containerd/containerd/v2/pkg/cio"
    "github.com/containerd/containerd/v2/pkg/oci"
)

// ContainerHandle implements Handle for containers
type ContainerHandle struct {
    containerID string
    name        string
    startedAt   time.Time
    done        chan error

    // containerd-specific
    container client.Container
    task      client.Task
}

func (h *ContainerHandle) ID() string           { return h.containerID }
func (h *ContainerHandle) Name() string         { return h.name }
func (h *ContainerHandle) StartedAt() time.Time { return h.startedAt }
func (h *ContainerHandle) Done() <-chan error   { return h.done }
func (h *ContainerHandle) Type() string         { return "container" }

// ContainerID returns the full container ID
func (h *ContainerHandle) ContainerID() string { return h.containerID }

// ContainerRunner implements Runner for containerd containers
type ContainerRunner struct {
    client    *client.Client
    namespace string
    snapshotter string
}

// ContainerRunnerConfig holds configuration for the container runner
type ContainerRunnerConfig struct {
    // Socket path for containerd (default: /run/containerd/containerd.sock)
    Socket string

    // Namespace for containers (default: "keystone")
    Namespace string

    // Snapshotter to use (default: "overlayfs")
    Snapshotter string

    // Default registry for images (default: "docker.io")
    DefaultRegistry string
}

func DefaultContainerRunnerConfig() ContainerRunnerConfig {
    return ContainerRunnerConfig{
        Socket:          "/run/containerd/containerd.sock",
        Namespace:       "keystone",
        Snapshotter:     "overlayfs",
        DefaultRegistry: "docker.io",
    }
}

func NewContainerRunner(cfg ContainerRunnerConfig) (*ContainerRunner, error) {
    c, err := client.New(cfg.Socket,
        client.WithDefaultNamespace(cfg.Namespace),
    )
    if err != nil {
        return nil, fmt.Errorf("failed to connect to containerd: %w", err)
    }

    return &ContainerRunner{
        client:      c,
        namespace:   cfg.Namespace,
        snapshotter: cfg.Snapshotter,
    }, nil
}

func (r *ContainerRunner) Close() error {
    if r.client != nil {
        return r.client.Close()
    }
    return nil
}

func (r *ContainerRunner) Start(ctx context.Context, opts Options) (Handle, error) {
    // 1. Pull image if needed
    image, err := r.ensureImage(ctx, opts.Image, opts.PullPolicy)
    if err != nil {
        return nil, fmt.Errorf("failed to ensure image: %w", err)
    }

    // 2. Create container spec
    specOpts := r.buildSpecOpts(opts, image)

    // 3. Create container
    containerID := fmt.Sprintf("keystone-%s-%d", opts.Name, time.Now().UnixNano())
    container, err := r.client.NewContainer(ctx, containerID,
        client.WithImage(image),
        client.WithNewSnapshot(containerID+"-snapshot", image),
        client.WithNewSpec(specOpts...),
    )
    if err != nil {
        return nil, fmt.Errorf("failed to create container: %w", err)
    }

    // 4. Create task (this starts the container)
    task, err := container.NewTask(ctx, cio.NewCreator(cio.WithStdio))
    if err != nil {
        container.Delete(ctx, client.WithSnapshotCleanup)
        return nil, fmt.Errorf("failed to create task: %w", err)
    }

    // 5. Start task
    if err := task.Start(ctx); err != nil {
        task.Delete(ctx)
        container.Delete(ctx, client.WithSnapshotCleanup)
        return nil, fmt.Errorf("failed to start task: %w", err)
    }

    // 6. Create handle
    handle := &ContainerHandle{
        containerID: containerID,
        name:        opts.Name,
        startedAt:   time.Now(),
        done:        make(chan error, 1),
        container:   container,
        task:        task,
    }

    // 7. Monitor task exit in background
    go r.monitorTask(ctx, handle)

    return handle, nil
}

func (r *ContainerRunner) Stop(ctx context.Context, h Handle, timeout time.Duration) error {
    ch, ok := h.(*ContainerHandle)
    if !ok {
        return fmt.Errorf("invalid handle type for ContainerRunner")
    }

    // 1. Send SIGTERM
    if err := ch.task.Kill(ctx, syscall.SIGTERM); err != nil {
        log.Printf("[container] warning: SIGTERM failed: %v", err)
    }

    // 2. Wait for exit or timeout
    select {
    case <-ch.done:
        // Exited gracefully
    case <-time.After(timeout):
        // Force kill
        log.Printf("[container] timeout waiting for %s, sending SIGKILL", ch.containerID)
        if err := ch.task.Kill(ctx, syscall.SIGKILL); err != nil {
            log.Printf("[container] warning: SIGKILL failed: %v", err)
        }
        select {
        case <-ch.done:
        case <-time.After(5 * time.Second):
            return fmt.Errorf("container did not exit after SIGKILL")
        }
    case <-ctx.Done():
        return ctx.Err()
    }

    // 3. Delete task and container
    if _, err := ch.task.Delete(ctx); err != nil {
        log.Printf("[container] warning: task delete failed: %v", err)
    }
    if err := ch.container.Delete(ctx, client.WithSnapshotCleanup); err != nil {
        log.Printf("[container] warning: container delete failed: %v", err)
    }

    return nil
}

func (r *ContainerRunner) RunManaged(
    ctx context.Context,
    name string,
    opts Options,
    hc HealthConfig,
    policy RestartPolicy,
    maxRetries int,
    onStart func(Handle),
    onHealth func(bool),
    onExit func(error),
) error {
    // Similar to ProcessRunner.RunManaged but for containers
    // Uses the same health check logic (HTTP, TCP, cmd)
    // For cmd health checks, exec into container

    retries := 0
    for {
        handle, err := r.Start(ctx, opts)
        if err != nil {
            retries++
            if maxRetries > 0 && retries > maxRetries {
                if onExit != nil {
                    onExit(fmt.Errorf("start failed after %d retries: %w", maxRetries, err))
                }
                return err
            }
            backoff := calculateBackoff(retries)
            select {
            case <-ctx.Done():
                return ctx.Err()
            case <-time.After(backoff):
                continue
            }
        }

        if onStart != nil {
            onStart(handle)
        }

        // Health monitoring loop (same as ProcessRunner)
        // ... (similar implementation)

        select {
        case <-ctx.Done():
            return nil
        case err := <-handle.Done():
            if policy == RestartNever {
                if onExit != nil {
                    onExit(err)
                }
                return err
            }
            // Restart logic...
        }
    }
}

// Helper methods

func (r *ContainerRunner) ensureImage(ctx context.Context, ref string, pullPolicy string) (client.Image, error) {
    // Check if image exists
    image, err := r.client.GetImage(ctx, ref)
    if err == nil && pullPolicy != "always" {
        return image, nil
    }

    if pullPolicy == "never" {
        return nil, fmt.Errorf("image %s not found and pull policy is 'never'", ref)
    }

    // Pull image
    image, err = r.client.Pull(ctx, ref,
        client.WithPullUnpack,
        client.WithPullSnapshotter(r.snapshotter),
    )
    if err != nil {
        return nil, fmt.Errorf("failed to pull image %s: %w", ref, err)
    }

    return image, nil
}

func (r *ContainerRunner) buildSpecOpts(opts Options, image client.Image) []oci.SpecOpts {
    specOpts := []oci.SpecOpts{
        oci.WithImageConfig(image),
    }

    // Command and args
    if opts.Command != "" {
        args := append([]string{opts.Command}, opts.Args...)
        specOpts = append(specOpts, oci.WithProcessArgs(args...))
    }

    // Environment
    if len(opts.Env) > 0 {
        specOpts = append(specOpts, oci.WithEnv(opts.Env))
    }

    // Working directory
    if opts.WorkingDir != "" {
        specOpts = append(specOpts, oci.WithProcessCwd(opts.WorkingDir))
    }

    // User
    if opts.User != "" {
        specOpts = append(specOpts, oci.WithUser(opts.User))
    }

    // Mounts
    for _, m := range opts.Mounts {
        specOpts = append(specOpts, oci.WithMounts([]specs.Mount{
            {
                Destination: m.Target,
                Source:      m.Source,
                Type:        m.Type,
                Options:     mountOptions(m),
            },
        }))
    }

    // Resources
    if opts.Resources.MemoryMB > 0 {
        specOpts = append(specOpts, oci.WithMemoryLimit(uint64(opts.Resources.MemoryMB)*1024*1024))
    }
    if opts.Resources.CPUShares > 0 {
        specOpts = append(specOpts, oci.WithCPUShares(uint64(opts.Resources.CPUShares)))
    }

    // Privileged
    if opts.Privileged {
        specOpts = append(specOpts, oci.WithPrivileged)
    }

    // Network mode
    if opts.NetworkMode == "host" {
        specOpts = append(specOpts, oci.WithHostNamespace(specs.NetworkNamespace))
    }

    return specOpts
}

func (r *ContainerRunner) monitorTask(ctx context.Context, h *ContainerHandle) {
    exitCh, err := h.task.Wait(ctx)
    if err != nil {
        h.done <- err
        return
    }

    status := <-exitCh
    if status.Error() != nil {
        h.done <- status.Error()
    } else if status.ExitCode() != 0 {
        h.done <- fmt.Errorf("container exited with code %d", status.ExitCode())
    } else {
        h.done <- nil
    }
}
```

### Phase 4: Recipe Schema Extension

Extend recipe TOML to support containers:

```toml
[metadata]
name = "myapp"
version = "1.0.0"

# For containers, artifacts are optional (image contains everything)
# But can still be used for config files, etc.
[[artifacts]]
uri = "file:///configs/myapp.yaml"
sha256 = "..."

[lifecycle.run]
# New field: type = "process" (default) or "container"
type = "container"

# Container-specific configuration
[lifecycle.run.container]
image = "ghcr.io/myorg/myapp:1.0.0"
pull_policy = "always"  # "always", "never", "if-not-present"
network_mode = "host"   # "host", "bridge", "none"
user = "1000:1000"
privileged = false

[[lifecycle.run.container.mounts]]
source = "/data/myapp"
target = "/app/data"
type = "bind"
read_only = false

[[lifecycle.run.container.mounts]]
source = "myapp-logs"
target = "/var/log/myapp"
type = "volume"

[[lifecycle.run.container.ports]]
host_port = 8080
container_port = 80
protocol = "tcp"

[lifecycle.run.container.resources]
memory_mb = 512
cpu_shares = 1024
pids_limit = 100

# Environment (same as process)
[lifecycle.run.container.env]
LOG_LEVEL = "info"
DATABASE_URL = "${DATABASE_URL}"

# Health check (same as process - works for both)
[lifecycle.run.health]
check = "http://localhost:8080/health"
interval = "10s"
timeout = "3s"
failure_threshold = 3

# Restart policy (same as process)
restart_policy = "always"
max_retries = 5
```

Recipe types.go extension:

```go
// internal/recipe/types.go

type Run struct {
    Type          string        `toml:"type"` // "process" or "container"
    Exec          *Exec         `toml:"exec"`
    Container     *Container    `toml:"container"`
    Health        *HealthProbe  `toml:"health"`
    RestartPolicy string        `toml:"restart_policy"`
    MaxRetries    int           `toml:"max_retries"`
}

type Container struct {
    Image       string            `toml:"image"`
    PullPolicy  string            `toml:"pull_policy"`
    NetworkMode string            `toml:"network_mode"`
    User        string            `toml:"user"`
    Privileged  bool              `toml:"privileged"`
    Mounts      []ContainerMount  `toml:"mounts"`
    Ports       []ContainerPort   `toml:"ports"`
    Resources   ContainerResources `toml:"resources"`
    Env         map[string]string `toml:"env"`
}

type ContainerMount struct {
    Source   string `toml:"source"`
    Target   string `toml:"target"`
    Type     string `toml:"type"`
    ReadOnly bool   `toml:"read_only"`
}

type ContainerPort struct {
    HostIP        string `toml:"host_ip"`
    HostPort      int    `toml:"host_port"`
    ContainerPort int    `toml:"container_port"`
    Protocol      string `toml:"protocol"`
}

type ContainerResources struct {
    MemoryMB  int64 `toml:"memory_mb"`
    CPUShares int64 `toml:"cpu_shares"`
    CPUQuota  int64 `toml:"cpu_quota"`
    PidsLimit int64 `toml:"pids_limit"`
}
```

### Phase 5: Agent Integration

Update agent to detect runner type and use appropriate runner:

```go
// internal/agent/agent.go

func (a *Agent) createRunner(runType string) (runner.Runner, error) {
    switch runType {
    case "container":
        cfg := runner.DefaultContainerRunnerConfig()
        // Override from env if needed
        if socket := os.Getenv("KEYSTONE_CONTAINERD_SOCKET"); socket != "" {
            cfg.Socket = socket
        }
        return runner.NewContainerRunner(cfg)
    case "process", "":
        return runner.NewProcessRunner(), nil
    default:
        return nil, fmt.Errorf("unknown run type: %s", runType)
    }
}

// In applyPlan(), detect runner type from recipe:
func (a *Agent) applyPlan(planPath string, dry bool) error {
    // ... existing code ...

    for _, it := range items {
        recipe := it.Recipe
        runType := "process"
        if recipe.Lifecycle.Run != nil && recipe.Lifecycle.Run.Type != "" {
            runType = recipe.Lifecycle.Run.Type
        }

        r, err := a.createRunner(runType)
        if err != nil {
            return fmt.Errorf("failed to create runner for %s: %w", it.Name, err)
        }

        // Build options based on runner type
        var opts runner.Options
        if runType == "container" {
            opts = a.buildContainerOptions(recipe)
        } else {
            opts = a.buildProcessOptions(recipe)
        }

        // Use runner.RunManaged (same interface for both)
        // ...
    }
}

func (a *Agent) buildContainerOptions(recipe *recipe.Recipe) runner.Options {
    c := recipe.Lifecycle.Run.Container
    return runner.Options{
        Name:        recipe.Metadata.Name,
        Image:       c.Image,
        PullPolicy:  c.PullPolicy,
        NetworkMode: c.NetworkMode,
        User:        c.User,
        Privileged:  c.Privileged,
        Env:         envMapToSlice(c.Env),
        Mounts:      convertMounts(c.Mounts),
        Ports:       convertPorts(c.Ports),
        Resources:   convertResources(c.Resources),
    }
}
```

---

## CLI Runner (Optional Fallback)

For environments without containerd socket access, provide a CLI-based runner:

```go
// internal/runner/clirunner.go

// CLIRunner uses docker/nerdctl/podman CLI
type CLIRunner struct {
    cli     string // "docker", "nerdctl", "podman"
    timeout time.Duration
}

func NewCLIRunner() (*CLIRunner, error) {
    // Detect available CLI
    for _, cli := range []string{"nerdctl", "docker", "podman"} {
        if _, err := exec.LookPath(cli); err == nil {
            return &CLIRunner{cli: cli, timeout: 30 * time.Second}, nil
        }
    }
    return nil, fmt.Errorf("no container CLI found (nerdctl, docker, podman)")
}

func (r *CLIRunner) Start(ctx context.Context, opts Options) (Handle, error) {
    args := r.buildRunArgs(opts)
    cmd := exec.CommandContext(ctx, r.cli, args...)

    output, err := cmd.Output()
    if err != nil {
        return nil, fmt.Errorf("%s run failed: %w", r.cli, err)
    }

    containerID := strings.TrimSpace(string(output))

    return &CLIHandle{
        id:        containerID,
        name:      opts.Name,
        cli:       r.cli,
        startedAt: time.Now(),
        done:      make(chan error, 1),
    }, nil
}
```

---

## Health Checks for Containers

Container health checks need special handling for `cmd:` probes:

```go
func probeOnce(hc HealthConfig, opts Options, handle Handle) bool {
    u := hc.Check

    // HTTP/TCP probes work the same
    if hasPrefix(u, "http://") || hasPrefix(u, "https://") {
        // ... existing HTTP probe ...
    }
    if hasPrefix(u, "tcp://") {
        // ... existing TCP probe ...
    }

    // Command probe - different for containers
    if hasPrefix(u, "cmd:") {
        cmdStr := u[len("cmd:"):]

        if ch, ok := handle.(*ContainerHandle); ok {
            // Exec into container
            ctx, cancel := context.WithTimeout(context.Background(), hc.Timeout)
            defer cancel()

            proc, err := ch.task.Exec(ctx, "health-"+strconv.Itoa(int(time.Now().UnixNano())),
                &specs.Process{
                    Args: []string{"/bin/sh", "-c", cmdStr},
                    Cwd:  "/",
                },
                cio.NullIO,
            )
            if err != nil {
                return false
            }
            defer proc.Delete(ctx)

            if err := proc.Start(ctx); err != nil {
                return false
            }

            exitCh, _ := proc.Wait(ctx)
            status := <-exitCh
            return status.ExitCode() == 0
        }

        // Process fallback
        ctx, cancel := context.WithTimeout(context.Background(), hc.Timeout)
        defer cancel()
        cmd := exec.CommandContext(ctx, "/bin/sh", "-c", cmdStr)
        return cmd.Run() == nil
    }

    return true
}
```

---

## Metrics for Containers

Extend metrics collection for containers:

```go
// internal/metrics/container.go

func SampleContainerMetrics(ctx context.Context, name string, containerID string, client *containerd.Client) {
    ticker := time.NewTicker(15 * time.Second)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            container, err := client.LoadContainer(ctx, containerID)
            if err != nil {
                continue
            }

            task, err := container.Task(ctx, nil)
            if err != nil {
                continue
            }

            metrics, err := task.Metrics(ctx)
            if err != nil {
                continue
            }

            // Parse cgroup metrics
            // Update Prometheus gauges
            componentCPU.WithLabelValues(name).Set(extractCPU(metrics))
            componentMemory.WithLabelValues(name).Set(extractMemory(metrics))
        }
    }
}
```

---

## Configuration

New environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `KEYSTONE_CONTAINERD_SOCKET` | `/run/containerd/containerd.sock` | containerd socket path |
| `KEYSTONE_CONTAINERD_NAMESPACE` | `keystone` | containerd namespace |
| `KEYSTONE_CONTAINER_SNAPSHOTTER` | `overlayfs` | Snapshotter to use |
| `KEYSTONE_CONTAINER_CLI` | (auto-detect) | Force CLI runner (docker/nerdctl/podman) |

---

## Testing Strategy

1. **Unit tests**: Mock containerd client interface
2. **Integration tests**: Use containerd in test containers
3. **CLI runner tests**: Mock exec commands

```go
// internal/runner/containerrunner_test.go

func TestContainerRunner_Start(t *testing.T) {
    if os.Getenv("TEST_CONTAINERD") == "" {
        t.Skip("containerd not available")
    }

    cfg := DefaultContainerRunnerConfig()
    r, err := NewContainerRunner(cfg)
    require.NoError(t, err)
    defer r.Close()

    ctx := context.Background()
    handle, err := r.Start(ctx, Options{
        Name:  "test-container",
        Image: "docker.io/library/alpine:latest",
        Command: "sleep",
        Args:    []string{"10"},
    })
    require.NoError(t, err)

    assert.NotEmpty(t, handle.ID())
    assert.Equal(t, "container", handle.Type())

    err = r.Stop(ctx, handle, 5*time.Second)
    require.NoError(t, err)
}
```

---

## Implementation Order

1. **Week 1**: Runner interface + ProcessRunner refactor
2. **Week 2**: ContainerRunner with containerd client
3. **Week 3**: Recipe schema extension + agent integration
4. **Week 4**: CLI runner fallback + health check adaptation
5. **Week 5**: Metrics + testing + documentation

---

## Dependencies

```go
// go.mod additions
require (
    github.com/containerd/containerd/v2 v2.0.0
    github.com/opencontainers/runtime-spec v1.2.0
)
```

---

## Risks and Mitigations

| Risk | Mitigation |
|------|------------|
| containerd not available on all systems | CLI runner fallback |
| Socket permission issues | Document required permissions, support rootless |
| Image pull failures | Retry with backoff, offline mode support |
| Resource isolation differences | Document behavior differences vs processes |
| Network complexity (ports, DNS) | Start with host network, add bridge later |
