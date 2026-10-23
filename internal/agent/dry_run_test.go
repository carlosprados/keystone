package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/carlosprados/keystone/internal/state"
)

// A dry run on a device that is already running a plan. Every test here
// applies plan v1 for real, previews something, and then checks the device
// behaves as if the preview had never happened. Each of the four is a way the
// previewed plan used to leak into the one in force.

// writeAppRecipe writes version v of one component. The version is in the
// process's command line, so a test can tell which one is running. The policy
// is "always" because one test restarts the component through the API, and a
// "never" component restarted that way does not come back — a separate matter,
// which happens with no dry run involved.
func writeAppRecipe(t *testing.T, v int) string {
	t.Helper()
	path := fmt.Sprintf("app-%d.recipe.toml", v)
	writeFile(t, path, fmt.Sprintf(`
[metadata]
name = "com.test.app"
version = "%d.0.0"

[lifecycle.run]
type = "process"
restart_policy = "always"

[lifecycle.run.exec]
command = "/bin/sh"
args = ["-c", "exec sleep 300%d"]
`, v, v))
	return path
}

func appPlan(recipe string) string {
	return fmt.Sprintf("[[components]]\nname = \"app\"\nrecipe = %q\n", recipe)
}

// cmdlineOf is what the process runs, as ps shows it.
func cmdlineOf(pid int) string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.ReplaceAll(string(b), "\x00", " "))
}

// runsVersion reports whether pid is version v of the component. The marker is
// in the command line before and after the shell execs into sleep, so a reading
// taken either side of the exec counts.
func runsVersion(pid, v int) bool {
	return strings.HasSuffix(cmdlineOf(pid), fmt.Sprintf("sleep 300%d", v))
}

// wantVersion fails unless pid is, or within a few seconds becomes, version v.
func wantVersion(t *testing.T, pid, v int, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !runsVersion(pid, v) {
		if time.Now().After(deadline) {
			t.Fatalf("%s runs %q, want v%d (sleep 300%d)", what, cmdlineOf(pid), v, v)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// applyV1 starts an agent in a fresh directory running v1, the way the HTTP
// API does it, and returns it with the PID of the component.
func applyV1(t *testing.T) (*Agent, int, string, string) {
	t.Helper()
	chdir(t, t.TempDir())
	v1, v2 := appPlan(writeAppRecipe(t, 1)), appPlan(writeAppRecipe(t, 2))
	a := New(Options{InsecureSkipVerify: true})
	if err := a.ApplyPlanContent(v1, false); err != nil {
		t.Fatalf("apply v1: %v", err)
	}
	pid := waitForPID(t, a, "app")
	t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })
	wantVersion(t, pid, 1, "after applying v1, the component")
	return a, pid, v1, v2
}

// TestADryRunChangesNothing: the plan in force, its path, its status and the
// snapshot are exactly what they were, and the next real apply of the
// previewed plan does what it says. It used to find the preview already
// recorded as the plan in force, compare the plan with itself, and leave v1
// running while reporting v2.
func TestADryRunChangesNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("starts real processes")
	}
	a, pid, _, v2 := applyV1(t)
	t.Cleanup(func() { _ = a.Close() })

	type planState struct {
		Path, Status string
		Comps        []state.PlanComponent
	}
	inMemory := func() planState {
		a.mu.RLock()
		defer a.mu.RUnlock()
		return planState{a.planPath, a.planStatus, append([]state.PlanComponent(nil), a.planComps...)}
	}
	onDisk := func() planState {
		snap, err := state.Load(filepath.Join("runtime", "state"))
		if err != nil {
			t.Fatalf("load snapshot: %v", err)
		}
		return planState{snap.Plan.Path, snap.Plan.Status, snap.PlanComponents}
	}
	memBefore, diskBefore := inMemory(), onDisk()

	if err := a.ApplyPlanContent(v2, true); err != nil {
		t.Fatalf("dry run of v2: %v", err)
	}
	if got := inMemory(); !reflect.DeepEqual(got, memBefore) {
		t.Errorf("the dry run changed the plan in force:\n got %+v\nwant %+v", got, memBefore)
	}
	if got := onDisk(); !reflect.DeepEqual(got, diskBefore) {
		t.Errorf("the dry run changed the persisted plan:\n got %+v\nwant %+v", got, diskBefore)
	}
	if got := pidOf(t, a, "app"); got != pid {
		t.Errorf("the dry run restarted the component: %d -> %d", pid, got)
	}

	if err := a.ApplyPlanContent(v2, false); err != nil {
		t.Fatalf("apply v2: %v", err)
	}
	var newPID int
	waitUntil(t, 15*time.Second, func() bool {
		ci, ok := a.comps.Get("app")
		newPID = ci.PID
		return ok && ci.State == "running" && newPID != pid && runsVersion(newPID, 2)
	}, "after the dry run, applying v2 for real did not start v2")
}

