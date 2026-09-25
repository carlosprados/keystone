package agent

import (
	"reflect"
	"testing"

	"github.com/carlosprados/keystone/internal/recipe"
)

// TestHealthFieldsAreAllMapped fails when a field is added to the recipe's
// health block and not carried into the runner's config.
//
// This is the second half of a defect that lived in the repo for a week. The
// first half was six places asking `Check == ""` and missing the argv form;
// that is fixed by HealthConfig.Configured(), which is now the single answer to
// "does this declare a probe". But a field can still be added to the recipe and
// silently dropped on the way across, which produces the same symptom: a recipe
// that declares something the system behaves as if it had not.
//
// The count is deliberately hard-coded. Adding a field breaks this test, which
// is the point: it forces whoever adds it to look at the mapping rather than
// discover the omission on a device three releases later.
func TestHealthFieldsAreAllMapped(t *testing.T) {
	const knownFields = 5 // Check, Exec, Interval, Timeout, FailureThreshold

	got := reflect.TypeOf(recipe.Health{}).NumField()
	if got != knownFields {
		t.Fatalf("recipe.Health now has %d fields, not %d.\n"+
			"A field was added. Carry it into buildHealthConfig and into "+
			"HealthConfig.Configured() if it can declare a probe on its own, "+
			"then update this count.", got, knownFields)
	}

	// And check each one actually arrives, in both probe forms.
	r := &recipe.Recipe{}
	r.Lifecycle.Run.Health = recipe.Health{
		Check:            "http://127.0.0.1:8080/healthz",
		Interval:         "7s",
		Timeout:          "2s",
		FailureThreshold: 9,
	}
	hc := buildHealthConfig(r)
	if hc.Check != "http://127.0.0.1:8080/healthz" {
		t.Errorf("Check did not arrive: %q", hc.Check)
	}
	if hc.Interval.String() != "7s" {
		t.Errorf("Interval did not arrive: %v", hc.Interval)
	}
	if hc.Timeout.String() != "2s" {
		t.Errorf("Timeout did not arrive: %v", hc.Timeout)
	}
	if hc.FailureThreshold != 9 {
		t.Errorf("FailureThreshold did not arrive: %d", hc.FailureThreshold)
	}
	if !hc.Configured() {
		t.Error("a check probe is not recognised as a probe")
	}

	r2 := &recipe.Recipe{}
	r2.Lifecycle.Run.Health = recipe.Health{Exec: []string{"/app", "healthcheck"}}
	hc2 := buildHealthConfig(r2)
	if len(hc2.Exec) != 2 || hc2.Exec[0] != "/app" {
		t.Errorf("Exec did not arrive: %v", hc2.Exec)
	}
	if !hc2.Configured() {
		t.Error("an exec probe is not recognised as a probe: this is the defect that hid for a week")
	}
}

// TestDeclaredProbeGatesReadiness is what makes a mute probe loud.
//
// A component that declares a probe is not considered ready until that probe
// reports healthy, so one that never reports fails its apply with a readiness
// timeout instead of sitting at "unknown" forever looking fine. That property
// is what would have surfaced the week-old defect on the first device that ran
// it — the symptom was invisible precisely because a declared-but-never-run
// probe was indistinguishable from no probe at all.
func TestDeclaredProbeGatesReadiness(t *testing.T) {
	r := &recipe.Recipe{}
	r.Lifecycle.Run.Health = recipe.Health{Exec: []string{"/bin/true"}, Interval: "10s"}

	if got := computeReadyTimeout(r); got < 30*1e9 {
		t.Errorf("readiness timeout for a probed component = %v; too short to let the probe report", got)
	}

	plain := &recipe.Recipe{}
	if computeReadyTimeout(plain) >= 30*1e9 {
		t.Error("a component with no probe is waiting as if it had one")
	}
}
