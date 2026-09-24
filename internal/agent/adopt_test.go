package agent

import (
	"testing"

	"github.com/carlosprados/keystone/internal/store"
)

// TestTakeAdoptableRequiresAnUnchangedComponent is the condition that keeps
// adoption from becoming a silent downgrade.
//
// A component whose recipe moved is being started precisely because it must be
// different. Adopting the survivor there would leave the OLD build running
// while the agent reports the new one — a deployment that claims success and
// changed nothing, which is worse than the restart adoption exists to avoid.
func TestTakeAdoptableRequiresAnUnchangedComponent(t *testing.T) {
	a := New(Options{InsecureSkipVerify: true})
	a.adoptable = map[string]int{"api": 4242, "worker": 4243}
	a.applySkipStart = map[string]bool{"api": true} // worker changed

	if got := a.takeAdoptable("api"); got != 4242 {
		t.Errorf("unchanged component: got %d, want 4242", got)
	}
	if got := a.takeAdoptable("worker"); got != 0 {
		t.Errorf("changed component was adopted: got %d, want 0", got)
	}

	// The unclaimed survivor must stay on the list so it is reaped, not left
	// running unsupervised.
	if _, still := a.adoptable["worker"]; !still {
		t.Error("a survivor that was not adopted was dropped instead of kept for reaping")
	}
}

// TestTakeAdoptableHandsOutAPIDOnce: a PID is only safe to adopt while nothing
// has had the chance to reuse it.
func TestTakeAdoptableHandsOutAPIDOnce(t *testing.T) {
	a := New(Options{InsecureSkipVerify: true})
	a.adoptable = map[string]int{"api": 4242}
	a.applySkipStart = map[string]bool{"api": true}

	if got := a.takeAdoptable("api"); got != 4242 {
		t.Fatalf("first call: got %d", got)
	}
	if got := a.takeAdoptable("api"); got != 0 {
		t.Errorf("the same PID was handed out twice: %d", got)
	}
}

// TestTakeAdoptableOnAnUnknownComponent: the common case, and it must be free.
func TestTakeAdoptableOnAnUnknownComponent(t *testing.T) {
	a := New(Options{InsecureSkipVerify: true})

	if got := a.takeAdoptable("never-seen"); got != 0 {
		t.Errorf("got %d, want 0", got)
	}
}

// TestAdoptableComponentsSkipsWhatIsNotOurs pins the identifying test: alive
// AND reparented to init. A live PID that is not an init orphan belongs to
// somebody else — the previous agent exited cleanly and took its children with
// it — and supervising a stranger is worse than restarting our own component.
func TestAdoptableComponentsSkipsWhatIsNotOurs(t *testing.T) {
	// This process is alive and is NOT an init orphan: it has a real parent.
	self := store.ComponentInfo{Name: "self", PID: 1}
	// PID 1 is init itself, which processIsInitOrphan reports false for
	// (its parent is 0), so it must not be offered.
	got := adoptableComponents([]store.ComponentInfo{
		self,
		{Name: "nopid", PID: 0},
		{Name: "negative", PID: -5},
	})

	if len(got) != 0 {
		t.Errorf("offered %v for adoption; none of these are ours", got)
	}
}
