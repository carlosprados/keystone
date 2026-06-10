package agent

import (
	"os"
	"testing"

	"github.com/carlosprados/keystone/internal/store"
)

func TestProcessExists(t *testing.T) {
	if !processExists(os.Getpid()) {
		t.Errorf("processExists(self) returned false; the test process exists")
	}
	for _, bad := range []int{0, -1, -42} {
		if processExists(bad) {
			t.Errorf("processExists(%d) returned true; non-positive PIDs are never valid", bad)
		}
	}
	// 99999999 is well above PID_MAX on Linux (4194303 default) so the lookup
	// must fail with ESRCH.
	if processExists(99999999) {
		t.Errorf("processExists(99999999) returned true; that PID cannot exist")
	}
}

func TestProcessIsInitOrphan(t *testing.T) {
	// The Go test runner is our parent; we are not parented by init.
	if processIsInitOrphan(os.Getpid()) {
		t.Errorf("test process must not look like an init orphan")
	}
	// PID 1 is init itself: PPid is 0, not 1.
	if processIsInitOrphan(1) {
		t.Errorf("init (pid 1) has PPid=0 and must not be classified as init orphan")
	}
	for _, bad := range []int{0, -1, 99999999} {
		if processIsInitOrphan(bad) {
			t.Errorf("processIsInitOrphan(%d) returned true; expected false for invalid/unknown PIDs", bad)
		}
	}
}

func TestReapOrphanedComponents_NoOpCases(t *testing.T) {
	// Empty list: nothing to reap, no panic.
	if got := reapOrphanedComponents(nil); got != 0 {
		t.Errorf("nil list: reaped=%d, want 0", got)
	}
	// PID <= 0 entries are silently skipped.
	if got := reapOrphanedComponents([]store.ComponentInfo{
		{Name: "a", PID: 0},
		{Name: "b", PID: -1},
	}); got != 0 {
		t.Errorf("non-positive PIDs must be skipped, reaped=%d", got)
	}
	// A PID that is alive but is NOT parented by init (the test process is
	// parented by the test runner) must not be reaped.
	if got := reapOrphanedComponents([]store.ComponentInfo{
		{Name: "self", PID: os.Getpid()},
	}); got != 0 {
		t.Errorf("non-init-parented PID must not be reaped, reaped=%d", got)
	}
	// A clearly-non-existent PID is skipped (read /proc/<pid>/status fails).
	if got := reapOrphanedComponents([]store.ComponentInfo{
		{Name: "gone", PID: 99999999},
	}); got != 0 {
		t.Errorf("non-existent PID must not be reaped, reaped=%d", got)
	}
}

func TestShouldResumeLastPlan(t *testing.T) {
	cases := []struct {
		status string
		want   bool
		reason string
	}{
		{"", true, "empty (legacy or first run) resumes"},
		{"running", true, "running resumes"},
		{"failed", true, "failed resumes"},
		{"applying", true, "interrupted apply resumes (the v0.1.0 boot-resume bug)"},
		{"APPLYING", true, "case-insensitive"},
		{"  applying  ", true, "trims whitespace"},
		{"stopped", false, "explicit stop is respected"},
		{"STOPPED", false, "case-insensitive stop"},
		{"dry-run", false, "dry-run never installed anything, do not resume"},
		{"unknown-future-status", true, "unknown values default to resume (safer than silent skip)"},
	}
	for _, c := range cases {
		if got := shouldResumeLastPlan(c.status); got != c.want {
			t.Errorf("shouldResumeLastPlan(%q)=%v, want %v (%s)", c.status, got, c.want, c.reason)
		}
	}
}
