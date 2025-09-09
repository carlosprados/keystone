package version

// These variables can be overridden via -ldflags during build/publish.
var (
	Version = "0.1.0-dev"
	Commit  = "unknown"
)
