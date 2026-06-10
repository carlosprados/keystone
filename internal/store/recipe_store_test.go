package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRecipeStoreRejectsTraversal(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "recipe-traversal-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	s := NewRecipeStore(tempDir)

	// A malicious recipe name must not be allowed to escape the store dir.
	if err := s.Save("../../escape", "1.0.0", "pwned", true); err == nil {
		t.Fatal("Save with traversal name succeeded, want error")
	}
	if err := s.Save("ok", "../../1.0.0", "pwned", true); err == nil {
		t.Fatal("Save with traversal version succeeded, want error")
	}

	// Confirm nothing was written outside the store directory.
	if _, err := os.Stat(filepath.Join(tempDir, "..", "..", "escape-1.0.0.toml")); err == nil {
		t.Fatal("file escaped the store directory")
	}

	// GetPath and Delete must reject traversal too.
	if _, err := s.GetPath("../x", "1.0.0"); err == nil {
		t.Error("GetPath with traversal succeeded, want error")
	}
	if err := s.Delete("../x", "1.0.0"); err == nil {
		t.Error("Delete with traversal succeeded, want error")
	}
}

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
