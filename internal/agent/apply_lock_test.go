package agent

import (
	"errors"
	"strings"
	"testing"

	"github.com/carlosprados/keystone/internal/adapter"
)

// TestACollisionSaysWhatIsRunning: "apply already in progress" used to be the
// answer whether the agent was resuming its own plan after a boot or something
// else held the lock, and a field test lost a run telling them apart.
func TestACollisionSaysWhatIsRunning(t *testing.T) {
	a := newStateAgent()
	if err := a.tryAcquireApply(applyByResume); err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	err := a.ApplyPlan("plan.toml", false)
	if !errors.Is(err, adapter.ErrNotReady) {
		t.Fatalf("collision err = %v; want ErrNotReady, which the HTTP adapter answers with 503", err)
	}
	if !strings.Contains(err.Error(), applyByResume) {
		t.Errorf("collision message %q does not say the resume is what is running", err)
	}

	a.releaseApply()
	if err := a.tryAcquireApply(applyByRequest); err != nil {
		t.Errorf("lock not released: %v", err)
	}
	if strings.Contains(a.applyBusyReason(), applyByResume) {
		t.Error("the released holder is still reported")
	}
}
