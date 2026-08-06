package agent

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/carlosprados/keystone/internal/deploy"
	"github.com/carlosprados/keystone/internal/recipe"
	"github.com/carlosprados/keystone/internal/runner"
	"github.com/carlosprados/keystone/internal/state"
	"github.com/carlosprados/keystone/internal/store"
)

// deadPID is well above PID_MAX on Linux (4194303 by default), so no process
// can ever own it.
const deadPID = 99999999

func newStateAgent() *Agent {
	return &Agent{
		comps:          store.NewMemoryStore(),
		supervised:     make(map[string]bool),
		handles:        make(map[string]runner.Handle),
		runners:        make(map[string]runner.Runner),
		cancels:        make(map[string]context.CancelFunc),
		applySkipStart: make(map[string]bool),
	}
}

func TestComponentIsReusable(t *testing.T) {
	cases := []struct {
		name          string
		ci            *store.ComponentInfo // nil = component unknown to the store
		supervised    bool
		requireHealth bool
		want          bool
		reason        string
	}{
		{
			name:   "unknown component",
			want:   false,
			reason: "nothing is known about it, so it cannot be adopted",
		},
		{
			name:       "live process, supervised",
			ci:         &store.ComponentInfo{Name: "c", State: "running", LastHealth: "healthy", PID: os.Getpid()},
			supervised: true,
			want:       true,
			reason:     "alive and watched: the only case where reuse is safe",
		},
		{
			name:       "dead PID, supervised",
			ci:         &store.ComponentInfo{Name: "c", State: "running", LastHealth: "healthy", PID: deadPID},
			supervised: true,
			want:       false,
			reason:     "the reported PID does not exist: cached state is stale",
		},
		{
			name:       "live process, not supervised",
			ci:         &store.ComponentInfo{Name: "c", State: "running", LastHealth: "healthy", PID: os.Getpid()},
			supervised: false,
			want:       false,
			reason:     "no managed loop: no health probe and no restart policy attached",
		},
		{
			name:       "container-like component (no PID), supervised",
			ci:         &store.ComponentInfo{Name: "c", State: "running", LastHealth: "healthy", PID: 0},
			supervised: true,
			want:       true,
			reason:     "PID 0 cannot be probed; the supervision loop is the liveness signal",
		},
		{
			name:       "state not running",
			ci:         &store.ComponentInfo{Name: "c", State: "failed", LastHealth: "healthy", PID: os.Getpid()},
			supervised: true,
			want:       false,
			reason:     "only components reported running are reuse candidates",
		},
		{
			name:          "health required but unhealthy",
			ci:            &store.ComponentInfo{Name: "c", State: "running", LastHealth: "unhealthy", PID: os.Getpid()},
			supervised:    true,
			requireHealth: true,
			want:          false,
			reason:        "a component with a health check must be healthy to be reused",
		},
		{
			name:          "health required but unknown",
			ci:            &store.ComponentInfo{Name: "c", State: "running", LastHealth: "unknown", PID: os.Getpid()},
			supervised:    true,
			requireHealth: true,
			want:          false,
			reason:        "no health verdict yet is not the same as healthy",
		},
		{
			name:          "health not required, never probed",
			ci:            &store.ComponentInfo{Name: "c", State: "running", LastHealth: "unknown", PID: os.Getpid()},
			supervised:    true,
			requireHealth: false,
			want:          true,
			reason:        "components without a health check never get a verdict",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := newStateAgent()
			if c.ci != nil {
				a.comps.Upsert(*c.ci)
			}
			if c.supervised {
				a.markSupervised("c")
			}
			if got := a.componentIsReusable("c", c.requireHealth); got != c.want {
				t.Errorf("componentIsReusable()=%v, want %v (%s)", got, c.want, c.reason)
			}
		})
	}
}

