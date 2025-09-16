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

type LifecycleRun struct {
	Exec          LifecycleRunExec `toml:"exec"`
	RestartPolicy string           `toml:"restart_policy"`
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
