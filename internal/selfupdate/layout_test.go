package selfupdate

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// thisBinary is a real ELF for the machine running the tests: the test binary
// itself. Using it keeps the architecture check honest instead of stubbed.
func thisBinary(t *testing.T) string {
	t.Helper()
	p, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test binary: %v", err)
	}
	return p
}

func TestInstallAndActivate(t *testing.T) {
	l := Layout{Root: t.TempDir()}

	if err := l.Install(thisBinary(t), "v0.12.0"); err != nil {
		t.Fatalf("install: %v", err)
	}
	if err := l.Activate("v0.12.0"); err != nil {
		t.Fatalf("activate: %v", err)
	}

	cur, err := l.Current()
	if err != nil {
		t.Fatalf("current: %v", err)
	}
	if cur != "v0.12.0" {
		t.Errorf("current = %q, want v0.12.0", cur)
	}

	// The binary must be reachable through the symlink, which is the path
	// systemd will be given.
	if _, err := os.Stat(filepath.Join(l.CurrentLink(), BinaryName)); err != nil {
		t.Errorf("binary not reachable through current: %v", err)
	}
}

// TestActivateReplacesWithoutAGap is the property the whole design rests on: a
// restart landing at any moment must find something to start. os.Symlink cannot
// replace an existing link, and the naive remove-then-create leaves a window
// where `current` does not exist.
func TestActivateReplacesWithoutAGap(t *testing.T) {
	l := Layout{Root: t.TempDir()}
	for _, v := range []string{"v1", "v2"} {
		if err := l.Install(thisBinary(t), v); err != nil {
			t.Fatalf("install %s: %v", v, err)
		}
	}
	if err := l.Activate("v1"); err != nil {
		t.Fatalf("activate v1: %v", err)
	}

	// Hammer the link while it is being switched. Every observation must see a
	// usable version, never a missing link.
	var wg sync.WaitGroup
	stop := make(chan struct{})
	var misses int
	var mu sync.Mutex

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				if _, err := os.Stat(filepath.Join(l.CurrentLink(), BinaryName)); err != nil {
					mu.Lock()
					misses++
					mu.Unlock()
				}
			}
		}
	}()

	for i := 0; i < 50; i++ {
		v := "v1"
		if i%2 == 0 {
			v = "v2"
		}
		if err := l.Activate(v); err != nil {
			t.Fatalf("activate %s: %v", v, err)
		}
	}
	close(stop)
	wg.Wait()

	if misses > 0 {
		t.Fatalf("current was unusable %d times during activation; a restart in that window has nothing to start", misses)
	}
}

