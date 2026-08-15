package agent

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/carlosprados/keystone/internal/dataset"
	"github.com/carlosprados/keystone/internal/metrics"
	"github.com/carlosprados/keystone/internal/recipe"
)

// datasetSweep is how often the agent checks whether any dataset is due. Each
// dataset has its own refresh interval; this is just the granularity at which
// those are noticed, so it can be far shorter than any of them without costing
// anything.
const datasetSweep = time.Minute

// installDatasets brings every dataset a component declares up to date before
// the component starts.
//
// Ordering matters and this is the only place that can guarantee it: a
// discovery product must not come up with no OUI list and start reporting
// unknown vendors. It runs inside the install hook, which the supervisor calls
// before the start hook.
func (a *Agent) installDatasets(ctx context.Context, component string, r *recipe.Recipe, workDir string) error {
	specs, err := dataset.ParseSpecs(r.Datasets)
	if err != nil {
		return err
	}
	if len(specs) == 0 {
		return nil
	}

	runType := r.Lifecycle.Run.Type
	reload, err := dataset.ParseReload(r.Lifecycle.Reload, runType)
	if err != nil {
		return fmt.Errorf("component %s: %w", component, err)
	}

	for _, spec := range specs {
		b := datasetBinding{spec: spec, component: component, reload: reload, workDir: workDir}
		a.mu.Lock()
		a.datasetSpecs[spec.Name] = spec
		a.datasetBinds[spec.Name] = b
		a.mu.Unlock()

		active := a.datasets.Active(spec.Name)
		if active != "" && !a.datasetDue(spec) {
			// Already serving something recent enough. Starting a component is
			// not the moment to go to the network.
			log.Printf("[dataset] name=%s version=%s msg=already current, not refreshing at install", spec.Name, active)
			continue
		}

		m, changed, err := a.resolveDataset(ctx, spec)
		if err != nil {
			metrics.ObserveDatasetRefresh(spec.Name, "failed")
			a.recordDataset(spec, nil, truncateReason(err.Error()))
			if spec.Required && active == "" {
				return fmt.Errorf("component %s needs dataset %q and it could not be fetched: %w", component, spec.Name, err)
			}
			// Optional, or we already have a usable copy: carry on with what is
			// on disk and let the staleness metric make the gap visible.
			log.Printf("[dataset] name=%s msg=refresh failed at install (%v); continuing with %s", spec.Name, err, orNone(active))
			continue
		}
		if !changed {
			metrics.ObserveDatasetRefresh(spec.Name, "unchanged")
			a.recordDataset(spec, m, "unchanged")
			continue
		}
		// No reload here: the component has not started yet, so there is
		// nothing to tell.
		if err := a.datasets.Activate(spec.Name, m.Version); err != nil {
			metrics.ObserveDatasetActivation(spec.Name, "failed")
			return fmt.Errorf("activating dataset %q for %s: %w", spec.Name, component, err)
		}
		_ = a.datasets.Prune(spec.Name, spec.Keep)
		metrics.ObserveDatasetActivation(spec.Name, "ok")
		metrics.ObserveDatasetRefresh(spec.Name, "updated")
		a.recordDataset(spec, m, "ok")
		log.Printf("[dataset] name=%s version=%s component=%s msg=installed", spec.Name, m.Version, component)
	}
	return nil
}

// datasetDue reports whether a dataset's refresh interval has elapsed.
//
// On the first boot after a long power-off this is true immediately, which is
// the whole missed-run policy: an intermittently powered device catches up when
// it wakes. A wall-clock cron is what would need rules for that case.
func (a *Agent) datasetDue(spec dataset.Spec) bool {
	a.mu.RLock()
	st, known := a.datasetState[spec.Name]
	a.mu.RUnlock()
	if !known || st.LastRefresh.IsZero() {
		return true
	}
	return time.Since(st.LastRefresh) >= spec.Refresh
}

