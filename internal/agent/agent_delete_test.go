package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/carlosprados/keystone/internal/state"
	"github.com/carlosprados/keystone/internal/store"
)

func TestMirrorRecipeToStore(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "mirror-recipe-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	storeDir := filepath.Join(tempDir, "recipes")
	a := &Agent{recipes: store.NewRecipeStore(storeDir)}

	src := filepath.Join(tempDir, "foo.recipe.toml")
	original := []byte("# recipe v1\n[metadata]\nname=\"foo\"\nversion=\"1.0.0\"\n")
	if err := os.WriteFile(src, original, 0644); err != nil {
		t.Fatal(err)
	}

	// 1. Happy path: store gets the content.
	a.mirrorRecipeToStore("compA", "foo", "1.0.0", src)
	got, err := os.ReadFile(filepath.Join(storeDir, "foo-1.0.0.toml"))
	if err != nil {
		t.Fatalf("expected recipe written to store: %v", err)
	}
	if string(got) != string(original) {
		t.Errorf("store content mismatch: got %q want %q", got, original)
	}

	// 2. Re-mirror same name:version with edited content overwrites (force=true).
	edited := []byte("# recipe v1 edited\n[metadata]\nname=\"foo\"\nversion=\"1.0.0\"\n")
	if err := os.WriteFile(src, edited, 0644); err != nil {
		t.Fatal(err)
	}
	a.mirrorRecipeToStore("compA", "foo", "1.0.0", src)
	got, err = os.ReadFile(filepath.Join(storeDir, "foo-1.0.0.toml"))
	if err != nil {
		t.Fatalf("expected recipe still present after re-mirror: %v", err)
	}
	if string(got) != string(edited) {
		t.Errorf("expected store to reflect edited content, got %q", got)
	}

	// 3. Source path missing: must not panic, must not abort caller.
	a.mirrorRecipeToStore("compB", "missing", "0.0.1", filepath.Join(tempDir, "does-not-exist.toml"))
	if _, err := os.Stat(filepath.Join(storeDir, "missing-0.0.1.toml")); !os.IsNotExist(err) {
		t.Errorf("store should not contain a recipe whose source was unreadable, stat err=%v", err)
	}
}

func TestAgentDeleteRecipeSafety(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "agent-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	// Mock Agent
	a := &Agent{
		recipes: store.NewRecipeStore(filepath.Join(tempDir, "recipes")),
		planComps: []state.PlanComponent{
			{
				Name:       "comp1",
				RecipePath: "recipe1:1.0.0",
			},
		},
	}

	// 1. Setup recipes
	os.MkdirAll(filepath.Join(tempDir, "recipes"), 0755)
	os.WriteFile(filepath.Join(tempDir, "recipes", "recipe1-1.0.0.toml"), []byte("recipe1"), 0644)
	os.WriteFile(filepath.Join(tempDir, "recipes", "recipe2-1.0.0.toml"), []byte("recipe2"), 0644)

	// 2. Try delete in-use recipe
	if err := a.DeleteRecipe("recipe1", "1.0.0"); err == nil {
		t.Error("expected error deleting in-use recipe, got nil")
	}

	// 3. Try delete unused recipe
	if err := a.DeleteRecipe("recipe2", "1.0.0"); err != nil {
		t.Fatalf("failed to delete unused recipe: %v", err)
	}

	// 4. Verify gone
	if _, err := os.Stat(filepath.Join(tempDir, "recipes", "recipe2-1.0.0.toml")); err == nil {
		t.Error("unused recipe still exists after deletion")
	}
}
