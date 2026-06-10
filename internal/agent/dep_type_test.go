package agent

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"

	"github.com/carlosprados/keystone/internal/state"
)

func TestNormalizeDepType(t *testing.T) {
	cases := map[string]string{
		"":          "hard",
		"   ":       "hard",
		"hard":      "hard",
		"HARD":      "hard",
		"  Soft  ":  "soft",
		"ordering":  "ordering",
		"weird-val": "weird-val", // normalize keeps unknown to surface via validateDepType
	}
	for in, want := range cases {
		if got := normalizeDepType(in); got != want {
			t.Errorf("normalizeDepType(%q)=%q, want %q", in, got, want)
		}
	}
}

func TestValidateDepType(t *testing.T) {
	for _, ok := range []string{"", "hard", "HARD", "soft", "ordering", "  ordering  "} {
		if err := validateDepType(ok); err != nil {
			t.Errorf("validateDepType(%q) unexpectedly errored: %v", ok, err)
		}
	}
	for _, bad := range []string{"weak", "must", "after", "before", "partof"} {
		if err := validateDepType(bad); err == nil {
			t.Errorf("validateDepType(%q) expected error, got nil", bad)
		}
	}
}

func TestPresenceAndCascadeMatrix(t *testing.T) {
	cases := []struct {
		typ                string
		wantMandatory      bool
		wantCascadeRestart bool
	}{
		{"", true, true},          // empty defaults to hard
		{"hard", true, true},      // hard: mandatory + cascade
		{"HARD", true, true},      // case-insensitive
		{"soft", false, true},     // soft: optional + cascade (this is the bug being fixed)
		{"ordering", true, false}, // ordering: mandatory + NO cascade
	}
	for _, c := range cases {
		if got := isPresenceMandatory(c.typ); got != c.wantMandatory {
			t.Errorf("isPresenceMandatory(%q)=%v, want %v", c.typ, got, c.wantMandatory)
		}
		if got := shouldCascadeRestart(c.typ); got != c.wantCascadeRestart {
			t.Errorf("shouldCascadeRestart(%q)=%v, want %v", c.typ, got, c.wantCascadeRestart)
		}
	}
}

// Plan shape used in the cascade tests:
//
//	sink  <- hardConsumer    (hard:    cascades)
//	sink  <- softConsumer    (soft:    cascades)
//	sink  <- orderingClient  (ordering: does NOT cascade)
//	hardConsumer <- transitive (hard: cascades)  — should also be pulled in
func newCascadePlan() []state.PlanComponent {
	return []state.PlanComponent{
		{Name: "sink"},
		{
			Name:     "hardConsumer",
			Deps:     []string{"sink"},
			DepTypes: map[string]string{"sink": "hard"},
		},
		{
			Name:     "softConsumer",
			Deps:     []string{"sink"},
			DepTypes: map[string]string{"sink": "soft"},
		},
		{
			Name:     "orderingClient",
			Deps:     []string{"sink"},
			DepTypes: map[string]string{"sink": "ordering"},
		},
		{
			Name:     "transitive",
			Deps:     []string{"hardConsumer"},
			DepTypes: map[string]string{"hardConsumer": "hard"},
		},
	}
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func TestPlanDependentsTopological_AllVsCascading(t *testing.T) {
	a := &Agent{planComps: newCascadePlan()}

	// cascadingOnly=false -> every dependent is returned (legacy behaviour)
	wantAll := []string{"hardConsumer", "softConsumer", "orderingClient", "transitive"}
	gotAll := a.planDependentsTopological("sink", false)
	if !reflect.DeepEqual(sortedCopy(gotAll), sortedCopy(wantAll)) {
		t.Errorf("non-filtered dependents of sink: got %v want %v", sortedCopy(gotAll), sortedCopy(wantAll))
	}

	// cascadingOnly=true -> orderingClient is excluded; transitive remains (hard chain)
	wantCascade := []string{"hardConsumer", "softConsumer", "transitive"}
	gotCascade := a.planDependentsTopological("sink", true)
	if !reflect.DeepEqual(sortedCopy(gotCascade), sortedCopy(wantCascade)) {
		t.Errorf("cascading dependents of sink: got %v want %v", sortedCopy(gotCascade), sortedCopy(wantCascade))
	}
	for _, n := range gotCascade {
		if n == "orderingClient" {
			t.Fatalf("orderingClient must NOT appear in cascading dependents, got %v", gotCascade)
		}
	}
}

func TestPlanDependentsTopological_LegacySnapshotDefaultsToHard(t *testing.T) {
	// Simulate a snapshot written before DepTypes existed: DepTypes is nil but
	// Deps is populated. Backward-compat requires defaulting to "hard" so every
	// edge still cascades.
	plan := []state.PlanComponent{
		{Name: "sink"},
		{Name: "consumerA", Deps: []string{"sink"}},
		{Name: "consumerB", Deps: []string{"sink"}},
	}
	a := &Agent{planComps: plan}
	got := a.planDependentsTopological("sink", true)
	want := []string{"consumerA", "consumerB"}
	if !reflect.DeepEqual(sortedCopy(got), sortedCopy(want)) {
		t.Errorf("legacy snapshot cascade: got %v want %v", sortedCopy(got), sortedCopy(want))
	}
}

func TestPlanComponentSnapshotRoundTrip(t *testing.T) {
	original := state.PlanComponent{
		Name:          "consumer",
		RecipePath:    "consumer:1.0.0",
		RecipeMeta:    "com.example.consumer",
		RecipeVersion: "1.0.0",
		RecipeID:      "com.example.consumer:1.0.0",
		RecipeDigest:  "deadbeef",
		Deps:          []string{"sink", "router"},
		DepTypes: map[string]string{
			"sink":   "soft",
			"router": "ordering",
		},
	}
	b, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var decoded state.PlanComponent
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if !reflect.DeepEqual(decoded, original) {
		t.Errorf("round-trip mismatch:\n got %+v\nwant %+v", decoded, original)
	}

	// Legacy snapshot: no dep_types field at all -> decode to nil map.
	legacy := []byte(`{"name":"old","recipe_path":"p","recipe_meta":"m","deps":["x"]}`)
	var oldComp state.PlanComponent
	if err := json.Unmarshal(legacy, &oldComp); err != nil {
		t.Fatalf("legacy unmarshal failed: %v", err)
	}
	if oldComp.DepTypes != nil {
		t.Errorf("legacy snapshot must decode DepTypes as nil, got %v", oldComp.DepTypes)
	}
}
