package agent

import (
	"fmt"
	"syscall"
	"testing"
	"time"
)

// TestRestartBringsTheComponentBackWithAnyPolicy: an operator's restart stops
// the instance and starts a new one, whatever the restart policy says about
// exits the agent did not ask for. With "never" and "on-failure", the exit of
// the instance being stopped used to be recorded as a terminal failure of the
// component, on top of the instance that had just replaced it: the new one was
// marked failed with PID 0, its handle and runner dropped, and the restart
// timed out waiting for a PID.
func TestRestartBringsTheComponentBackWithAnyPolicy(t *testing.T) {
	if testing.Short() {
		t.Skip("starts real processes")
	}
	for _, policy := range []string{"never", "on-failure", "always"} {
		t.Run(policy, func(t *testing.T) {
			chdir(t, t.TempDir())
			writeFile(t, "app.recipe.toml", fmt.Sprintf(`
[metadata]
name = "com.test.app"
version = "1.0.0"

[lifecycle.run]
type = "process"
restart_policy = %q

[lifecycle.run.exec]
command = "/bin/sh"
args = ["-c", "exec sleep 3001"]
`, policy))
			a := New(Options{InsecureSkipVerify: true})
			t.Cleanup(func() { _ = a.Close() })
			if err := a.ApplyPlanContent(appPlan("app.recipe.toml"), false); err != nil {
				t.Fatalf("apply: %v", err)
			}
			pid := waitForPID(t, a, "app")
			t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })

			if _, err := a.RestartComponent("app", "pid", 15*time.Second); err != nil {
				t.Fatalf("restart: %v", err)
			}
			newPID := waitForPID(t, a, "app")
			t.Cleanup(func() { _ = syscall.Kill(-newPID, syscall.SIGKILL) })
			if newPID == pid {
				t.Fatalf("the component was not restarted (pid %d)", pid)
			}

			// The exit of the old instance must not land on the new one late:
			// give it time to, then check the new instance is still the one
			// the agent reports, and still alive.
			time.Sleep(time.Second)
			ci, ok := a.comps.Get("app")
			if !ok || ci.State != "running" || ci.PID != newPID {
				t.Fatalf("after the restart the agent reports state=%q pid=%d, want running with pid %d", ci.State, ci.PID, newPID)
			}
			if err := syscall.Kill(newPID, 0); err != nil {
				t.Fatalf("the restarted instance (pid %d) is not alive: %v", newPID, err)
			}
			a.mu.RLock()
			_, hasHandle := a.handles["app"]
			_, hasRunner := a.runners["app"]
			a.mu.RUnlock()
			if !hasHandle || !hasRunner {
				t.Fatalf("the restarted instance lost its handle=%v or runner=%v", hasHandle, hasRunner)
			}
		})
	}
}
