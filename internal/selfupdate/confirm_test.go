package selfupdate

import (
	"testing"
)

func pendingLayout(t *testing.T, pending, confirmed string, boots int) Layout {
	t.Helper()
	l := Layout{Root: t.TempDir()}
	if err := l.SaveState(UpdateState{Pending: pending, Confirmed: confirmed, Boots: boots}); err != nil {
		t.Fatalf("save state: %v", err)
	}
	return l
}

// TestConfirmationNeedsBothFacts is the property the design rests on: starting
// is not proof. A version that supervises correctly but cannot reach the
// control plane is healthy by its own account and mute to the operator, and the
// order to roll it back travels over the channel it broke.
func TestConfirmationNeedsBothFacts(t *testing.T) {
	l := pendingLayout(t, "v2", "v1", 1)
	c := NewConfirmation(l, "v2", true)

	c.MarkConverged()
	if c.Confirmed() {
		t.Fatal("confirmed on convergence alone, with a control plane configured")
	}
	st, _ := l.LoadState()
	if st.Pending != "v2" || st.Boots != 1 {
		t.Fatalf("state was touched too early: %+v", st)
	}

	c.MarkReported()
	if !c.Confirmed() {
		t.Fatal("not confirmed after both facts")
	}

	st, _ = l.LoadState()
	if st.Pending != "" {
		t.Errorf("pending = %q, want cleared", st.Pending)
	}
	if st.Boots != 0 {
		t.Errorf("boots = %d, want reset", st.Boots)
	}
	if st.Confirmed != "v2" {
		t.Errorf("confirmed = %q, want v2", st.Confirmed)
	}
}

// TestConfirmationWithoutARemoteControlPlane: an agent with only a loopback
// HTTP adapter has nobody to be mute to. Requiring a report there would mean no
// update ever confirms, and every update would be reverted by a guardrail meant
// to catch broken ones.
func TestConfirmationWithoutARemoteControlPlane(t *testing.T) {
	l := pendingLayout(t, "v2", "v1", 1)
	c := NewConfirmation(l, "v2", false)

	c.MarkConverged()
	if !c.Confirmed() {
		t.Fatal("an update could never confirm without a remote control plane")
	}
}

// TestConfirmationIgnoresReportWithoutConvergence: being reachable is not the
// same as working. A version that talks to the broker while its components are
// down must not confirm itself.
func TestConfirmationIgnoresReportWithoutConvergence(t *testing.T) {
	l := pendingLayout(t, "v2", "v1", 1)
	c := NewConfirmation(l, "v2", true)

	c.MarkReported()
	c.MarkReported()
	if c.Confirmed() {
		t.Fatal("confirmed while the plan had not converged")
	}
}

// TestConfirmationIsIdempotent: both marks arrive repeatedly in a running
// agent — every health probe, every published event.
func TestConfirmationIsIdempotent(t *testing.T) {
	l := pendingLayout(t, "v2", "v1", 1)
	c := NewConfirmation(l, "v2", true)

	for i := 0; i < 5; i++ {
		c.MarkConverged()
		c.MarkReported()
	}

	st, _ := l.LoadState()
	if st.Confirmed != "v2" || st.Pending != "" || st.Boots != 0 {
		t.Errorf("repeated marks disturbed the state: %+v", st)
	}
}

// TestConfirmationRefusesAMismatchedVersion: if the state says a different
// version is on trial, something is wrong enough that confirming would clear a
// counter protecting someone else's update.
func TestConfirmationRefusesAMismatchedVersion(t *testing.T) {
	l := pendingLayout(t, "v3", "v1", 2)
	c := NewConfirmation(l, "v2", false)

	c.MarkConverged()
	if c.Confirmed() {
		t.Fatal("confirmed while a different version was pending")
	}
	st, _ := l.LoadState()
	if st.Pending != "v3" || st.Boots != 2 {
		t.Errorf("another version's trial was disturbed: %+v", st)
	}
}

// TestConfirmationOnAnOrdinaryStart: no update in flight. The running version
// still becomes the confirmed one — it is demonstrably working, and it is what
// the next update falls back to.
func TestConfirmationOnAnOrdinaryStart(t *testing.T) {
	l := Layout{Root: t.TempDir()}
	c := NewConfirmation(l, "v1", false)

	c.MarkConverged()

	st, _ := l.LoadState()
	if st.Confirmed != "v1" {
		t.Errorf("confirmed = %q, want the running version recorded", st.Confirmed)
	}
}

// TestConfirmationStatus is what the telemetry publishes, so an operator can
// see a device stuck in a trial that never completes.
func TestConfirmationStatus(t *testing.T) {
	l := pendingLayout(t, "v2", "v1", 1)
	c := NewConfirmation(l, "v2", true)

	if got := c.Status(); got != "pending-confirmation" {
		t.Errorf("status = %q before confirming", got)
	}
	c.MarkConverged()
	c.MarkReported()
	if got := c.Status(); got != "confirmed" {
		t.Errorf("status = %q after confirming", got)
	}

	var nilC *Confirmation
	if got := nilC.Status(); got != "idle" {
		t.Errorf("status = %q with no self-update configured", got)
	}
}
