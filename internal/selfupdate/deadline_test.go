package selfupdate

import (
	"context"
	"strings"
	"testing"
	"time"
)

func expiry(t *testing.T, c *Confirmation, d time.Duration) (<-chan string, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	got := make(chan string, 1)
	c.WatchDeadline(ctx, d, func(reason string) { got <- reason })
	return got, cancel
}

// TestAMuteTrialRestarts is the case the gate could not see: a version that
// converges but never reaches its control plane. Nothing restarted it, the boot
// counter never moved, and it ran unconfirmed forever. The deadline turns that
// into a start the gate counts.
func TestAMuteTrialRestarts(t *testing.T) {
	c := NewConfirmation(pendingLayout(t, "v2", "v1", 1), "v2", true)
	c.MarkConverged()

	got, _ := expiry(t, c, 50*time.Millisecond)
	select {
	case reason := <-got:
		if !strings.Contains(reason, "no control plane has heard from the device") {
			t.Errorf("reason %q does not say which half is missing", reason)
		}
		if strings.Contains(reason, "not converged") {
			t.Errorf("reason %q blames convergence, which held", reason)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("an unconfirmed trial was never restarted")
	}
}

func TestAConfirmedTrialIsLeftAlone(t *testing.T) {
	c := NewConfirmation(pendingLayout(t, "v2", "v1", 1), "v2", true)
	got, _ := expiry(t, c, 100*time.Millisecond)
	c.MarkConverged()
	c.MarkReported()

	select {
	case reason := <-got:
		t.Fatalf("a confirmed version was restarted: %s", reason)
	case <-time.After(300 * time.Millisecond):
	}
}

// TestOnlyATrialIsWatched: an ordinary start that cannot reach its control
// plane has nothing to roll back to. Restarting it on a timer would only bounce
// a device that is merely offline.
func TestOnlyATrialIsWatched(t *testing.T) {
	for name, l := range map[string]Layout{
		"nothing pending":         pendingLayout(t, "", "v1", 0),
		"another version's trial": pendingLayout(t, "v3", "v1", 1),
	} {
		t.Run(name, func(t *testing.T) {
			c := NewConfirmation(l, "v2", true)
			got, _ := expiry(t, c, 20*time.Millisecond)
			select {
			case reason := <-got:
				t.Fatalf("restarted outside its own trial: %s", reason)
			case <-time.After(200 * time.Millisecond):
			}
		})
	}
}

// TestWithoutARemoteControlPlaneOnlyConvergenceCounts: with nobody to report
// to, a trial must not be failed for silence.
func TestWithoutARemoteControlPlaneOnlyConvergenceCounts(t *testing.T) {
	c := NewConfirmation(pendingLayout(t, "v2", "v1", 1), "v2", false)
	got, _ := expiry(t, c, 100*time.Millisecond)
	c.MarkConverged()

	select {
	case reason := <-got:
		t.Fatalf("restarted for silence with no control plane configured: %s", reason)
	case <-time.After(300 * time.Millisecond):
	}
}
