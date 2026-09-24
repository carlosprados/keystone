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

// TestStageSelfUpdateInstallsBeside is the property the design depends on:
// nothing is stopped and nothing is overwritten. The running version stays
// exactly where it was, and the new one is only reached after a restart.
func TestStageSelfUpdateInstallsBeside(t *testing.T) {
	root := t.TempDir()
	l := selfupdate.Layout{Root: root}
	if err := l.Install(mustExecutable(t), "v1"); err != nil {
		t.Fatalf("seed v1: %v", err)
	}
	if err := l.Activate("v1"); err != nil {
		t.Fatalf("activate v1: %v", err)
	}

	url, sum := binaryServer(t)
	a := selfUpdateAgent(t, root)

	if err := a.StageSelfUpdate(context.Background(), adapter.SelfUpdateSpec{
		Version: "v2", URI: url, SHA256: sum,
	}); err != nil {
		t.Fatalf("stage: %v", err)
	}

	installed, _ := l.Installed()
	if len(installed) != 2 {
		t.Fatalf("installed = %v, want both versions side by side", installed)
	}
	if _, err := os.Stat(l.BinaryPath("v1")); err != nil {
		t.Errorf("the running version was disturbed: %v", err)
	}

	cur, _ := l.Current()
	if cur != "v2" {
		t.Errorf("current = %q, want v2 for the next restart", cur)
	}

	st, _ := l.LoadState()
	if st.Pending != "v2" {
		t.Errorf("pending = %q, want v2", st.Pending)
	}
	if st.Boots != 0 {
		t.Errorf("boots = %d, want the trial to start at zero", st.Boots)
	}
	if st.Confirmed != "v1" {
		t.Errorf("confirmed = %q, want the version that was running", st.Confirmed)
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
