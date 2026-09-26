package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carlosprados/keystone/internal/adapter"
	"github.com/carlosprados/keystone/internal/selfupdate"
)

// binaryServer serves a real ELF (the test binary) so the architecture check
// and the digest are exercised against something genuine.
func binaryServer(t *testing.T) (url string, sum string) {
	t.Helper()

	self, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test binary: %v", err)
	}
	b, err := os.ReadFile(self)
	if err != nil {
		t.Fatalf("read test binary: %v", err)
	}
	h := sha256.Sum256(b)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/keystone", hex.EncodeToString(h[:])
}

func selfUpdateAgent(t *testing.T, root string) *Agent {
	t.Helper()
	return New(Options{InsecureSkipVerify: true, SelfUpdateRoot: root})
}

// TestStageSelfUpdateProposesAndInstallsNothing is the division the A/B unit
// enforces: the agent may stage and propose, and only the pre-start gate, as
// root, installs. The agent used to install into versions/ and move current
// itself, which under the unit's read-only /opt/keystone failed on every device.
func TestStageSelfUpdateProposesAndInstallsNothing(t *testing.T) {
	root := t.TempDir()
	l := selfupdate.Layout{Root: root}
	if err := l.Install(mustExecutable(t), "v1"); err != nil {
		t.Fatalf("seed v1: %v", err)
	}
	if err := l.Activate("v1"); err != nil {
		t.Fatalf("activate v1: %v", err)
	}
	if err := l.SaveState(selfupdate.UpdateState{Confirmed: "v1"}); err != nil {
		t.Fatal(err)
	}

	url, sum := binaryServer(t)
	a := selfUpdateAgent(t, root)
	if err := a.StageSelfUpdate(context.Background(), adapter.SelfUpdateSpec{
		Version: "v2", URI: url, SHA256: sum,
	}); err != nil {
		t.Fatalf("stage: %v", err)
	}

	if installed, _ := l.Installed(); len(installed) != 1 {
		t.Errorf("installed = %v: the agent must not install", installed)
	}
	if cur, _ := l.Current(); cur != "v1" {
		t.Errorf("current = %q: the agent must not move it", cur)
	}
	if _, err := os.Stat(filepath.Join(l.StagedDir("v2"), selfupdate.BinaryName)); err != nil {
		t.Errorf("nothing staged for the gate: %v", err)
	}
	st, _ := l.LoadState()
	if st.Proposed != "v2" || st.Pending != "" || st.Confirmed != "v1" {
		t.Errorf("state = %+v, want v2 proposed and nothing else changed", st)
	}
}

// TestStageSelfUpdateUnderAReadOnlyInstallRoot reproduces the unit: everything
// under the install root is read-only to the agent except staging/ and state/.
// This is what failed on hardware with "read-only file system".
func TestStageSelfUpdateUnderAReadOnlyInstallRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	root := t.TempDir()
	l := selfupdate.Layout{Root: root}
	if err := l.Install(mustExecutable(t), "v1"); err != nil {
		t.Fatalf("seed v1: %v", err)
	}
	if err := l.Activate("v1"); err != nil {
		t.Fatalf("activate v1: %v", err)
	}
	if err := l.SaveState(selfupdate.UpdateState{Confirmed: "v1"}); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{l.VersionsDir(), root} {
		if err := os.Chmod(d, 0o555); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o755); _ = os.Chmod(l.VersionsDir(), 0o755) })

	url, sum := binaryServer(t)
	a := selfUpdateAgent(t, root)
	if err := a.StageSelfUpdate(context.Background(), adapter.SelfUpdateSpec{
		Version: "v2", URI: url, SHA256: sum,
	}); err != nil {
		t.Fatalf("staging under a read-only install root failed: %v", err)
	}
}

