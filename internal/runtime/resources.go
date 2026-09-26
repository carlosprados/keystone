//go:build linux

package runtime

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// LimitOpenFiles sets RLIMIT_NOFILE on the process pid, soft and hard.
//
// On pid, not on the caller. It used to be setrlimit in the agent before
// starting the child, which the child inherited — and so did the agent itself,
// for good: one component with open_files = 64 capped the agent and every
// component started after it at 64 descriptors, and lowering a hard limit
// cannot be undone without CAP_SYS_RESOURCE. Measured, not supposed.
func LimitOpenFiles(pid int, n uint64) error {
	if n == 0 {
		return nil
	}
	lim := &unix.Rlimit{Cur: n, Max: n}
	if err := unix.Prlimit(pid, unix.RLIMIT_NOFILE, lim, nil); err != nil {
		return fmt.Errorf("prlimit NOFILE on pid %d: %w", pid, err)
	}
	return nil
}

// IsProcessRunning checks if a PID is alive using signal 0.
func IsProcessRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := unix.Kill(pid, 0)
	if err == nil {
		return true
	}
	if err == unix.ESRCH {
		return false
	}
	// Permision error usually means it's running but we can't signal it
	if err == unix.EPERM {
		return true
	}
	return false
}
