package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	sysrt "github.com/carlosprados/keystone/internal/runtime"
)

// The test the design called for and the unit tests cannot give: a real plan,
// real processes, and the two guarantees the whole feature rests on.
//
//   - A healthy component keeps its PID across a reconcile. Everything about
//     periodic reconcile is only safe because of this; a regression here would
//     restart production stacks on a timer.
//   - A component killed out of band comes back.
//
// It runs real processes and writes under a temp working directory, so it is
// skipped in -short.
func TestReconcileIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("starts real processes")
	}

	dir := t.TempDir()
	chdir(t, dir)

	writeRecipe(t, "keeper", "keeper.recipe.toml")
	writeRecipe(t, "victim", "victim.recipe.toml")
	planPath := filepath.Join(dir, "plan.toml")
	writeFile(t, planPath, `
[[components]]
name = "keeper"
recipe = "keeper.recipe.toml"

[[components]]
name = "victim"
recipe = "victim.recipe.toml"
`)

	a := New(Options{InsecureSkipVerify: true})
	t.Cleanup(func() { _ = a.Close() })

	if err := a.ApplyPlan(planPath, false); err != nil {
		t.Fatalf("apply: %v", err)
	}
	keeperPID := waitForPID(t, a, "keeper")
	victimPID := waitForPID(t, a, "victim")

	// 1. A reconcile that finds nothing wrong must change nothing.
	res, err := a.ReconcileNow()
	if err != nil {
		t.Fatalf("reconcile over a healthy plan: %v", err)
	}
	if len(res.Repaired) != 0 {
		t.Errorf("a healthy plan reported repairs: %v", res.Repaired)
	}
	if got := pidOf(t, a, "keeper"); got != keeperPID {
		t.Errorf("keeper was restarted by a no-op reconcile: %d -> %d", keeperPID, got)
	}
	if got := pidOf(t, a, "victim"); got != victimPID {
		t.Errorf("victim was restarted by a no-op reconcile: %d -> %d", victimPID, got)
	}

	// 2. Kill one out of band. Its restart policy is "never", so nothing but a
	//    reconcile will bring it back — which is the situation on a gateway
	//    whose component exhausted its retries.
	proc, err := os.FindProcess(victimPID)
	if err != nil {
		t.Fatalf("finding victim %d: %v", victimPID, err)
	}
	if err := proc.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("killing victim %d: %v", victimPID, err)
	}
	waitUntil(t, 5*time.Second, func() bool {
		ci, ok := a.comps.Get("victim")
		return ok && ci.State != "running"
	}, "victim never left running after being killed")

	res, err = a.ReconcileNow()
	if err != nil {
		t.Fatalf("reconcile after the kill: %v", err)
	}
	if len(res.Repaired) != 1 || res.Repaired[0] != "victim" {
		t.Errorf("repaired=%v, want [victim]", res.Repaired)
	}

	newVictim := waitForPID(t, a, "victim")
	if newVictim == victimPID {
		t.Errorf("victim reports the old PID %d; it was not actually restarted", victimPID)
	}
	if !sysrt.IsProcessRunning(newVictim) {
		t.Errorf("victim's new PID %d is not alive", newVictim)
	}
	if got := pidOf(t, a, "keeper"); got != keeperPID {
		t.Errorf("keeper was restarted while repairing victim: %d -> %d", keeperPID, got)
	}
}

// A plan the operator stopped stays stopped, however many passes run.
func TestReconcileIntegrationHonoursAStoppedPlan(t *testing.T) {
	if testing.Short() {
		t.Skip("starts real processes")
	}

	dir := t.TempDir()
	chdir(t, dir)
	writeRecipe(t, "keeper", "keeper.recipe.toml")
	planPath := filepath.Join(dir, "plan.toml")
	writeFile(t, planPath, "[[components]]\nname = \"keeper\"\nrecipe = \"keeper.recipe.toml\"\n")

	a := New(Options{InsecureSkipVerify: true})
	t.Cleanup(func() { _ = a.Close() })

	if err := a.ApplyPlan(planPath, false); err != nil {
		t.Fatalf("apply: %v", err)
	}
	waitForPID(t, a, "keeper")

	if err := a.StopPlan(); err != nil {
		t.Fatalf("stop-plan: %v", err)
	}

	for i := range 3 {
		res, err := a.ReconcileNow()
		if err != nil {
			t.Fatalf("pass %d: %v", i, err)
		}
		if !res.Skipped {
			t.Fatalf("pass %d resurrected a plan the operator stopped", i)
		}
	}
	if ci, ok := a.comps.Get("keeper"); ok && ci.State == "running" {
		t.Error("keeper is running again after stop-plan")
	}
}

// --- helpers -------------------------------------------------------------

func chdir(t *testing.T, dir string) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
}

func writeRecipe(t *testing.T, name, path string) {
	t.Helper()
	// restart_policy = "never" so the runner does not bring it back on its own:
	// the only thing that can revive it is a reconcile, which is the point.
	writeFile(t, path, fmt.Sprintf(`
[metadata]
name = "com.test.%s"
version = "1.0.0"

[lifecycle.run]
type = "process"
restart_policy = "never"

[lifecycle.run.exec]
command = "/bin/sh"
args = ["-c", "exec sleep 300"]
`, name))
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func pidOf(t *testing.T, a *Agent, name string) int {
	t.Helper()
	ci, ok := a.comps.Get(name)
	if !ok {
		t.Fatalf("%s is not in the store", name)
	}
	return ci.PID
}

// waitForPID waits for the component to be observably running: a live PID *and*
// the state the reuse rules are written in terms of. Waiting only for the PID
// would make the test race the state poller.
func waitForPID(t *testing.T, a *Agent, name string) int {
	t.Helper()
	var pid int
	waitUntil(t, 15*time.Second, func() bool {
		ci, ok := a.comps.Get(name)
		if ok && ci.State == "running" && ci.PID > 0 && sysrt.IsProcessRunning(ci.PID) {
			pid = ci.PID
			return true
		}
		return false
	}, name+" never reported as running with a live PID")
	return pid
}

func waitUntil(t *testing.T, limit time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal(msg)
}
