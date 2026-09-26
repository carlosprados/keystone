// Package runner provides interfaces and implementations for running components.
// It supports both native processes (ProcessRunner) and containers (ContainerRunner).
package runner

import (
	"context"
	"time"
)

// Handle represents a running component (process or container).
type Handle interface {
	// ID returns the unique identifier (PID string for processes, container ID for containers)
	ID() string

	// Name returns the component name
	Name() string

	// StartedAt returns when the component started
	StartedAt() time.Time

	// Done returns a channel that receives an error (or nil) when the component exits
	Done() <-chan error

	// Type returns "process" or "container"
	Type() string
}

// Runner manages the lifecycle of components.
type Runner interface {
	// Start launches a component and returns a handle
	Start(ctx context.Context, opts Options) (Handle, error)

	// Stop gracefully stops a component
	Stop(ctx context.Context, h Handle, timeout time.Duration) error

	// RunManaged starts a component with health monitoring and restart policies.
	// It blocks until the context is canceled or a terminal error occurs.
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

// Options specifies how to start a component.
type Options struct {
	// Common fields
	Name       string
	Env        []string
	WorkingDir string

	// Process-specific fields
	Command string
	Args    []string
	NoFile  uint64 // RLIMIT_NOFILE for processes
	// Security restricts the privileges of a process component (user,
	// no_new_privileges, capability allow-list). Enforced by ProcessRunner; a
	// restriction that cannot be applied prevents the process from starting.
	Security Security
	// AdoptPID asks the runner to take over an already-running process instead
	// of starting a new one, for the FIRST attempt only. It exists so that
	// restarting the agent does not restart everything it supervises. If the
	// process is gone by the time the runner looks, it starts one normally —
	// adoption is an optimisation, never a precondition.
	AdoptPID int

	// Container-specific fields
	Image          string            // Container image (e.g., "docker.io/library/nginx:latest")
	PullPolicy     string            // "always", "never", "if-not-present" (default: "if-not-present")
	Mounts         []Mount           // Volume mounts
	Ports          []PortMapping     // Port mappings
	Resources      ResourceLimits    // CPU/memory limits
	NetworkMode    string            // "host", "bridge", "none", or a user-defined network name (CLI runtimes)
	NetworkAliases []string          // Extra DNS names on a user-defined network (CLI runtimes only)
	User           string            // User to run as (e.g., "1000:1000")
	Privileged     bool              // Run in privileged mode
	Hostname       string            // Container hostname
	Labels         map[string]string // Container labels
}

// Mount represents a volume mount for containers.
type Mount struct {
	Source   string // Host path or volume name
	Target   string // Container path
	ReadOnly bool
	Type     string // "bind", "volume", "tmpfs" (default: "bind")
}

// PortMapping represents a port mapping for containers.
type PortMapping struct {
	HostIP        string // Host IP to bind (default: "0.0.0.0")
	HostPort      uint16 // Host port
	ContainerPort uint16 // Container port
	Protocol      string // "tcp", "udp" (default: "tcp")
}

// ResourceLimits specifies resource constraints for containers.
type ResourceLimits struct {
	CPUShares  int64 // CPU shares (relative weight, default: 1024)
	CPUQuota   int64 // CPU quota in microseconds per CPUPeriod
	CPUPeriod  int64 // CPU period in microseconds (default: 100000)
	MemoryMB   int64 // Memory limit in MB
	MemorySwap int64 // Memory+Swap limit in MB (-1 for unlimited swap)
	PidsLimit  int64 // Max number of PIDs
}

// HealthConfig defines how to probe a component's health.
type HealthConfig struct {
	Check string // http://..., tcp://..., cmd:...
	// Exec is an argv run directly, with no shell. Set instead of Check when
	// the workload has no /bin/sh.
	Exec             []string
	Interval         time.Duration // Probe interval (default: 10s)
	Timeout          time.Duration // Probe timeout (default: 3s)
	FailureThreshold int           // Failures before unhealthy (default: 3)
}

// Configured reports whether this component declares a health probe at all.
//
// It exists because the answer was being spelled out as `Check == ""` in six
// places, and when the argv form was added only the probing code learned about
// it — every one of those checks kept asking about Check alone. A component
// with `exec` and no `check` therefore declared a probe that was never run: the
// agent reported its health as "unknown" forever and nothing supervised it.
//
// Worse than the bug it replaced. Before, a probe that could not run failed and
// the component was restarted; after, there was no probe at all and everything
// looked fine.
func (h HealthConfig) Configured() bool {
	return h.Check != "" || len(h.Exec) > 0
}

// RestartPolicy defines when to restart a component.
type RestartPolicy string

const (
	RestartNever     RestartPolicy = "never"
	RestartOnFailure RestartPolicy = "on-failure"
	RestartAlways    RestartPolicy = "always"
)

// RunType indicates the type of runner to use.
type RunType string

const (
	RunTypeProcess   RunType = "process"
	RunTypeContainer RunType = "container"
)

// DefaultHealthConfig returns a HealthConfig with sensible defaults.
func DefaultHealthConfig() HealthConfig {
	return HealthConfig{
		Interval:         10 * time.Second,
		Timeout:          3 * time.Second,
		FailureThreshold: 3,
	}
}

// IsContainerRunner returns true if the options indicate container mode.
func (o *Options) IsContainerRunner() bool {
	return o.Image != ""
}

// cfsPeriod is the CFS period a quota is measured against: the declared one, or
// the kernel's default of 100 ms.
func cfsPeriod(r ResourceLimits) int64 {
	if r.CPUPeriod > 0 {
		return r.CPUPeriod
	}
	return 100000
}

// swapBytes converts memory_swap (MB, -1 for unlimited) to the bytes the OCI
// spec takes.
func swapBytes(mb int64) int64 {
	if mb < 0 {
		return -1
	}
	return mb * 1024 * 1024
}
