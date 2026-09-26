package selfupdate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The gate verifies a proposal by running the INSTALLED version with
// --verify-update. These tests install a stand-in verifier as that version, so
// what is under test is the gate's logic — copy, verify the copy, refuse or
// install, never fail the start — not the cryptography, which has its own tests.
//
// The stand-in accepts a proposal whose signature file says "good", refuses any
// other, and exits 2 on anything but --verify-update, as a version from before
// the mode existed does on an unknown flag.
const standInVerifier = `#!/bin/sh
[ "$1" = --verify-update ] || exit 2
grep -q good "$2/keystone.sig" 2>/dev/null
`

func gateLayout(t *testing.T, verifier string) Layout {
	t.Helper()
	l := Layout{Root: t.TempDir()}
	if err := os.MkdirAll(l.VersionDir("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(l.BinaryPath("v1"), []byte(verifier), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("versions", "v1"), l.CurrentLink()); err != nil {
		t.Fatal(err)
	}
	if err := l.SaveState(UpdateState{Confirmed: "v1"}); err != nil {
		t.Fatal(err)
	}
	return l
}

// stage leaves a proposal as the agent would, and marks it proposed.
func stage(t *testing.T, l Layout, v, binary, sig string) {
	t.Helper()
	dir := l.StagedDir(v)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{BinaryName: binary, StagedSig: sig, StagedCert: "cert"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Propose(v); err != nil {
		t.Fatal(err)
	}
}

// TestGateInstallsAVerifiedProposal is the path the agent could never complete
// on its own: under the A/B unit it cannot write versions/ or move current.
func TestGateInstallsAVerifiedProposal(t *testing.T) {
	l := gateLayout(t, standInVerifier)
	stage(t, l, "v2", "new binary", "good")

	out := runGate(t, l, "3")

	if cur, _ := l.Current(); cur != "v2" {
		t.Fatalf("current = %q, want v2\n%s", cur, out)
	}
	if b, _ := os.ReadFile(l.BinaryPath("v2")); string(b) != "new binary" {
		t.Errorf("installed binary = %q, want the staged one", b)
	}
	st, _ := l.LoadState()
	if st.Pending != "v2" || st.Boots != 1 || st.Confirmed != "v1" || st.Proposed != "" {
		t.Errorf("state = %+v, want v2 pending on its first start over v1, proposal cleared", st)
	}
	if _, err := os.Stat(l.StagedDir("v2")); !os.IsNotExist(err) {
		t.Errorf("staging/v2 left behind: %v", err)
	}
}

// TestGateRefusesAProposalThatDoesNotVerify is the boundary: a compromised agent
// can put anything in staging/, and it must not run.
func TestGateRefusesAProposalThatDoesNotVerify(t *testing.T) {
	l := gateLayout(t, standInVerifier)
	stage(t, l, "v2", "hostile binary", "forged")

	out := runGate(t, l, "3")

	if cur, _ := l.Current(); cur != "v1" {
		t.Fatalf("current = %q after a failed verification, want v1", cur)
	}
	if _, err := os.Stat(l.VersionDir("v2")); !os.IsNotExist(err) {
		t.Errorf("a proposal that failed verification was installed")
	}
	st, _ := l.LoadState()
	if st.Pending != "" || st.Proposed != "" || !strings.Contains(st.LastFailure, "refused") {
		t.Errorf("state = %+v, want nothing pending and the refusal recorded", st)
	}
	if !strings.Contains(out, "refusing proposed version 'v2'") {
		t.Errorf("gate did not say why: %q", out)
	}
}

// TestGateRefusesWhenTheInstalledVersionCannotVerify: a version from before
// --verify-update exits 2 on the unknown flag. That must refuse the proposal,
// never be read as success and never start an agent inside the gate.
func TestGateRefusesWhenTheInstalledVersionCannotVerify(t *testing.T) {
	l := gateLayout(t, "#!/bin/sh\nexit 2\n")
	stage(t, l, "v2", "new binary", "good")

	runGate(t, l, "3")

	if cur, _ := l.Current(); cur != "v1" {
		t.Fatalf("current = %q, want v1: the installed version could not verify", cur)
	}
}

// TestGateVerifiesTheCopyNotTheStagedFiles: after copying, the staged files may
// still be changed by a process the agent left behind. The copy, which only
// root can touch now, is what gets verified and installed.
func TestGateVerifiesTheCopyNotTheStagedFiles(t *testing.T) {
	verifier := `#!/bin/sh
[ "$1" = --verify-update ] || exit 2
case "$2" in */versions/.incoming-gate) ;; *) exit 1 ;; esac
grep -q good "$2/keystone.sig"
`
	l := gateLayout(t, verifier)
	stage(t, l, "v2", "new binary", "good")

	runGate(t, l, "3")

	if cur, _ := l.Current(); cur != "v2" {
		t.Fatalf("current = %q: the gate did not verify its own copy under versions/", cur)
	}
}

// TestGateRefusesNamesThatAreNotOnePathComponent: every name comes from a file
// the agent writes. "../../x" as the confirmed version would point current at
// a binary the agent chose.
func TestGateRefusesNamesThatAreNotOnePathComponent(t *testing.T) {
	t.Run("proposed", func(t *testing.T) {
		l := gateLayout(t, standInVerifier)
		writeRawState(t, l, "KEYSTONE_UPDATE_CONFIRMED=v1\nKEYSTONE_UPDATE_PROPOSED=../escape\n")
		runGate(t, l, "3")
		if cur, _ := l.Current(); cur != "v1" {
			t.Fatalf("current = %q", cur)
		}
	})
	t.Run("confirmed", func(t *testing.T) {
		l := gateLayout(t, standInVerifier)
		if err := os.MkdirAll(l.VersionDir("v2"), 0o755); err != nil {
			t.Fatal(err)
		}
		_ = os.Remove(l.CurrentLink())
		_ = os.Symlink(filepath.Join("versions", "v2"), l.CurrentLink())
		writeRawState(t, l, "KEYSTONE_UPDATE_PENDING=v2\nKEYSTONE_UPDATE_BOOTS=3\nKEYSTONE_UPDATE_CONFIRMED=../../tmp/evil\n")
		runGate(t, l, "3")
		target, _ := os.Readlink(l.CurrentLink())
		if strings.Contains(target, "..") {
			t.Fatalf("current points at %q: a traversal from the state file was followed", target)
		}
	})
}

// TestGateRetriesTheSameBinaryAndRefusesADifferentOne: a version that failed
// its trial stays in versions/. Retrying it is allowed with the same bytes; a
// different binary under the same name is not.
func TestGateRetriesTheSameBinaryAndRefusesADifferentOne(t *testing.T) {
	l := gateLayout(t, standInVerifier)
	if err := os.MkdirAll(l.VersionDir("v2"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(l.BinaryPath("v2"), []byte("same binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	stage(t, l, "v2", "other binary", "good")
	runGate(t, l, "3")
	if cur, _ := l.Current(); cur != "v1" {
		t.Fatalf("a different binary under an installed name was activated")
	}

	stage(t, l, "v2", "same binary", "good")
	runGate(t, l, "3")
	if cur, _ := l.Current(); cur != "v2" {
		t.Fatalf("retrying the same binary was refused")
	}
}

func writeRawState(t *testing.T, l Layout, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(l.StatePath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(l.StatePath(), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
