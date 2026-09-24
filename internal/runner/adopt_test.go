package runner

import (
	"os/exec"
	"testing"
	"time"
)

// sleeper starts a real process and returns its PID, killing it at test end.
func sleeper(t *testing.T, seconds string) int {
	t.Helper()
	cmd := exec.Command("sleep", seconds)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd.Process.Pid
}

// TestAdoptTakesOverALiveProcess is the whole point: supervision resumes
// without the process being restarted, so an agent update is not an outage for
// everything it supervises.
func TestAdoptTakesOverALiveProcess(t *testing.T) {
	r := NewProcessRunner()
	pid := sleeper(t, "30")

	h, err := r.Adopt(pid, "sleeper")
	if err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if h.PID() != pid {
		t.Errorf("PID = %d, want %d", h.PID(), pid)
	}
	if h.Name() != "sleeper" {
		t.Errorf("Name = %q", h.Name())
	}

	// Still running: nothing on the done channel.
	select {
	case err := <-h.Done():
		t.Fatalf("a live process reported an exit: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestAdoptNoticesTheProcessLeaving: without wait() there is no SIGCHLD, so an
// exit has to be polled for. If this did not work, a component could die and
// the agent would keep reporting it as running — worse than restarting it.
func TestAdoptNoticesTheProcessLeaving(t *testing.T) {
	r := NewProcessRunner()

	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	pid := cmd.Process.Pid

	h, err := r.Adopt(pid, "sleeper")
	if err != nil {
		t.Fatalf("adopt: %v", err)
	}

	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()

	select {
	case <-h.Done():
	case <-time.After(5 * adoptInterval):
		t.Fatal("the exit of an adopted process was never noticed")
	}
}

// TestAdoptRefusesADeadPID: a PID that is gone must not be adopted, because
// the alternative is an agent supervising nothing and saying it is fine.
func TestAdoptRefusesADeadPID(t *testing.T) {
	r := NewProcessRunner()

	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("run true: %v", err)
	}
	dead := cmd.Process.Pid

	if _, err := r.Adopt(dead, "gone"); err == nil {
		t.Fatal("a dead PID was adopted")
	}
	if _, err := r.Adopt(0, "zero"); err == nil {
		t.Fatal("PID 0 was adopted")
	}
	if _, err := r.Adopt(-1, "negative"); err == nil {
		t.Fatal("a negative PID was adopted")
	}
}