// startDatasetRefresher runs the loop that keeps datasets current.
//
// One sweep for all of them rather than a goroutine each: the number of
// datasets is small, their intervals differ, and a single loop makes "what is
// due" a question with one answer.
func (a *Agent) startDatasetRefresher() {
	go func() {
		t := time.NewTicker(datasetSweep)
		defer t.Stop()
		for range t.C {
			if a.closed.Load() {
				return
			}
			a.refreshDueDatasets(a.Context())
		}
	}()
}

// refreshDueDatasets refreshes every dataset whose interval has elapsed.
func (a *Agent) refreshDueDatasets(ctx context.Context) {
	// An apply is rearranging components and their data; a refresh landing in
	// the middle would be reloading a component that is being replaced. It will
	// still be due on the next sweep.
	if a.applyInProgress.Load() {
		return
	}

	a.mu.RLock()
	binds := make([]datasetBinding, 0, len(a.datasetBinds))
	for _, b := range a.datasetBinds {
		binds = append(binds, b)
	}
	a.mu.RUnlock()

	for _, b := range binds {
		if !a.datasetDue(b.spec) {
			continue
		}
		a.refreshDataset(ctx, b)
	}
}

// refreshDataset performs one refresh, activating and reloading if the manifest
// names something new.
func (a *Agent) refreshDataset(ctx context.Context, b datasetBinding) {
	m, changed, err := a.resolveDataset(ctx, b.spec)
	if err != nil {
		metrics.ObserveDatasetRefresh(b.spec.Name, "failed")
		a.recordDataset(b.spec, nil, truncateReason(err.Error()))
		log.Printf("[dataset] name=%s msg=refresh failed: %v", b.spec.Name, err)
		return
	}
	if !changed {
		metrics.ObserveDatasetRefresh(b.spec.Name, "unchanged")
		a.recordDataset(b.spec, m, "unchanged")
		return
	}

	if err := a.activateDataset(ctx, b, m); err != nil {
		metrics.ObserveDatasetActivation(b.spec.Name, "failed")
		metrics.ObserveDatasetRefresh(b.spec.Name, "failed")
		a.recordDataset(b.spec, nil, truncateReason(err.Error()))
		log.Printf("[dataset] name=%s msg=activation failed: %v", b.spec.Name, err)
		return
	}
	metrics.ObserveDatasetActivation(b.spec.Name, "ok")
	metrics.ObserveDatasetRefresh(b.spec.Name, "updated")
	a.recordDataset(b.spec, m, "ok")
}

// RefreshDatasets checks every dataset for a new version now, whatever their
// intervals say — for the day a vulnerability feed cannot wait until tonight.
//
// Each one still goes through the same path as a scheduled refresh: signature,
// anti-replay, activation, reload, rollback. The caller reads the outcome from
// DatasetStates rather than a return value, so one dataset failing does not
// hide what happened to the others.
func (a *Agent) RefreshDatasets() {
	ctx := a.Context()
	a.mu.RLock()
	binds := make([]datasetBinding, 0, len(a.datasetBinds))
	for _, b := range a.datasetBinds {
		binds = append(binds, b)
	}
	a.mu.RUnlock()

	for _, b := range binds {
		a.refreshDataset(ctx, b)
	}
}

// datasetEnv returns the environment entries a component needs to find its
// datasets.
func (a *Agent) datasetEnv(r *recipe.Recipe) []string {
	specs, err := dataset.ParseSpecs(r.Datasets)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(specs))
	for _, spec := range specs {
		out = append(out, fmt.Sprintf("%s=%s", dataset.EnvName(spec.Name), a.datasets.Path(spec.Name)))
	}
	return out
}

// truncateReason keeps a stored failure short enough to be read in a status
// response rather than scrolled past.
func truncateReason(s string) string {
	const limit = 200
	s = trimSpace(s)
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "…"
}

func trimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\n' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\n' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}
