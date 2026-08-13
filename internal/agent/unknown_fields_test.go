package agent

import (
	"strings"
	"testing"

	"github.com/carlosprados/keystone/internal/deploy"
	"github.com/carlosprados/keystone/internal/recipe"
	"github.com/carlosprados/keystone/internal/store"
)

// The agent tolerates a key it does not know, because a recipe is published to
// a fleet where some agents predate the field. These tests pin the other half
// of that bargain: what it tolerates, it has to name.

func TestUnknownFieldReportsNamesPlanAndRecipes(t *testing.T) {
	ps := &plannedState{
		path: "plan.toml",
		plan: &deploy.Plan{UnknownFields: []string{`"components.recipie" (line 3)`}},
		byName: map[string]*plannedComponent{
			// Deliberately out of alphabetical order in the map literal: the
			// report must not reshuffle between two runs over the same files.
			"web": {
				item: deploy.Component{Name: "web", RecipePath: "recipes/web.toml"},
				rec:  &recipe.Recipe{UnknownFields: []string{`"lifecycle.run.restart_polciy" (line 6)`}},
			},
			"api": {
				item: deploy.Component{Name: "api", RecipePath: "recipes/api.toml"},
				rec:  &recipe.Recipe{UnknownFields: []string{`"metadata.autor" (line 4)`}},
			},
			"db": {
				item: deploy.Component{Name: "db", RecipePath: "recipes/db.toml"},
				rec:  &recipe.Recipe{},
			},
		},
	}

	got := ps.unknownFieldReports()
	if len(got) != 3 {
		t.Fatalf("got %d reports, want 3: %v", len(got), got)
	}
	if !strings.HasPrefix(got[0], "plan plan.toml:") {
		t.Errorf("first report should be the plan's, got %q", got[0])
	}
	if !strings.Contains(got[1], "component api") || !strings.Contains(got[1], "recipes/api.toml") {
		t.Errorf("second report should name component api and its recipe, got %q", got[1])
	}
	if !strings.Contains(got[2], "component web") {
		t.Errorf("third report should name component web, got %q", got[2])
	}
	for _, r := range got {
		if strings.Contains(r, "component db") {
			t.Errorf("a component with nothing unknown must not be reported: %q", r)
		}
	}
}

// Running the same state twice must produce the same message. Map iteration
// order is randomised per run in Go, so an unsorted implementation passes once
// and fails later, in someone else's build.
func TestUnknownFieldReportsAreStable(t *testing.T) {
	build := func() *plannedState {
		byName := map[string]*plannedComponent{}
		for _, n := range []string{"zeta", "alpha", "mu", "beta", "omega"} {
			byName[n] = &plannedComponent{
				item: deploy.Component{Name: n, RecipePath: "recipes/" + n + ".toml"},
				rec:  &recipe.Recipe{UnknownFields: []string{`"metadata.autor" (line 4)`}},
			}
		}
		return &plannedState{path: "plan.toml", plan: &deploy.Plan{}, byName: byName}
	}
	first := strings.Join(build().unknownFieldReports(), "\n")
	for i := 0; i < 20; i++ {
		if got := strings.Join(build().unknownFieldReports(), "\n"); got != first {
			t.Fatalf("report order is not stable\nfirst:\n%s\nlater:\n%s", first, got)
		}
	}
}

func TestNoReportsWhenEverythingIsUnderstood(t *testing.T) {
	ps := &plannedState{
		path:   "plan.toml",
		plan:   &deploy.Plan{},
		byName: map[string]*plannedComponent{"api": {rec: &recipe.Recipe{}}},
	}
	if got := ps.unknownFieldReports(); len(got) != 0 {
		t.Errorf("clean plan produced reports: %v", got)
	}
}

// The list has to reach an operator who is looking at the API rather than the
// agent's stdout, which is the usual case on a device.
func TestPlanStatusSurfacesUnknownFields(t *testing.T) {
	a := &Agent{comps: store.NewMemoryStore()}
	a.planStatus = "running"
	a.planUnknown = []string{`recipe recipes/api.toml (component api): "metadata.autor" (line 4)`}

	st := a.GetPlanStatus()
	if len(st.UnknownFields) != 1 || !strings.Contains(st.UnknownFields[0], "metadata.autor") {
		t.Fatalf("UnknownFields = %v, want the recorded report", st.UnknownFields)
	}

	// The caller must not be handed the agent's own slice to mutate.
	st.UnknownFields[0] = "tampered"
	if a.planUnknown[0] == "tampered" {
		t.Error("GetPlanStatus handed out the agent's backing array")
	}
}
