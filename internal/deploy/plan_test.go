package deploy

import (
	"os"
	"path/filepath"
	"strings"
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

// A plan is two keys per component, so a misspelling is total: `recipie` leaves
// the path empty and the component resolves to nothing. Loading still succeeds,
// for the same fleet reason recipes tolerate unknown keys, but the plan carries
// what it did not understand so the dry run can refuse it.
func TestLoadPlanReportsUnknownFields(t *testing.T) {
	p := writePlan(t, `
[[components]]
name = "svc"
recipe = "svc.recipe.toml"
recipie = "typo.recipe.toml"
`)
	plan, err := Load(p)
	if err != nil {
		t.Fatalf("plan with an unknown field was rejected: %v", err)
	}
	if len(plan.UnknownFields) != 1 {
		t.Fatalf("UnknownFields = %v, want exactly one entry", plan.UnknownFields)
	}
	if !strings.Contains(plan.UnknownFields[0], "recipie") {
		t.Errorf("report %q does not name the offending key", plan.UnknownFields[0])
	}
	if plan.Components[0].RecipePath != "svc.recipe.toml" {
		t.Errorf("RecipePath = %q, the real key must still win", plan.Components[0].RecipePath)
	}
}
