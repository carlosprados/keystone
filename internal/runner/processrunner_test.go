package runner

import (
	"context"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestRunManagedRetryLimit(t *testing.T) {
	pr := New()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	opts := Options{
		Name:    "test-fail",
		Command: "non-existent-command-12345",
	}
	hc := HealthConfig{Interval: 1 * time.Second}
	policy := RestartAlways
	maxRetries := 3

	exitErrCh := make(chan error, 1)
	onExit := func(err error) {
		exitErrCh <- err
	}

	err := pr.RunManaged(ctx, "test-fail", opts, hc, policy, maxRetries, nil, nil, onExit)
	if err == nil {
		t.Fatal("expected error from RunManaged, got nil")
	}

	select {
	case err := <-exitErrCh:
		if err == nil {
			t.Error("expected exit error, got nil")
		}
		// Should contain "start failed after 3 retries"
		expected := "start failed after 3 retries"
		if err.Error() != "" && !contains(err.Error(), expected) {
			t.Errorf("expected error containing %q, got %q", expected, err.Error())
		}
	default:
		t.Error("onExit was not called")
	}
}

func TestRunManagedExitRetryLimit(t *testing.T) {
	pr := New()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Use a command that exits immediately with error
	opts := Options{
		Name:    "test-exit",
		Command: "ls",
		Args:    []string{"/non-existent-dir-999"},
	}
	hc := HealthConfig{Interval: 1 * time.Second}
	policy := RestartAlways
	maxRetries := 2 // 1 initial + 2 retries = 3 starts total

	exitErrCh := make(chan error, 1)
	onExit := func(err error) {
		exitErrCh <- err
	}

	err := pr.RunManaged(ctx, "test-exit", opts, hc, policy, maxRetries, nil, nil, onExit)
	if err == nil {
		t.Fatal("expected error from RunManaged, got nil")
	}

	select {
	case err := <-exitErrCh:
		if err == nil {
			t.Error("expected exit error, got nil")
		}
		expected := "reached restart limit"
		if !contains(err.Error(), expected) {
			t.Errorf("expected error containing %q, got %q", expected, err.Error())
		}
	default:
		t.Error("onExit was not called")
	}
}

func contains(s, substr string) bool {
	// Import strings or use a simple loop
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// TestOpenFilesLimitsTheComponentNotTheAgent: the limit used to be set on the
// agent before starting the child. The agent kept it — soft and hard, and a
// hard limit cannot be raised back without privilege — so one component with
// open_files = 64 capped the agent and every later component at 64.
func TestOpenFilesLimitsTheComponentNotTheAgent(t *testing.T) {
	var before unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &before); err != nil {
		t.Fatal(err)
	}
	h, err := New().Start(context.Background(), Options{Name: "fd", Command: "sleep", Args: []string{"5"}, NoFile: 64})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	ph := h.(*ProcessHandle)
	t.Cleanup(func() { _ = ph.cmd.Process.Kill(); _ = ph.cmd.Wait() })

	var after unix.Rlimit
	_ = unix.Getrlimit(unix.RLIMIT_NOFILE, &after)
	if after != before {
		t.Errorf("the agent's own limit changed from %+v to %+v", before, after)
	}

	var child unix.Rlimit
	if err := unix.Prlimit(ph.pid, unix.RLIMIT_NOFILE, nil, &child); err != nil {
		t.Fatalf("read child limit: %v", err)
	}
	if child.Cur != 64 || child.Max != 64 {
		t.Errorf("child limit = %+v, want 64/64", child)
	}
}
