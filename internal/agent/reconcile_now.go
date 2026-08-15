package agent

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/carlosprados/keystone/internal/adapter"
	"github.com/carlosprados/keystone/internal/metrics"
)

// ReconcileNow re-applies the plan already in effect so components that died
// and exhausted their restart budget are started again.
//
// The repair itself needs no new logic: buildReconcileActions already puts
// every component that fails componentIsReusable — not running, not
// supervised, or with a dead PID — into startTargets, and leaves the healthy
// ones strictly alone. This method is the safe way to ask for that.
//
// Two things make it more than a call to ApplyPlan:
//
//   - It applies with rollback DISABLED. ApplyPlan captures the plan in effect
//     as the rollback target, which for a re-apply of the same plan is the plan
//     that just failed; the rollback would then stop every healthy component
//     (stopPlanInternal) and re-apply the failure. Harmless-looking once, and
//     ruinous on a timer.
//   - It refuses to resurrect a plan an operator stopped, reusing the same
//     predicate the boot resume uses. "Should I bring this plan up without
//     being asked?" is one question, and its two answers must not drift.
//
// Deciding to do nothing is not an error: a timer that logs a failure every
// time an apply happens to be running teaches operators to ignore it.
func (a *Agent) ReconcileNow() (*adapter.ReconcileResult, error) {
	start := time.Now()
	res := &adapter.ReconcileResult{}

	done := func(outcome string, err error) (*adapter.ReconcileResult, error) {
		elapsed := time.Since(start)
		res.Duration = elapsed.Round(time.Millisecond).String()
		metrics.ObserveReconcile(outcome, elapsed, res.Repaired)
		a.recordReconcile(res, err)
		return res, err
	}

	a.mu.RLock()
	planPath := a.planPath
	planStatus := a.planStatus
	a.mu.RUnlock()

	if strings.TrimSpace(planPath) == "" {
		res.Skipped = true
		res.Reason = "no plan has been applied yet"
		return done("skipped", nil)
	}
	if !shouldResumeLastPlan(planStatus) {
		res.Skipped = true
		res.Reason = fmt.Sprintf("plan is %q; a plan stopped by an operator is never resumed automatically", planStatus)
		return done("skipped", nil)
	}
	if !a.applyInProgress.CompareAndSwap(false, true) {
		res.Skipped = true
		res.Reason = "an apply is already in progress"
		return done("skipped", nil)
	}
	defer a.applyInProgress.Store(false)

	before := a.componentFingerprints()

	// allowRollback=false — see the doc comment above.
	if err := a.applyPlanReconcileUnlocked(planPath, false, false); err != nil {
		log.Printf("[agent] reconcile failed: %v", err)
		return done("failed", err)
	}
	// Only on success: a failed apply may have started some components and not
	// others, and claiming repairs it did not make is worse than claiming none.
	res.Repaired = a.repairedSince(before)
	if len(res.Repaired) > 0 {
		log.Printf("[agent] reconcile repaired %d component(s): %v", len(res.Repaired), res.Repaired)
	}
	return done("ok", nil)
}

// componentFingerprints captures enough of each component's observable state to
// tell afterwards whether it was replaced.
//
// State and PID together, because neither alone is sufficient: a component can
// be restarted and end up in the same state with a different PID, and a
// container reports PID 0 throughout, so only its state moves.
func (a *Agent) componentFingerprints() map[string]string {
	out := map[string]string{}
	for _, ci := range a.comps.List() {
		out[ci.Name] = fmt.Sprintf("%s/%d", ci.State, ci.PID)
	}
	return out
}

// repairedSince names the plan components that were not running before the
// pass. Call it only after an apply that returned no error, which is what makes
// them running now: StartStack either brings every component up or fails.
//
// Deliberately not "which components are running now and were not before". The
// store's State is written by refreshComponentStates on its own 500ms cycle,
// not by the apply, so a component that was just started still reads "none" for
// up to half a second — long enough for every repair to go uncounted, which is
// exactly what the first end-to-end run showed.
//
// A component that was already running and got a new PID is not a repair: that
// is a restart caused by a changed recipe, and calling it a repair would make
// keystone_reconcile_repairs_total meaningless for the thing it exists to
// answer — is something on this device dying repeatedly?
func (a *Agent) repairedSince(before map[string]string) []string {
	a.mu.RLock()
	names := make([]string, 0, len(a.planComps))
	for _, pc := range a.planComps {
		names = append(names, pc.Name)
	}
	a.mu.RUnlock()

	var out []string
	for _, name := range names {
		if !strings.HasPrefix(before[name], "running/") {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// recordReconcile stores the outcome of the last pass for the plan status, so
// an operator can see that reconcile is running — and what it found — without
// reading the agent's logs.
func (a *Agent) recordReconcile(res *adapter.ReconcileResult, err error) {
	summary := "ok"
	switch {
	case err != nil:
		summary = "failed: " + err.Error()
	case res.Skipped:
		summary = "skipped: " + res.Reason
	case len(res.Repaired) > 0:
		summary = fmt.Sprintf("repaired %s", strings.Join(res.Repaired, ", "))
	}
	a.mu.Lock()
	a.lastReconcile = time.Now().UTC()
	a.lastReconcileResult = summary
	a.mu.Unlock()
}
