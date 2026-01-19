package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/carlosprados/keystone/internal/state"
	"github.com/carlosprados/keystone/internal/store"
)

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
