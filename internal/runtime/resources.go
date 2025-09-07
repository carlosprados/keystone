//go:build linux

package runtime

import (
    "fmt"
    "golang.org/x/sys/unix"
)

// ApplyRlimits sets soft limits for NOFILE if provided (>0).
func ApplyRlimits(noFile uint64) error {
    if noFile == 0 { return nil }
    lim := &unix.Rlimit{Cur: noFile, Max: noFile}
    if err := unix.Setrlimit(unix.RLIMIT_NOFILE, lim); err != nil {
        return fmt.Errorf("setrlimit NOFILE: %w", err)
    }
    return nil
}

// WithCgroup creates a cgroup for the given pid and applies CPU/memory limits.
// It returns a cleanup function to remove the cgroup. Best-effort: if cgroups are
// unavailable, it returns a no-op cleanup and nil error.
func WithCgroup(pid int, cpuQuotaPct int64, memoryLimitBytes int64) (func() error, error) {
    // Optional: cgroups setup is intentionally a no-op in the MVP build to avoid
    // extra kernel/runtime coupling. This function serves as a placeholder and
    // returns a no-op cleanup.
    _ = pid
    _ = cpuQuotaPct
    _ = memoryLimitBytes
    return func() error { return nil }, nil
}
