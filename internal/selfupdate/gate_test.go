package selfupdate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gateScript locates the shipped gate. The tests run the real script rather
// than a reimplementation: the script IS the rollback logic, and a Go copy of
// it would be the thing that stays correct while the shipped one drifts.
func gateScript(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", "configs", "systemd", "keystone-update-gate.sh"))
	if err != nil {
		t.Fatalf("resolve gate path: %v", err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("gate script not found: %v", err)
	}
	return p
}

// runGate executes the gate against a layout, as systemd would.
func runGate(t *testing.T, l Layout, maxBoots string) string {
	t.Helper()
	cmd := exec.Command("/bin/sh", gateScript(t))
	cmd.Env = append(os.Environ(),
		"KEYSTONE_ROOT="+l.Root,
		"KEYSTONE_UPDATE_MAX_BOOTS="+maxBoots,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("gate failed: %v\n%s", err, out)
	}
	return string(out)
}

// layoutWithVersions builds a layout with the given versions installed.
func layoutWithVersions(t *testing.T, versions ...string) Layout {
	t.Helper()
	l := Layout{Root: t.TempDir()}
	for _, v := range versions {
		if err := l.Install(thisBinary(t), v); err != nil {
			t.Fatalf("install %s: %v", v, err)
		}
	}
	return l
}

// TestGateIsSilentWithNoUpdate: the overwhelmingly common case. Anything the
// gate does on an ordinary boot is a new way for an ordinary boot to fail.
func TestGateIsSilentWithNoUpdate(t *testing.T) {
	l := layoutWithVersions(t, "v1")
	if err := l.Activate("v1"); err != nil {
		t.Fatalf("activate: %v", err)
	}

	out := runGate(t, l, "3")
	if out != "" {
		t.Errorf("gate said something on an ordinary boot: %q", out)
	}
	if cur, _ := l.Current(); cur != "v1" {
		t.Errorf("current changed to %q on an ordinary boot", cur)
	}
}

// TestGateCountsAttempts: each start of an unconfirmed version burns one.
func TestGateCountsAttempts(t *testing.T) {
	l := layoutWithVersions(t, "v1", "v2")
	if err := l.Activate("v2"); err != nil {
		t.Fatalf("activate: %v", err)
	}
	if err := l.SaveState(UpdateState{Pending: "v2", Confirmed: "v1"}); err != nil {
		t.Fatalf("save state: %v", err)
	}

	for want := 1; want <= 2; want++ {
		runGate(t, l, "3")
		st, err := l.LoadState()
		if err != nil {
			t.Fatalf("load state: %v", err)
		}
		if st.Boots != want {
			t.Fatalf("boots = %d, want %d", st.Boots, want)
		}
		if st.Pending != "v2" {
			t.Fatalf("pending = %q, want v2", st.Pending)
		}
		if cur, _ := l.Current(); cur != "v2" {
			t.Fatalf("current = %q; the gate rolled back too early", cur)
		}
	}
}

// TestGateRollsBackAfterMaxBoots is the case the whole design exists for: a
// version that never confirms, on a device nobody can reach.
func TestGateRollsBackAfterMaxBoots(t *testing.T) {
	l := layoutWithVersions(t, "v1", "v2")
	if err := l.Activate("v2"); err != nil {
		t.Fatalf("activate: %v", err)
	}
	if err := l.SaveState(UpdateState{Pending: "v2", Boots: 3, Confirmed: "v1"}); err != nil {
		t.Fatalf("save state: %v", err)
	}

	out := runGate(t, l, "3")

	if cur, _ := l.Current(); cur != "v1" {
		t.Fatalf("current = %q, want v1: the rollback did not happen", cur)
	}
	if !strings.Contains(out, "rolling back") {
		t.Errorf("the rollback was silent: %q", out)
	}

	st, _ := l.LoadState()
	if st.Pending != "" {
		t.Errorf("pending = %q after a rollback, want empty", st.Pending)
	}
	if st.Confirmed != "v1" {
		t.Errorf("confirmed = %q, want v1", st.Confirmed)
	}
	if !strings.Contains(st.LastFailure, "v2") {
		t.Errorf("last failure does not name the version that failed: %q", st.LastFailure)
	}
}

// TestGateWithNothingToRollBackTo: refusing to loop forever matters more than
// insisting on a rollback that cannot happen.
func TestGateWithNothingToRollBackTo(t *testing.T) {
	l := layoutWithVersions(t, "v2")
	if err := l.Activate("v2"); err != nil {
		t.Fatalf("activate: %v", err)
	}
	if err := l.SaveState(UpdateState{Pending: "v2", Boots: 5}); err != nil {
		t.Fatalf("save state: %v", err)
	}

	out := runGate(t, l, "3")

	if !strings.Contains(out, "no confirmed version") {
		t.Errorf("the situation was not reported: %q", out)
	}
	st, _ := l.LoadState()
	if st.Pending != "" || st.Boots != 0 {
		t.Errorf("the counter was left growing: pending=%q boots=%d", st.Pending, st.Boots)
	}
	if cur, _ := l.Current(); cur != "v2" {
		t.Errorf("current = %q; with nowhere to go it must be left alone", cur)
	}
}

// TestGateSurvivesAGarbageCounter: the state file is on a device that loses
// power. A truncated or nonsense counter must not stop the gate working.
func TestGateSurvivesAGarbageCounter(t *testing.T) {
	l := layoutWithVersions(t, "v1", "v2")
	if err := l.Activate("v2"); err != nil {
		t.Fatalf("activate: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(l.StatePath()), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	junk := "KEYSTONE_UPDATE_PENDING=v2\nKEYSTONE_UPDATE_BOOTS=not-a-number\nKEYSTONE_UPDATE_CONFIRMED=v1\n"
	if err := os.WriteFile(l.StatePath(), []byte(junk), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	runGate(t, l, "3")

	st, err := l.LoadState()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if st.Boots != 1 {
		t.Errorf("boots = %d, want the counter restarted at 1", st.Boots)
	}
}

// TestGateDoesNotEvaluateTheStateFile: the file is written by a process that
// could be compromised, and this script runs as root before the agent starts.
// Sourcing it would be arbitrary code execution.
func TestGateDoesNotEvaluateTheStateFile(t *testing.T) {
	l := layoutWithVersions(t, "v1", "v2")
	if err := l.Activate("v2"); err != nil {
		t.Fatalf("activate: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(l.StatePath()), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	canary := filepath.Join(t.TempDir(), "executed")
	payload := "KEYSTONE_UPDATE_PENDING=v2\nKEYSTONE_UPDATE_CONFIRMED=v1\nKEYSTONE_UPDATE_BOOTS=$(touch " + canary + ")\n"
	if err := os.WriteFile(l.StatePath(), []byte(payload), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	runGate(t, l, "3")

	if _, err := os.Stat(canary); err == nil {
		t.Fatal("the gate evaluated the state file: command substitution in it ran")
	}
}