// TestARestartedAgentResumesThePlanInForceAfterADryRun: an agent restarted
// after a preview — an upgrade, a power cut — has to bring back what was
// running. A dry run used to leave the status "dry-run", which a starting agent
// reads as "nothing was ever installed": it resumed nothing. Previewing the
// plan already in force was enough.
func TestARestartedAgentResumesThePlanInForceAfterADryRun(t *testing.T) {
	if testing.Short() {
		t.Skip("starts real processes")
	}
	for _, preview := range []string{"another plan", "the plan in force"} {
		t.Run(preview, func(t *testing.T) {
			a, _, v1, v2 := applyV1(t)
			dry := v2
			if preview == "the plan in force" {
				dry = v1
			}
			if err := a.ApplyPlanContent(dry, true); err != nil {
				t.Fatalf("dry run: %v", err)
			}
			if err := a.Close(); err != nil {
				t.Fatalf("close: %v", err)
			}

			b := New(Options{InsecureSkipVerify: true})
			t.Cleanup(func() { _ = b.Close() })
			if !b.ResumesPlan() {
				t.Fatal("the restarted agent does not resume the plan in force")
			}
			pid := waitForPID(t, b, "app")
			t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })
			wantVersion(t, pid, 1, "after the restart, the component")
		})
	}
}

// TestRestartingAComponentAfterADryRunUsesThePlanInForce: a restart reloads the
// plan from its path. A dry run over HTTP used to leave that path pointing at
// the staging file it had just deleted, so the restart stopped the component
// and failed to start it again.
func TestRestartingAComponentAfterADryRunUsesThePlanInForce(t *testing.T) {
	if testing.Short() {
		t.Skip("starts real processes")
	}
	a, pid, _, v2 := applyV1(t)
	t.Cleanup(func() { _ = a.Close() })
	if err := a.ApplyPlanContent(v2, true); err != nil {
		t.Fatalf("dry run of v2: %v", err)
	}

	if _, err := a.RestartComponent("app", "pid", 15*time.Second); err != nil {
		t.Fatalf("restart after a dry run: %v", err)
	}
	newPID := waitForPID(t, a, "app")
	t.Cleanup(func() { _ = syscall.Kill(-newPID, syscall.SIGKILL) })
	if newPID == pid {
		t.Errorf("the component was not restarted (pid %d)", pid)
	}
	wantVersion(t, newPID, 1, "the restarted component")
}

// TestAReconcileAfterADryRunStillRuns: a reconcile skips a plan whose status
// says an operator stopped it, and "dry-run" was read that way, so a preview
// switched periodic reconcile off until the next real apply.
func TestAReconcileAfterADryRunStillRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("starts real processes")
	}
	a, _, v1, _ := applyV1(t)
	t.Cleanup(func() { _ = a.Close() })
	if err := a.ApplyPlanContent(v1, true); err != nil {
		t.Fatalf("dry run of the plan in force: %v", err)
	}

	res, err := a.ReconcileNow()
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if res.Skipped {
		t.Errorf("the reconcile was skipped after a dry run: %s", res.Reason)
	}
}