// TestInstallRefusesOverwrite: a version number that means different things on
// different devices destroys the only handle an operator has on a fleet.
func TestInstallRefusesOverwrite(t *testing.T) {
	l := Layout{Root: t.TempDir()}

	if err := l.Install(thisBinary(t), "v1"); err != nil {
		t.Fatalf("install: %v", err)
	}

	other := filepath.Join(t.TempDir(), "keystone")
	if err := os.WriteFile(other, []byte("a different binary"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	err := l.Install(other, "v1")
	if err == nil || !strings.Contains(err.Error(), "different contents") {
		t.Fatalf("expected a refusal to put different bytes under v1, got %v", err)
	}
}

// TestInstallRetriesTheSameBinary: a version that failed its trial stays on
// disk, and retrying it after fixing the cause — the broker, the network — must
// not need a new version number. The same bytes under the same name is not an
// overwrite.
func TestInstallRetriesTheSameBinary(t *testing.T) {
	l := Layout{Root: t.TempDir()}

	if err := l.Install(thisBinary(t), "v1"); err != nil {
		t.Fatalf("install: %v", err)
	}
	if err := l.Install(thisBinary(t), "v1"); err != nil {
		t.Fatalf("retrying the same binary was refused: %v", err)
	}
}

// TestInstallRejectsNonELF: a truncated download or an HTML error page saved as
// a binary must not become an installed version.
func TestInstallRejectsNonELF(t *testing.T) {
	l := Layout{Root: t.TempDir()}

	junk := filepath.Join(t.TempDir(), "keystone")
	if err := os.WriteFile(junk, []byte("<html>504 Gateway Timeout</html>"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := l.Install(junk, "v1"); err == nil {
		t.Fatal("a non-ELF file was installed as a version")
	}
	if vs, _ := l.Installed(); len(vs) != 0 {
		t.Errorf("a failed install left %v behind", vs)
	}
}

// TestVersionNameCannotEscapeTheLayout: the version is used as a directory name
// and as a symlink target, so a traversal in it would write outside the root.
func TestVersionNameCannotEscapeTheLayout(t *testing.T) {
	l := Layout{Root: t.TempDir()}

	for _, bad := range []string{"", "..", "../evil", "a/b", `a\b`, "v1:v2"} {
		if err := l.Install(thisBinary(t), bad); err == nil {
			t.Errorf("version %q was accepted", bad)
		}
		if err := l.Activate(bad); err == nil {
			t.Errorf("version %q was activated", bad)
		}
	}
}

// TestActivateRefusesAMissingVersion: pointing current at nothing is how a
// device ends up with no agent at all.
func TestActivateRefusesAMissingVersion(t *testing.T) {
	l := Layout{Root: t.TempDir()}
	if err := l.Prepare(); err != nil {
		t.Fatalf("prepare: %v", err)
	}

	if err := l.Activate("v-not-installed"); err == nil {
		t.Fatal("activated a version that is not installed")
	}
	if cur, _ := l.Current(); cur != "" {
		t.Errorf("current became %q after a failed activation", cur)
	}
}

// TestPruneKeepsWhatItIsTold: the two that matter are the running one and the
// one to fall back to.
func TestPruneKeepsWhatItIsTold(t *testing.T) {
	l := Layout{Root: t.TempDir()}
	for _, v := range []string{"v1", "v2", "v3", "v4"} {
		if err := l.Install(thisBinary(t), v); err != nil {
			t.Fatalf("install %s: %v", v, err)
		}
	}

	removed, err := l.Prune("v3", "v4")
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if len(removed) != 2 {
		t.Errorf("removed %v, want two", removed)
	}

	left, _ := l.Installed()
	if len(left) != 2 || left[0] != "v3" || left[1] != "v4" {
		t.Errorf("left %v, want [v3 v4]", left)
	}
}

// TestCurrentOnAFreshInstall: no symlink is not an error, it is an
// uninitialised layout.
func TestCurrentOnAFreshInstall(t *testing.T) {
	l := Layout{Root: t.TempDir()}

	cur, err := l.Current()
	if err != nil {
		t.Fatalf("current on an empty layout: %v", err)
	}
	if cur != "" {
		t.Errorf("current = %q, want empty", cur)
	}
}

// TestRunningVersionIsTheInstallDirectory: the pending marker says "v0.12.4"
// while a release binary reports "0.12.4". Confirmation compared those two, so
// no release could confirm and the gate reverted every good update. The
// directory the binary runs from is the name the gate uses.
func TestRunningVersionIsTheInstallDirectory(t *testing.T) {
	l := Layout{Root: t.TempDir()}
	if err := os.MkdirAll(l.VersionDir("v0.12.4"), 0o755); err != nil {
		t.Fatal(err)
	}
	bin := l.BinaryPath("v0.12.4")
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("versions", "v0.12.4"), l.CurrentLink()); err != nil {
		t.Fatal(err)
	}

	for name, exe := range map[string]string{
		"direct":          bin,
		"through current": filepath.Join(l.CurrentLink(), BinaryName),
	} {
		if v, ok := l.RunningVersion(exe); !ok || v != "v0.12.4" {
			t.Errorf("%s: RunningVersion = %q, %v; want v0.12.4", name, v, ok)
		}
	}

	outside := filepath.Join(t.TempDir(), BinaryName)
	if err := os.WriteFile(outside, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if v, ok := l.RunningVersion(outside); ok {
		t.Errorf("a binary outside the layout was named %q", v)
	}
}
