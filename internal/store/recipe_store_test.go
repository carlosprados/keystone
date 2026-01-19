package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRecipeStoreDelete(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "recipe-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	s := NewRecipeStore(tempDir)
	name, version := "test", "1.0.0"
	content := "test recipe"

	// 1. Save
	if err := s.Save(name, version, content, false); err != nil {
		t.Fatalf("failed to save: %v", err)
	}

	// 2. Delete
	if err := s.Delete(name, version); err != nil {
		t.Fatalf("failed to delete: %v", err)
	}

	// 3. Verify gone
	path := filepath.Join(tempDir, "test-1.0.0.toml")
	if _, err := os.Stat(path); err == nil {
		t.Error("file still exists after deletion")
	}

	// 4. Delete again should fail
	if err := s.Delete(name, version); err == nil {
		t.Error("expected error deleting non-existent recipe, got nil")
	}
}
