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
	// Optional delta-download source. Absent means "always fetch the whole
	// artifact", which is what every recipe written before this field did.
	Delta *ArtifactDelta `toml:"delta"`
}

// ArtifactDelta opts one artifact into patch-based updates: instead of
// downloading the whole archive again, the agent patches the copy it already
// has. It is always optional and always falls back — see internal/artifact.
//
// The patch transforms the *uncompressed* archive, not the compressed one: a
// single changed byte reshuffles a gzip stream, so a delta over .tar.gz saves
// nothing (measured: 98% of the full size). Over the uncompressed tar of two
// adjacent Keystone releases the same patch is 3%.
//
// SHA256 is the digest of the uncompressed archive after patching, and it is
// the trust gate for this path. It is trustworthy for the same reason the rest
// of the recipe is: a file-loaded recipe carries a detached signature verified
// against the trust bundle before anything in it is acted on. No new signing
// machinery is needed, and no second signature has to be published.
type ArtifactDelta struct {
	// Server is the base URL of a delta server. The agent derives the patch
	// URL from the two digests it already knows:
	//
	//	{server}/delta/{sha256 of the base archive}/{SHA256}
	//
	// so no manifest, handshake or heartbeat is involved. Any server
	// implementing that route works; github.com/carlosprados/ota-updater does.
	Server string `toml:"server"`
	// SHA256 is the hex digest of the uncompressed archive the patch must
	// produce. Required: without it there is nothing to verify the patch
	// against, and an unverified patch is not a download, it is an exploit.
	SHA256 string `toml:"sha256"`
	// Format names the patch encoding. Empty means the default,
	// artifact.DeltaFormatBsdiffZstd. An unrecognised value is not an error:
	// the agent logs it and falls back to the full download, so a server that
	// moves to a new encoding degrades loudly instead of corrupting bytes.
	Format string `toml:"format"`
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
	Runtime     string             `toml:"runtime"`      // "auto"(default), "containerd", "cli", "nerdctl", "docker", "podman"
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

// SecurityConfig restricts the privileges of a process component. It is the
// process-runner counterpart of a systemd unit's User=, NoNewPrivileges= and
// AmbientCapabilities=, and it applies only to `type = "process"`; containers
// are confined through [lifecycle.run.container] instead.
//
// Everything declared here is enforced or the component refuses to start: a
// restriction that cannot be applied is an error, never a silent no-op.
type SecurityConfig struct {
	// User to run the process as: "user", "uid", "user:group" or "uid:gid".
	// Empty means the agent's own user, which is usually root.
	User string `toml:"user"`
	// NoNewPrivileges forbids gaining privileges through execve, for this
	// process and everything it starts. Irreversible once set.
	NoNewPrivileges bool `toml:"no_new_privileges"`
	// Capabilities is the allow-list of capabilities the process may ever hold,
	// e.g. ["CAP_NET_BIND_SERVICE"]. Declaring the key — even as an empty list,
	// which means "none at all" — drops everything else from the bounding set.
	// Omitting it leaves capabilities untouched, so nil and [] differ on
	// purpose.
	Capabilities []string `toml:"capabilities"`
}

type LifecycleRun struct {
	Type          string           `toml:"type"` // "process" (default) or "container"
	Exec          LifecycleRunExec `toml:"exec"`
	Container     ContainerConfig  `toml:"container"`
	Security      SecurityConfig   `toml:"security"`
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

	// UnknownFields lists the keys the file carried that this struct does not
	// declare, each as `"lifecycle.run.restart_polciy" (line 6)`. Loading does
	// not fail on them — an agent older than a field must still run the recipe,
	// which is what lets a new field reach a mixed-version fleet. A dry run
	// turns this list into an error and the agent logs it, so a typo is loud
	// where it is cheap to fix and visible where it is not.
	//
	// Not a TOML field: it is derived from the parse, and a recipe that tried to
	// set it would be reporting on itself.
	UnknownFields []string `toml:"-"`
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
//
// Type controls two independent semantics of the dependency:
//
//   - hard      (default): dependency must be present in the plan AND the
//     dependent is restarted whenever the dependency is restarted.
//   - soft               : dependency is optional in the plan AND the
//     dependent is restarted whenever the dependency is restarted.
//   - ordering           : dependency must be present in the plan but the
//     dependent is NOT restarted when the dependency is restarted; it only
//     governs start/stop ordering.
//
// An empty value is treated as "hard" for backward compatibility.
type Dependency struct {
	Name    string `toml:"name"`
	Version string `toml:"version"`
	Type    string `toml:"type"` // hard|soft|ordering (default hard)
}
