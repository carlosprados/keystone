package selfupdate

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

// Confirmation decides when a pending version has proved itself.
//
// "It started" is not proof. A version can start, supervise its components
// correctly, and have broken its own way of reaching the control plane — a
// library bump, a stricter TLS default, a duplicated client id. It would then
// be healthy by its own account and mute to the operator, and the order to roll
// it back travels over the channel it broke. On a device reachable only
// outbound, that means a visit.
//
// So confirmation needs two independent facts:
//
//   - the plan converged: components are running and healthy;
//   - the device was heard from: a remote control plane received something.
//
// Until both hold, the boot counter keeps running and the gate will revert.
type Confirmation struct {
	mu sync.Mutex

	layout  Layout
	version string

	// requireReport is false when there is no remote control plane to report
	// to — an agent with only a loopback HTTP adapter has nobody to be mute
	// to. Requiring a report there would mean no update could ever confirm,
	// and every update would be reverted by a guardrail meant to catch broken
	// ones. The condition has to match the deployment, not the ideal.
	requireReport bool

	converged bool
	reported  bool
	done      bool
}

// NewConfirmation prepares the confirmation for the version now running.
//
// requireReport should be true whenever a remote control plane is configured,
// because that is exactly when being mute is a failure worth reverting for.
func NewConfirmation(layout Layout, version string, requireReport bool) *Confirmation {
	return &Confirmation{layout: layout, version: version, requireReport: requireReport}
}

// MarkConverged records that the plan is applied and its components are healthy.
func (c *Confirmation) MarkConverged() { c.mark(func() { c.converged = true }) }

// MarkReported records that a remote control plane received something from this
// device — proof that the way back in still works.
func (c *Confirmation) MarkReported() { c.mark(func() { c.reported = true }) }

func (c *Confirmation) mark(set func()) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.done {
		return
	}
	set()
	if !c.satisfiedLocked() {
		return
	}

	if err := c.commitLocked(); err != nil {
		// Not fatal: the version is working, and the worst case is that the
		// gate reverts a good update. Saying so is what lets an operator tell
		// that apart from a version that genuinely failed.
		log.Printf("[selfupdate] WARNING could not confirm version %s (%v); the boot counter will keep running and this update may be rolled back despite working", c.version, err)
		return
	}
	c.done = true
	log.Printf("[selfupdate] version %s confirmed", c.version)
}

func (c *Confirmation) satisfiedLocked() bool {
	if !c.converged {
		return false
	}
	return c.reported || !c.requireReport
}

// commitLocked writes the confirmation: the pending marker goes, the counter
// resets, and this version becomes the one a future failure rolls back to.
func (c *Confirmation) commitLocked() error {
	st, err := c.layout.LoadState()
	if err != nil {
		return err
	}
	if st.Pending == "" {
		// Nothing was pending: an ordinary start, not a trial. Recording the
		// running version as confirmed is still right — it is demonstrably
		// working, and it is what the next update will fall back to.
		if st.Confirmed == c.version {
			return nil
		}
	}
	if st.Pending != "" && st.Pending != c.version {
		return fmt.Errorf("pending version is %q but %q is running", st.Pending, c.version)
	}

	st.Pending = ""
	st.Boots = 0
	st.Confirmed = c.version
	return c.layout.SaveState(st)
}

// Confirmed reports whether this run has confirmed itself yet. It is what the
// telemetry publishes, so an operator can see a device sitting in a trial that
// never completes.
func (c *Confirmation) Confirmed() bool {
	if c == nil {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.done
}

// Status renders the update state for reporting: "idle" when nothing is in
// flight, "pending-confirmation" while a trial is running, "confirmed" once it
// has proved itself.
func (c *Confirmation) Status() string {
	if c == nil {
		return "idle"
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.done {
		return "confirmed"
	}
	return "pending-confirmation"
}

// WatchDeadline gives a trial a time limit, and calls expire once if this run
// has not confirmed itself within d.
//
// The gate reverts after a number of STARTS, and nothing else restarts a
// version that starts fine and then goes mute: it converges, never reaches the
// control plane, and would run unconfirmed forever with the counter frozen.
// That is exactly the failure confirmation exists to catch. Exiting when the
// deadline passes turns "mute" into a start the gate can count; after its limit
// it rolls back. Components survive each of these restarts through adoption.
//
// Only a trial is watched — this version is the pending one. An ordinary start
// that cannot reach its control plane has nothing to roll back to, and
// restarting it would only add noise. d <= 0 disables the deadline.
func (c *Confirmation) WatchDeadline(ctx context.Context, d time.Duration, expire func(reason string)) {
	if c == nil || d <= 0 {
		return
	}
	st, err := c.layout.LoadState()
	if err != nil || st.Pending != c.version {
		return
	}
	log.Printf("[selfupdate] version %s is on trial (start %d); it must confirm within %s or it restarts for the gate to count", c.version, st.Boots, d)

	go func() {
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if reason, late := c.missing(); late {
			log.Printf("[selfupdate] WARNING version %s did not confirm within %s (%s); restarting so the gate can count this start and roll back if it keeps failing", c.version, d, reason)
			expire(fmt.Sprintf("version %s did not confirm within %s: %s", c.version, d, reason))
		}
	}()
}

// missing says which half of the confirmation is still outstanding, and false
// once there is nothing left to wait for.
func (c *Confirmation) missing() (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done {
		return "", false
	}
	var parts []string
	if !c.converged {
		parts = append(parts, "the plan has not converged")
	}
	if c.requireReport && !c.reported {
		parts = append(parts, "no control plane has heard from the device")
	}
	if len(parts) == 0 {
		// Both halves hold but the commit failed; commitLocked already said why.
		parts = append(parts, "the confirmation could not be written")
	}
	return strings.Join(parts, " and "), true
}
