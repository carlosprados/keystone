package recipe

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestShippedExamplesParse loads every example recipe in the repository. An
// example that no longer parses is documentation that lies: these files are
// copied verbatim by anyone following the guides, and a stale field name here
// surfaces as an unknown-field error on someone else's machine.
func TestShippedExamplesParse(t *testing.T) {
	root := filepath.Join("..", "..", "configs", "examples")

	var found int
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".recipe.toml") {
			return err
		}
		found++
		t.Run(filepath.Base(path), func(t *testing.T) {
			r, err := Load(path)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if len(r.UnknownFields) > 0 {
				t.Fatalf("unknown fields: %v", r.UnknownFields)
			}
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if found == 0 {
		t.Fatalf("no example recipes found under %s", root)
	}
}
