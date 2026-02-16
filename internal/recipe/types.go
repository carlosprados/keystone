package recipe

// Minimal TOML recipe structure for MVP. This matches the example in configs/examples.

type Metadata struct {
	Name        string `toml:"name"`
	Version     string `toml:"version"`
	Description string `toml:"description"`
	Publisher   string `toml:"publisher"`
	Type        string `toml:"type"`
}

type Artifact struct {
	URI     string `toml:"uri"`
	SHA256  string `toml:"sha256"`
	Unpack  bool   `toml:"unpack"`
	SigURI  string `toml:"sig_uri"`  // detached signature file
	CertURI string `toml:"cert_uri"` // optional leaf cert if not provisioned
	// Optional HTTP headers to attach when downloading this artifact/signature/cert.
	// Example:
	//   [ [artifacts] ]
	//   uri = "https://..."
	//   [artifacts.headers]
	//   Accept = "application/zip"
	Headers map[string]string `toml:"headers"`
	// Optional GitHub token to set Authorization for github.com/api.github.com downloads
	// when Authorization is not already provided via Headers.
	GithubToken string `toml:"github_token"`
}

type LifecycleInstall struct {
	RequirePrivilege bool   `toml:"require_privilege"`
	Script           string `toml:"script"`
}

type LifecycleRunExec struct {
	Command    string            `toml:"command"`
	Args       []string          `toml:"args"`
	WorkingDir string            `toml:"working_dir"`
	Env        map[string]string `toml:"env"`
}

// ContainerConfig holds container-specific configuration.
type ContainerConfig struct {
	Image       string             `toml:"image"`        // Container image (e.g., "docker.io/library/nginx:latest")
	PullPolicy  string             `toml:"pull_policy"`  // "always", "never", "if-not-present"
	NetworkMode string             `toml:"network_mode"` // "host", "bridge", "none"
	User        string             `toml:"user"`         // User to run as (e.g., "1000:1000")
	Privileged  bool               `toml:"privileged"`   // Run in privileged mode
	Hostname    string             `toml:"hostname"`     // Container hostname
	Mounts      []ContainerMount   `toml:"mounts"`       // Volume mounts
	Ports       []ContainerPort    `toml:"ports"`        // Port mappings
	Resources   ContainerResources `toml:"resources"`    // Container resource limits
	Env         map[string]string  `toml:"env"`          // Environment variables
	Labels      map[string]string  `toml:"labels"`       // Container labels
}

// ContainerMount represents a volume mount for containers.
type ContainerMount struct {
	Source   string `toml:"source"`    // Host path or volume name
	Target   string `toml:"target"`    // Container path
	Type     string `toml:"type"`      // "bind", "volume", "tmpfs" (default: "bind")
	ReadOnly bool   `toml:"read_only"` // Mount as read-only
}

// ContainerPort represents a port mapping for containers.
type ContainerPort struct {
	HostIP        string `toml:"host_ip"`        // Host IP to bind (default: "0.0.0.0")
	HostPort      int    `toml:"host_port"`      // Host port
	ContainerPort int    `toml:"container_port"` // Container port
	Protocol      string `toml:"protocol"`       // "tcp", "udp" (default: "tcp")
}

// ContainerResources specifies resource limits for containers.
type ContainerResources struct {
	MemoryMB   int64 `toml:"memory_mb"`   // Memory limit in MB
	CPUShares  int64 `toml:"cpu_shares"`  // CPU shares (relative weight)
	CPUQuota   int64 `toml:"cpu_quota"`   // CPU quota in microseconds
	CPUPeriod  int64 `toml:"cpu_period"`  // CPU period in microseconds
	MemorySwap int64 `toml:"memory_swap"` // Memory+Swap limit in MB (-1 for unlimited)
	PidsLimit  int64 `toml:"pids_limit"`  // Max number of PIDs
}

type LifecycleRun struct {
	Type          string           `toml:"type"` // "process" (default) or "container"
	Exec          LifecycleRunExec `toml:"exec"`
	Container     ContainerConfig  `toml:"container"`
	RestartPolicy string           `toml:"restart_policy"`
	MaxRetries    int              `toml:"max_retries"`
	Health        Health           `toml:"health"`
}

type LifecycleShutdown struct {
	Script string `toml:"script"`
}

type Lifecycle struct {
	Install  LifecycleInstall  `toml:"install"`
	Run      LifecycleRun      `toml:"run"`
	Shutdown LifecycleShutdown `toml:"shutdown"`
}

type ConfigDefaults struct {
	// store generically in MVP
}

type Recipe struct {
	Metadata     Metadata     `toml:"metadata"`
	Artifacts    []Artifact   `toml:"artifacts"`
	Lifecycle    Lifecycle    `toml:"lifecycle"`
	Resources    Resources    `toml:"resources"`
	Dependencies []Dependency `toml:"dependencies"`
}

// Health probe definition
type Health struct {
	Check            string `toml:"check"`    // http://..., tcp://..., cmd:...
	Interval         string `toml:"interval"` // e.g., "10s"
	Timeout          string `toml:"timeout"`
	FailureThreshold int    `toml:"failure_threshold"`
}

// Resources maps to simple limits for the MVP
type Resources struct {
	OpenFiles   uint64 `toml:"open_files"`
	MemoryLimit string `toml:"memory_limit"` // placeholder, cgroups not enforced yet
	CPUQuota    int64  `toml:"cpu_quota"`    // placeholder
}

// Dependency models recipe-level dependencies referencing other components by name.
type Dependency struct {
	Name    string `toml:"name"`
	Version string `toml:"version"`
	Type    string `toml:"type"` // hard|soft (unused in MVP)
}
