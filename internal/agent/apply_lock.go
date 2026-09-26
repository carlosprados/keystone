package agent

import (
	"fmt"
	"time"

	"github.com/carlosprados/keystone/internal/adapter"
)

// What can hold the apply lock. The names end up in the error a caller gets
// when it collides with one, so they are written for that reader.
const (
	applyByRequest = "a plan apply"
	applyByResume  = "the resume of the saved plan after the agent started"
	applyByTimer   = "a periodic reconcile"
)

// applyHolder records what holds the apply lock and since when.
//
// A collision used to answer "apply already in progress" whatever the cause.
// On a device that just booted the cause is almost always the agent resuming
// its own plan, and on v0.12.2 the same words came from an apply cut short by a
// crash: two very different situations with one message, which cost a field
// test a whole run to tell apart.
type applyHolder struct {
	what  string
	since time.Time
}

// tryAcquireApply takes the apply lock for what, or reports what holds it.
func (a *Agent) tryAcquireApply(what string) error {
	if !a.applyInProgress.CompareAndSwap(false, true) {
		return fmt.Errorf("%w: %s", adapter.ErrNotReady, a.applyBusyReason())
	}
	a.applyHolderMu.Lock()
	a.applyHolder = applyHolder{what: what, since: time.Now()}
	a.applyHolderMu.Unlock()
	return nil
}

func (a *Agent) releaseApply() {
	a.applyHolderMu.Lock()
	a.applyHolder = applyHolder{}
	a.applyHolderMu.Unlock()
	a.applyInProgress.Store(false)
}

// applyBusyReason says what is running, for a caller that has to wait for it.
// It is a transient condition, which is why the error wraps ErrNotReady: the
// HTTP adapter answers 503, "try again later", rather than 500.
func (a *Agent) applyBusyReason() string {
	a.applyHolderMu.Lock()
	h := a.applyHolder
	a.applyHolderMu.Unlock()
	if h.what == "" {
		return "another apply is already in progress; retry when it finishes"
	}
	return fmt.Sprintf("%s is already in progress (since %s, %s ago); retry when it finishes",
		h.what, h.since.UTC().Format(time.RFC3339), time.Since(h.since).Round(time.Second))
}