// TestStageSelfUpdateRejectsABadDigest: an agent binary is the one artifact
// where "could not check it" must never mean "install it anyway".
func TestStageSelfUpdateRejectsABadDigest(t *testing.T) {
	root := t.TempDir()
	url, _ := binaryServer(t)
	a := selfUpdateAgent(t, root)

	err := a.StageSelfUpdate(context.Background(), adapter.SelfUpdateSpec{
		Version: "v2", URI: url,
		SHA256: strings.Repeat("0", 64),
	})
	if err == nil {
		t.Fatal("a binary with the wrong digest was installed")
	}

	l := selfupdate.Layout{Root: root}
	if vs, _ := l.Installed(); len(vs) != 0 {
		t.Errorf("a rejected update left %v behind", vs)
	}
	if st, _ := l.LoadState(); st.Pending != "" {
		t.Errorf("a rejected update was marked pending: %+v", st)
	}
}

// TestStageSelfUpdateRefusesTheRunningVersion guards against a controller that
// re-sends the same instruction: reinstalling what is running would start a
// trial of a version that is already confirmed.
func TestStageSelfUpdateRefusesTheRunningVersion(t *testing.T) {
	a := selfUpdateAgent(t, t.TempDir())

	err := a.StageSelfUpdate(context.Background(), adapter.SelfUpdateSpec{
		Version: "0.1.0-dev", URI: "http://example.invalid/x", SHA256: strings.Repeat("a", 64),
	})
	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("expected a refusal naming the running version, got %v", err)
	}
}

// TestStageSelfUpdateDisabled: every deployment upgraded from outside takes
// this path, and it must fail clearly rather than half-doing something.
func TestStageSelfUpdateDisabled(t *testing.T) {
	a := New(Options{InsecureSkipVerify: true})

	err := a.StageSelfUpdate(context.Background(), adapter.SelfUpdateSpec{Version: "v2", URI: "http://x", SHA256: "y"})
	if err == nil || !strings.Contains(err.Error(), "not enabled") {
		t.Fatalf("expected a clear refusal, got %v", err)
	}
}

// TestRequestRestartIsCoalesced: two controllers asking at once must not queue
// two restarts.
func TestRequestRestartIsCoalesced(t *testing.T) {
	a := New(Options{InsecureSkipVerify: true})

	a.RequestRestart("first")
	a.RequestRestart("second")

	select {
	case got := <-a.RestartRequests():
		if got != "first" {
			t.Errorf("reason = %q, want the first", got)
		}
	default:
		t.Fatal("no restart was queued")
	}

	select {
	case got := <-a.RestartRequests():
		t.Fatalf("a second restart was queued: %q", got)
	default:
	}
}

func mustExecutable(t *testing.T) string {
	t.Helper()
	p, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test binary: %v", err)
	}
	return p
}

var _ = filepath.Join

// TestAFailedVersionCanBeRetried: a version that failed its trial stays in
// versions/. Proposing it again once the cause is fixed must work with the same
// bytes, and be refused with different ones.
func TestAFailedVersionCanBeRetried(t *testing.T) {
	root := t.TempDir()
	l := selfupdate.Layout{Root: root}
	if err := l.Install(mustExecutable(t), "v1"); err != nil {
		t.Fatalf("seed v1: %v", err)
	}
	if err := l.Activate("v1"); err != nil {
		t.Fatalf("activate v1: %v", err)
	}
	url, sum := binaryServer(t)
	// What the gate leaves after v2 failed its trial: installed, not current.
	if err := l.Install(mustExecutable(t), "v2"); err != nil {
		t.Fatalf("seed v2: %v", err)
	}
	if err := l.SaveState(selfupdate.UpdateState{Confirmed: "v1", LastFailure: "v2 failed to confirm after 3 starts"}); err != nil {
		t.Fatal(err)
	}

	a := selfUpdateAgent(t, root)
	if err := a.StageSelfUpdate(context.Background(), adapter.SelfUpdateSpec{Version: "v2", URI: url, SHA256: sum}); err != nil {
		t.Fatalf("retrying the failed version was refused: %v", err)
	}
	if st, _ := l.LoadState(); st.Proposed != "v2" {
		t.Errorf("state = %+v, want v2 proposed again", st)
	}

	// The version in use is still refused.
	if err := a.StageSelfUpdate(context.Background(), adapter.SelfUpdateSpec{Version: "v1", URI: url, SHA256: sum}); err == nil || !strings.Contains(err.Error(), "in use") {
		t.Errorf("staging the confirmed version: got %v, want a refusal", err)
	}
}
