package dataset

import (
	"os"
	"path/filepath"
	"testing"
)

func TestActivateIsAtomicAndRelative(t *testing.T) {
	s := NewStore(t.TempDir())

	first, err := s.Prepare("cve", "2026-08-14")
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(first, "data.txt"), "yesterday")
	if err := s.Activate("cve", "2026-08-14"); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if got := s.Active("cve"); got != "2026-08-14" {
		t.Fatalf("Active()=%q", got)
	}
	if got := read(t, filepath.Join(s.Path("cve"), "data.txt")); got != "yesterday" {
		t.Errorf("through the symlink: %q", got)
	}

	// A relative link so the tree can be moved or bind-mounted.
	target, err := os.Readlink(s.Path("cve"))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.IsAbs(target) {
		t.Errorf("symlink target %q is absolute", target)
	}

	second, err := s.Prepare("cve", "2026-08-15")
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(second, "data.txt"), "today")
	if err := s.Activate("cve", "2026-08-15"); err != nil {
		t.Fatalf("re-Activate over an existing symlink: %v", err)
	}
	if got := read(t, filepath.Join(s.Path("cve"), "data.txt")); got != "today" {
		t.Errorf("after the swap: %q", got)
	}
	// Rolling back is the same operation in reverse, and the old version is
	// still there to roll back to.
	if err := s.Activate("cve", "2026-08-14"); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if got := read(t, filepath.Join(s.Path("cve"), "data.txt")); got != "yesterday" {
		t.Errorf("after rollback: %q", got)
	}
}

func TestActivateRefusesAVersionThatIsNotThere(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Activate("cve", "2026-08-15"); err == nil {
		t.Error("activated a version with no directory; the symlink would dangle")
	}
}

// A half-extracted directory from a killed run must not be mistaken for a
// version: extracting over it would produce a mixture of two.
func TestPrepareClearsAPartialAttempt(t *testing.T) {
	s := NewStore(t.TempDir())
	dir, err := s.Prepare("cve", "2026-08-15")
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "half-written.txt"), "truncated")

	dir, err = s.Prepare("cve", "2026-08-15")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("Prepare left %d file(s) from the previous attempt", len(entries))
	}
}

// Retention must never delete what is in use, and must never delete the
// rollback target — after a rollback the newest directory is the failed one.
func TestPruneKeepsTheActiveAndProtectedVersions(t *testing.T) {
	s := NewStore(t.TempDir())
	for _, v := range []string{"2026-08-11", "2026-08-12", "2026-08-13", "2026-08-14", "2026-08-15"} {
		if _, err := s.Prepare("cve", v); err != nil {
			t.Fatal(err)
		}
	}
	// Rolled back: the active version is not the newest one.
	if err := s.Activate("cve", "2026-08-13"); err != nil {
		t.Fatal(err)
	}

	if err := s.Prune("cve", 2, "2026-08-12"); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	left := map[string]bool{}
	for _, v := range s.Versions("cve") {
		left[v] = true
	}
	if !left["2026-08-13"] {
		t.Error("Prune deleted the active version; the symlink would dangle")
	}
	if !left["2026-08-12"] {
		t.Error("Prune deleted a protected version")
	}
	if got := s.Active("cve"); got != "2026-08-13" {
		t.Errorf("Active()=%q after pruning", got)
	}
}

func TestPruneKeepsTheNewest(t *testing.T) {
	s := NewStore(t.TempDir())
	for _, v := range []string{"2026-08-11", "2026-08-12", "2026-08-13"} {
		if _, err := s.Prepare("oui", v); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Prune("oui", 2); err != nil {
		t.Fatal(err)
	}
	got := s.Versions("oui")
	if len(got) != 2 || got[0] != "2026-08-12" || got[1] != "2026-08-13" {
		t.Errorf("kept %v, want the two newest", got)
	}
}

func TestActiveIsEmptyWhenNothingIsActivated(t *testing.T) {
	s := NewStore(t.TempDir())
	if got := s.Active("cve"); got != "" {
		t.Errorf("Active()=%q on an empty store", got)
	}
}

func TestEnvName(t *testing.T) {
	cases := map[string]string{
		"oui":         "KEYSTONE_DATASET_OUI",
		"cve-bundle":  "KEYSTONE_DATASET_CVE_BUNDLE",
		"vendor.list": "KEYSTONE_DATASET_VENDOR_LIST",
		"a1":          "KEYSTONE_DATASET_A1",
	}
	for in, want := range cases {
		if got := EnvName(in); got != want {
			t.Errorf("EnvName(%q)=%q, want %q", in, got, want)
		}
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The path a component is handed must be absolute: it runs with its own working
// directory, and a relative path there resolves to nothing — which looks
// exactly like an empty dataset rather than a misconfiguration.
func TestPathsAreAbsolute(t *testing.T) {
	s := NewStore("runtime/datasets")
	if !filepath.IsAbs(s.Path("oui")) {
		t.Errorf("Path()=%q is relative", s.Path("oui"))
	}
	if !filepath.IsAbs(s.VersionDir("oui", "2026-08-15")) {
		t.Errorf("VersionDir()=%q is relative", s.VersionDir("oui", "2026-08-15"))
	}
}
