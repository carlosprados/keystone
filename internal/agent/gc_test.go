package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/carlosprados/keystone/internal/artifact"
	"github.com/carlosprados/keystone/internal/deploy"
)

// TestGCKeepsTheCacheOfTheDatasetsInUse: after an apply, the GC removes every
// two-level directory under runtime/artifacts that the plan does not use. A
// dataset's cache is one of those, and it used to go on every apply, so the
// next refresh downloaded its payload again. The cache of a dataset no recipe
// in the plan declares still goes: that is what keeps the cache bounded.
func TestGCKeepsTheCacheOfTheDatasetsInUse(t *testing.T) {
	chdir(t, t.TempDir())
	writeFile(t, "app.recipe.toml", `
[metadata]
name = "com.test.app"
version = "1.0.0"

[[datasets]]
name     = "oui"
manifest = "https://hub.example/oui.manifest.toml"

[lifecycle.run]
type = "process"

[lifecycle.run.exec]
command = "/bin/true"
`)
	root := filepath.Join("runtime", "artifacts")
	for _, dir := range []string{"com.test.app/1.0.0", "com.test.app/0.9.0", "datasets/oui", "datasets/gone"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(root, dir, "payload"), "x")
	}

	keep := artifactsInUse([]deploy.Component{{Name: "app", RecipePath: "app.recipe.toml"}})
	if err := artifact.GC(root, keep); err != nil {
		t.Fatalf("GC: %v", err)
	}

	for dir, want := range map[string]bool{
		"com.test.app/1.0.0": true,
		"datasets/oui":       true,
		"com.test.app/0.9.0": false,
		"datasets/gone":      false,
	} {
		_, err := os.Stat(filepath.Join(root, dir, "payload"))
		if got := err == nil; got != want {
			t.Errorf("%s kept=%v, want %v", dir, got, want)
		}
	}
}