// TestReconcileRestartsDeadNoTouchComponent covers the production incident: an
// unchanged component whose process is gone must be restarted by the reconcile,
// not classified as no_touch on the strength of a stale running/healthy record.
func TestReconcileRestartsDeadNoTouchComponent(t *testing.T) {
	planned, oldPlan := singleComponentPlan("api", "curl -sf http://127.0.0.1:8080/healthz")

	t.Run("dead process is restarted", func(t *testing.T) {
		a := newStateAgent()
		a.comps.Upsert(store.ComponentInfo{Name: "api", State: "running", LastHealth: "healthy", PID: deadPID})
		a.markSupervised("api")

		actions, err := a.buildReconcileActions(oldPlan, planned)
		if err != nil {
			t.Fatalf("buildReconcileActions: %v", err)
		}
		if !actions.startTargets["api"] {
			t.Errorf("api not in startTargets; a dead component must be restarted")
		}
		if actions.noTouch["api"] {
			t.Errorf("api classified as no_touch; its process does not exist")
		}
	})

	t.Run("live supervised process is reused", func(t *testing.T) {
		a := newStateAgent()
		a.comps.Upsert(store.ComponentInfo{Name: "api", State: "running", LastHealth: "healthy", PID: os.Getpid()})
		a.markSupervised("api")

		actions, err := a.buildReconcileActions(oldPlan, planned)
		if err != nil {
			t.Fatalf("buildReconcileActions: %v", err)
		}
		if !actions.noTouch["api"] {
			t.Errorf("api not in no_touch; an alive supervised component must not be restarted")
		}
		if actions.startTargets["api"] {
			t.Errorf("api in startTargets; nothing changed and it is alive")
		}
	})

	t.Run("unsupervised process is restarted", func(t *testing.T) {
		a := newStateAgent()
		a.comps.Upsert(store.ComponentInfo{Name: "api", State: "running", LastHealth: "healthy", PID: os.Getpid()})
		// No markSupervised: the managed loop already returned.

		actions, err := a.buildReconcileActions(oldPlan, planned)
		if err != nil {
			t.Fatalf("buildReconcileActions: %v", err)
		}
		if !actions.startTargets["api"] {
			t.Errorf("api not in startTargets; a component nobody supervises must be restarted")
		}
	})
}

// TestHandleComponentExit_CleanExit is the clean-exit manifestation: a process
// that exits 0 under restart_policy="on-failure" is not restarted, and the
// component store must say so instead of keeping the last known good state.
func TestHandleComponentExit_CleanExit(t *testing.T) {
	a := newStateAgent()
	a.comps.Upsert(store.ComponentInfo{
		Name: "border", State: "running", LastHealth: "healthy", PID: 181279, Restarts: 2,
		Recipe: "recipes/border.toml", Version: "1.4.0",
	})
	a.handles["border"] = &runner.ProcessHandle{}
	cleaned := false

	a.handleComponentExit("border", nil, func() { cleaned = true })

	ci, ok := a.comps.Get("border")
	if !ok {
		t.Fatal("component vanished from the store")
	}
	if ci.State != "stopped" {
		t.Errorf("State=%q, want %q: a clean exit that is not restarted leaves the component stopped", ci.State, "stopped")
	}
	if ci.PID != 0 {
		t.Errorf("PID=%d, want 0: the process is gone", ci.PID)
	}
	if ci.LastHealth == "healthy" {
		t.Errorf("LastHealth=%q: a component that exited cannot still be healthy", ci.LastHealth)
	}
	if _, stillThere := a.handles["border"]; stillThere {
		t.Errorf("handle still registered for an exited component")
	}
	if !cleaned {
		t.Errorf("runner cleanup was not invoked")
	}
	// Fields the exit says nothing about must survive.
	if ci.Restarts != 2 || ci.Recipe != "recipes/border.toml" || ci.Version != "1.4.0" {
		t.Errorf("unrelated fields were clobbered: %+v", ci)
	}
}

func TestHandleComponentExit_Failure(t *testing.T) {
	a := newStateAgent()
	a.comps.Upsert(store.ComponentInfo{Name: "influxdb", State: "running", LastHealth: "healthy", PID: 180072})
	a.handles["influxdb"] = &runner.ProcessHandle{}

	a.handleComponentExit("influxdb", errors.New("signal: killed"), nil)

	ci, _ := a.comps.Get("influxdb")
	if ci.State != "failed" {
		t.Errorf("State=%q, want %q", ci.State, "failed")
	}
	if ci.PID != 0 {
		t.Errorf("PID=%d, want 0: the process is gone", ci.PID)
	}
}

// TestHandleComponentExit_EndsSupervision guards the invariant reuse depends
// on: once the managed loop is gone the component must stop looking reusable.
func TestHandleComponentExit_EndsSupervision(t *testing.T) {
	a := newStateAgent()
	a.comps.Upsert(store.ComponentInfo{Name: "c", State: "running", LastHealth: "healthy", PID: os.Getpid()})
	a.markSupervised("c")
	if !a.componentIsReusable("c", false) {
		t.Fatal("precondition: an alive supervised component must be reusable")
	}

	a.handleComponentExit("c", nil, nil)
	a.clearSupervised("c") // what the deferred call in the managed goroutine does

	if a.componentIsReusable("c", false) {
		t.Errorf("component still reusable after its managed loop exited")
	}
}

