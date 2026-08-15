package agent

import (
	"os"
	"strings"
	"testing"

	"github.com/carlosprados/keystone/internal/state"
	"github.com/carlosprados/keystone/internal/store"
)

// A pass that decides to change nothing must say so and return no error. A
// timer that reports "an apply was running" as a failure teaches operators to
// ignore the ones that matter.
func TestReconcileNowSkips(t *testing.T) {
	cases := []struct {
		name       string
		planPath   string
		planStatus string
		applyBusy  bool
		wantReason string
	}{
		{
			name:       "no plan applied yet",
			wantReason: "no plan",
		},
		{
			name:       "operator stopped the plan",
			planPath:   "plan.toml",
			planStatus: "stopped",
			wantReason: "stopped by an operator",
		},
		{
			name:       "last state was a dry run",
			planPath:   "plan.toml",
			planStatus: "dry-run",
			wantReason: "stopped by an operator",
		},
		{
			name:       "an apply is already running",
			planPath:   "plan.toml",
			planStatus: "running",
			applyBusy:  true,
			wantReason: "already in progress",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := newStateAgent()
			a.planPath = c.planPath
			a.planStatus = c.planStatus
			if c.applyBusy {
				a.applyInProgress.Store(true)
			}

			res, err := a.ReconcileNow()
			if err != nil {
				t.Fatalf("skipping must not be an error, got %v", err)
			}
			if !res.Skipped {
				t.Fatalf("pass was not skipped; reason=%q", res.Reason)
			}
			if !strings.Contains(res.Reason, c.wantReason) {
				t.Errorf("reason %q does not mention %q", res.Reason, c.wantReason)
			}
			if res.Duration == "" {
				t.Error("a skipped pass still reports how long it took")
			}
			if c.applyBusy && !a.applyInProgress.Load() {
				t.Error("a skipped pass released the apply lock it never took")
			}
		})
	}
}

// The plan status is where an operator sees that reconcile is running at all.
func TestReconcileNowRecordsOutcome(t *testing.T) {
	a := newStateAgent()
	a.planComps = nil

	if _, err := a.ReconcileNow(); err != nil {
		t.Fatalf("ReconcileNow: %v", err)
	}
	ps := a.GetPlanStatus()
	if ps.LastReconcile == "" {
		t.Error("plan status does not report when the last pass ran")
	}
	if !strings.Contains(ps.LastReconcileResult, "skipped") {
		t.Errorf("last result %q does not report the skip", ps.LastReconcileResult)
	}
}

func TestRepairedSince(t *testing.T) {
	a := newStateAgent()
	a.planComps = []state.PlanComponent{
		{Name: "healthy"}, {Name: "revived"}, {Name: "container"},
	}
	live := os.Getpid()

	a.comps.Replace(store.ComponentInfo{Name: "healthy", State: "running", PID: live})
	a.comps.Replace(store.ComponentInfo{Name: "revived", State: "failed", PID: 0})
	a.comps.Replace(store.ComponentInfo{Name: "container", State: "stopped", PID: 0})
	// Not in the plan: whatever happens to it, it is not this plan's repair.
	a.comps.Replace(store.ComponentInfo{Name: "leftover", State: "failed", PID: 0})

	got := a.repairedSince(a.componentFingerprints())
	want := []string{"container", "revived"}
	if len(got) != len(want) {
		t.Fatalf("repaired=%v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("repaired=%v, want %v (sorted)", got, want)
		}
	}
}

// The state a component reads immediately after being started is not "running"
// — refreshComponentStates writes that on its own 500ms cycle. A repair count
// that waited for it would count nothing, which is how the first end-to-end run
// behaved.
func TestRepairedSinceDoesNotWaitForTheStatePoller(t *testing.T) {
	a := newStateAgent()
	a.planComps = []state.PlanComponent{{Name: "victim"}}
	a.comps.Replace(store.ComponentInfo{Name: "victim", State: "failed", PID: 0})
	before := a.componentFingerprints()

	// Exactly what applyPlan leaves behind before the poller catches up.
	a.comps.Replace(store.ComponentInfo{Name: "victim", State: "none", PID: 0})

	got := a.repairedSince(before)
	if len(got) != 1 || got[0] != "victim" {
		t.Errorf("repaired=%v, want [victim] regardless of the post-apply state", got)
	}
}

// A component that was already running and came back with a new PID was
// restarted by a changed recipe, not repaired. Counting it would make the
// repairs metric useless for spotting a component that keeps dying.
func TestRepairedSinceIgnoresRestartOfALiveComponent(t *testing.T) {
	a := newStateAgent()
	a.planComps = []state.PlanComponent{{Name: "api"}}
	a.comps.Replace(store.ComponentInfo{Name: "api", State: "running", PID: 1000})
	before := a.componentFingerprints()

	a.comps.Replace(store.ComponentInfo{Name: "api", State: "running", PID: 2000})

	if got := a.repairedSince(before); len(got) != 0 {
		t.Errorf("repaired=%v, want none: a live component that restarted was not repaired", got)
	}
}

// A pass over a plan whose components are all untouched must report nothing.
func TestRepairedSinceReportsNothingWhenUnchanged(t *testing.T) {
	a := newStateAgent()
	a.planComps = []state.PlanComponent{{Name: "db"}, {Name: "api"}}
	a.comps.Replace(store.ComponentInfo{Name: "db", State: "running", PID: 1000})
	a.comps.Replace(store.ComponentInfo{Name: "api", State: "running", PID: 1001})
	before := a.componentFingerprints()

	if got := a.repairedSince(before); len(got) != 0 {
		t.Errorf("repaired=%v over an unchanged plan, want none", got)
	}
}
