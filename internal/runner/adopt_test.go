package runner

import (
	"context"
	"os/exec"
	"sync"
	"syscall"
	"testing"
	"time"

	sysrt "github.com/carlosprados/keystone/internal/runtime"
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

// survivor starts a process in its own group, the way the previous agent did,
// and reaps it when it exits so polling sees it gone rather than a zombie.
func survivor(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start survivor: %v", err)
	}
	go func() { _ = cmd.Wait() }()
	t.Cleanup(func() { _ = syscall.Kill(cmd.Process.Pid, syscall.SIGKILL) })
	return cmd.Process.Pid
}

type starts struct {
	mu   sync.Mutex
	pids []int
}

func (s *starts) add(h Handle) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pids = append(s.pids, h.(*ProcessHandle).PID())
}

func (s *starts) get() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int(nil), s.pids...)
}

// TestRunManagedSupervisesAnAdoptedProcess goes through the managed loop, not
// Adopt alone. Adopt was tested by itself while the loop around it called
// cmd.Wait on a handle with no cmd: the first real adoption crashed the agent.
func TestRunManagedSupervisesAnAdoptedProcess(t *testing.T) {
	pid := survivor(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	var got starts
	go New().RunManaged(ctx, "c", Options{Name: "c", Command: "sleep", Args: []string{"30"}, AdoptPID: pid},
		HealthConfig{}, RestartAlways, 5, got.add, nil, nil)

	time.Sleep(3 * adoptInterval)
	if p := got.get(); len(p) != 1 || p[0] != pid {
		t.Fatalf("starts = %v, want exactly one, the adopted pid %d", p, pid)
	}
	if !sysrt.IsProcessRunning(pid) {
		t.Fatal("the adopted process is gone; adoption must not disturb it")
	}
}

// TestAnAdoptedProcessThatDiesIsRestarted: the restart policy has to come back
// with adoption, or an adopted component that exits stays down unnoticed.
func TestAnAdoptedProcessThatDiesIsRestarted(t *testing.T) {
	pid := survivor(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	var got starts
	go New().RunManaged(ctx, "c", Options{Name: "c", Command: "sleep", Args: []string{"30"}, AdoptPID: pid},
		HealthConfig{}, RestartAlways, 5, got.add, nil, nil)
	time.Sleep(adoptInterval)

	_ = syscall.Kill(pid, syscall.SIGKILL)

	deadline := time.Now().Add(3*adoptInterval + 5*time.Second)
	for time.Now().Before(deadline) {
		if p := got.get(); len(p) == 2 {
			if p[1] == pid {
				t.Fatalf("restarted with the dead pid %d", pid)
			}
			syscall.Kill(p[1], syscall.SIGKILL)
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("starts = %v; the adopted process died and was never restarted", got.get())
}

// TestStopStopsAnAdoptedProcess: Stop used to return nil for a handle with no
// cmd, so an adopted component could never be stopped — a restart then ran a
// second copy beside it.
func TestStopStopsAnAdoptedProcess(t *testing.T) {
	pid := survivor(t)
	r := New()
	h, err := r.Adopt(pid, "c")
	if err != nil {
		t.Fatalf("adopt: %v", err)
	}

	if err := r.Stop(context.Background(), h, 3*time.Second); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if sysrt.IsProcessRunning(pid) {
		t.Fatal("the adopted process is still running after Stop")
	}
}
