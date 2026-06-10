package deploy

import (
	"os"
	"path/filepath"
	"testing"
)

func writePlan(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "plan.toml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadPlanValid(t *testing.T) {
	p := writePlan(t, `
[[components]]
name = "svc"
recipe = "svc.recipe.toml"
`)
	plan, err := Load(p)
	if err != nil {
		t.Fatalf("valid plan rejected: %v", err)
	}
	if len(plan.Components) != 1 || plan.Components[0].Name != "svc" {
		t.Fatalf("unexpected plan: %+v", plan)
	}
}

func TestLoadPlanRejectsComponentMissingRecipe(t *testing.T) {
	p := writePlan(t, `
[[components]]
name = "svc"
`)
	if _, err := Load(p); err == nil {
		t.Fatal("plan with a component missing 'recipe' was accepted, want error")
	}
}