// TestRefreshComponentStates covers the state poller: holding a handle is not
// proof of life. A stop path that signals the process without deregistering its
// handle (a failed apply unwinding a layer) used to leave the component
// reported running with a dead PID.
func TestRefreshComponentStates(t *testing.T) {
	a := newStateAgent()
	a.comps.Upsert(store.ComponentInfo{Name: "alive", State: "running", LastHealth: "healthy", PID: os.Getpid()})
	a.comps.Upsert(store.ComponentInfo{Name: "stale", State: "running", LastHealth: "healthy", PID: deadPID})
	a.comps.Upsert(store.ComponentInfo{Name: "gone", State: "running", LastHealth: "healthy", PID: deadPID})
	a.comps.Upsert(store.ComponentInfo{Name: "broken", State: "failed", LastHealth: "unhealthy", PID: deadPID})
	// "alive" and "stale" are both still registered; only one has a live process.
	a.handles["alive"] = &runner.ProcessHandle{}
	a.handles["stale"] = &runner.ProcessHandle{}
	// "gone" lost its handle, the usual case after a terminal exit.

	a.refreshComponentStates()

	for _, c := range []struct {
		name      string
		wantState string
		wantPID   int
		reason    string
	}{
		{"alive", "running", os.Getpid(), "handle plus a live process is the definition of running"},
		{"stale", "stopped", 0, "the handle is stale: nothing is behind that PID"},
		{"gone", "stopped", 0, "no handle and no process"},
		{"broken", "failed", deadPID, "a terminal failure is not downgraded by the poller"},
	} {
		ci, ok := a.comps.Get(c.name)
		if !ok {
			t.Errorf("%s: missing from the store", c.name)
			continue
		}
		if ci.State != c.wantState {
			t.Errorf("%s: State=%q, want %q (%s)", c.name, ci.State, c.wantState, c.reason)
		}
		if ci.PID != c.wantPID {
			t.Errorf("%s: PID=%d, want %d (%s)", c.name, ci.PID, c.wantPID, c.reason)
		}
	}
}

func TestMemoryStoreReplaceClearsPID(t *testing.T) {
	s := store.NewMemoryStore()
	s.Upsert(store.ComponentInfo{Name: "c", State: "running", PID: 4242, LastHealth: "healthy"})

	// Upsert cannot clear a PID: 0 means "keep the previous value".
	s.Upsert(store.ComponentInfo{Name: "c", State: "stopped", PID: 0})
	if ci, _ := s.Get("c"); ci.PID != 4242 {
		t.Errorf("Upsert changed the PID to %d; its merge semantics must keep it", ci.PID)
	}

	s.Replace(store.ComponentInfo{Name: "c", State: "stopped", PID: 0})
	if ci, _ := s.Get("c"); ci.PID != 0 {
		t.Errorf("Replace left PID=%d, want 0", ci.PID)
	}
	if ci, _ := s.Get("c"); ci.LastHealth != "unknown" {
		t.Errorf("LastHealth=%q, want %q for an empty value", ci.LastHealth, "unknown")
	}
}

// singleComponentPlan returns a desired state with one unchanged component plus
// the matching previous plan entry, so reconcile classifies it as no_touch and
// the decision rests entirely on liveness.
func singleComponentPlan(name, healthCheck string) (*plannedState, []state.PlanComponent) {
	rec := &recipe.Recipe{}
	rec.Metadata.Name = name
	rec.Metadata.Version = "1.0.0"
	rec.Lifecycle.Run.Health.Check = healthCheck

	const digest = "b4c0ffee"
	recipePath := "configs/examples/" + name + ".toml"
	pc := &plannedComponent{
		item:         deploy.Component{Name: name, RecipePath: recipePath},
		rec:          rec,
		recipeDigest: digest,
		recipeID:     recipeIdentity(rec),
	}
	planned := &plannedState{
		path:    "configs/examples/plan.toml",
		byName:  map[string]*plannedComponent{name: pc},
		edges:   map[string][]string{},
		planMap: []state.PlanComponent{{Name: name, RecipePath: recipePath, RecipeMeta: name, RecipeVersion: "1.0.0", RecipeID: pc.recipeID, RecipeDigest: digest}},
	}
	return planned, planned.planMap
}
