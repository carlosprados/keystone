package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// runningNotZombie reports whether pid is a live process, not a zombie: a child of
// this test that was killed stays in the table until it is reaped.
func runningNotZombie(pid int) bool {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	return i >= 0 && i+2 < len(s) && s[i+2] != 'Z'
}

// TestCloseForRestartLeavesComponentsRunning: the restart a self-update makes
// must leave supervised components alive for the next start to adopt. The
// agent's context is cancelled on the way out, and a component started with
// exec.CommandContext under it is SIGKILLed by Go when that happens — the
// leader only, so a shell component dies and its children linger.
func TestCloseForRestartLeavesComponentsRunning(t *testing.T) {
	if testing.Short() {
		t.Skip("starts real processes")
	}

	dir := t.TempDir()
	chdir(t, dir)

	writeRecipe(t, "survivor", "survivor.recipe.toml")
	planPath := filepath.Join(dir, "plan.toml")
	writeFile(t, planPath, `
[[components]]
name = "survivor"
recipe = "survivor.recipe.toml"
`)

	a := New(Options{InsecureSkipVerify: true})
	if err := a.ApplyPlan(planPath, false); err != nil {
		t.Fatalf("apply: %v", err)
	}
	pid := waitForPID(t, a, "survivor")
	t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })

	if err := a.CloseForRestart(); err != nil {
		t.Fatalf("close for restart: %v", err)
	}
	time.Sleep(time.Second)
	if !runningNotZombie(pid) {
		t.Fatalf("component %d was killed by CloseForRestart; the next start has nothing to adopt", pid)
	}
}
