package agent

import (
	"testing"

	"github.com/carlosprados/keystone/internal/selfupdate"
)

// TestAgentWithoutSelfUpdateIsInert: the overwhelming majority of installs do
// not update themselves, and every one of the calls below happens on their
// ordinary paths. A nil confirmation must cost nothing and crash nothing.
func TestAgentWithoutSelfUpdateIsInert(t *testing.T) {
	a := New(Options{InsecureSkipVerify: true})

	a.MarkUpdateReported()
	a.markUpdateConverged()

	if got := a.UpdateStatus(); got != "idle" {
		t.Errorf("UpdateStatus = %q, want idle", got)
	}
}

// TestAgentReportsPendingConfirmation: a device on trial must say so, because
// the operator who can see it is the one who cannot log in to check.
func TestAgentReportsPendingConfirmation(t *testing.T) {
	l := selfupdate.Layout{Root: t.TempDir()}
	if err := l.SaveState(selfupdate.UpdateState{Pending: "v2", Confirmed: "v1", Boots: 1}); err != nil {
		t.Fatalf("save state: %v", err)
	}

	a := New(Options{InsecureSkipVerify: true})
	a.SetUpdateConfirmation(selfupdate.NewConfirmation(l, "v2", true))

	if got := a.UpdateStatus(); got != "pending-confirmation" {
		t.Fatalf("UpdateStatus = %q, want pending-confirmation", got)
	}

	// Converging alone is not enough while a control plane is configured.
	a.markUpdateConverged()
	if got := a.UpdateStatus(); got != "pending-confirmation" {
		t.Errorf("UpdateStatus = %q after converging only", got)
	}

	a.MarkUpdateReported()
	if got := a.UpdateStatus(); got != "confirmed" {
		t.Errorf("UpdateStatus = %q after both facts", got)
	}

	st, err := l.LoadState()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if st.Pending != "" || st.Boots != 0 || st.Confirmed != "v2" {
		t.Errorf("state after confirmation: %+v", st)
	}
}
