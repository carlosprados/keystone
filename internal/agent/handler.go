package agent

import (
	"context"
	"log"
	"time"

	"github.com/carlosprados/keystone/internal/adapter"
	"github.com/carlosprados/keystone/internal/runner"
	"github.com/carlosprados/keystone/internal/store"
)

// Ensure Agent implements adapter.CommandHandler at compile time.
var _ adapter.CommandHandler = (*Agent)(nil)

// GetPlanStatus returns the current plan status.
func (a *Agent) GetPlanStatus() *adapter.PlanStatus {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return &adapter.PlanStatus{
		PlanPath:   a.planPath,
		Status:     a.planStatus,
		Error:      a.planErr,
		Components: a.comps.List(),
	}
}

// GetPlanGraph returns the dependency graph and topological order.
func (a *Agent) GetPlanGraph() *adapter.GraphInfo {
	a.mu.RLock()
	defer a.mu.RUnlock()

	nodes := make([]string, 0, len(a.planComps))
	for _, pc := range a.planComps {
		nodes = append(nodes, pc.Name)
	}

	edges := map[string][]string{}
	indeg := map[string]int{}
	for _, pc := range a.planComps {
		for _, d := range pc.Deps {
			edges[d] = append(edges[d], pc.Name)
			indeg[pc.Name]++
		}
		if _, ok := indeg[pc.Name]; !ok {
			indeg[pc.Name] = 0
		}
	}

	order := topoOrder(edges, indeg)
	return &adapter.GraphInfo{
		Nodes: nodes,
		Edges: edges,
		Order: order,
	}
}

// GetComponents returns the list of all managed components.
func (a *Agent) GetComponents() []store.ComponentInfo {
	return a.comps.List()
}

// StopPlan stops all running components.
func (a *Agent) StopPlan() error {
	a.mu.Lock()
	defer a.mu.Unlock()

	for name, cancel := range a.cancels {
		if cancel != nil {
			cancel()
		}
		delete(a.cancels, name)
	}

	pr := runner.NewProcessRunner()
	for name, h := range a.handles {
		stopCtx, stopCancel := context.WithTimeout(a.Context(), 10*time.Second)
		// Use stored runner if available, otherwise use appropriate runner based on type
		if compRunner, ok := a.runners[name]; ok {
			_ = compRunner.Stop(stopCtx, h, 5*time.Second)
		} else if _, ok := h.(*runner.ProcessHandle); ok {
			_ = pr.Stop(stopCtx, h, 5*time.Second)
		} else {
			// Container without stored runner - try CLI runner
			if clir, err := runner.NewCLIRunner(); err == nil {
				_ = clir.Stop(stopCtx, h, 5*time.Second)
			}
		}
		stopCancel()
		delete(a.handles, name)
		if ci, ok := a.comps.Get(name); ok {
			ci.State = "stopped"
			a.comps.Upsert(ci)
		}
	}
	// Clean up runners
	for name := range a.runners {
		delete(a.runners, name)
	}

	a.planStatus = "stopped"
	return nil
}

// StopComponent stops a single component by name.
func (a *Agent) StopComponent(name string) error {
	a.stopComponent(name)
	return nil
}

// RestartComponent restarts a component and its dependents.
// wait: "pid" (default) or "health"
// timeout: max time to wait for readiness
func (a *Agent) RestartComponent(name string, wait string, timeout time.Duration) (*adapter.RestartResult, error) {
	if wait == "" {
		wait = "pid"
	}
	if wait != "health" {
		wait = "pid"
	}
	if timeout <= 0 {
		timeout = 60 * time.Second
	}

	// Get dependents in topological order
	depsOrder := a.planDependentsTopological(name)
	log.Printf("[handler] component=%s dependents=%v msg=restart: stopping dependents", name, depsOrder)

	// Stop dependents first (reverse dependency order)
	for _, dn := range depsOrder {
		log.Printf("[handler] dependent=%s msg=stopping dependent", dn)
		a.stopComponent(dn)
	}

	// Stop target
	log.Printf("[handler] target=%s msg=stopping target", name)
	a.stopComponent(name)

	// Start target
	log.Printf("[handler] target=%s msg=starting target", name)
	if err := a.restartFromPlan(name); err != nil {
		log.Printf("[handler] target=%s error=%v msg=restart failed to start", name, err)
		return nil, err
	}

	// Wait for target
	log.Printf("[handler] target=%s mode=%s timeout=%v msg=waiting for target", name, wait, timeout)
	if err := a.waitReady(name, wait, timeout); err != nil {
		log.Printf("[handler] target=%s error=%v msg=restart wait failed", name, err)
		return nil, err
	}
	log.Printf("[handler] target=%s pid=%d msg=target ready", name, a.currentPID(name))

	// Start dependents and wait for each
	depPIDs := map[string]int{}
	for _, dn := range depsOrder {
		log.Printf("[handler] dependent=%s msg=starting dependent", dn)
		_ = a.restartFromPlan(dn)
		if err := a.waitReady(dn, wait, timeout); err != nil {
			log.Printf("[handler] dependent=%s error=%v msg=dependent not ready", dn, err)
		}
		if pid := a.currentPID(dn); pid > 0 {
			depPIDs[dn] = pid
		}
	}

	return &adapter.RestartResult{
		Component:  name,
		PID:        a.currentPID(name),
		Dependents: depPIDs,
		Wait:       wait,
		Timeout:    timeout.String(),
	}, nil
}

// RestartComponentDry returns the planned stop/start order without executing.
func (a *Agent) RestartComponentDry(name string) *adapter.RestartDryResult {
	depsOrder := a.planDependentsTopological(name)
	startOrder := append([]string{name}, depsOrder...)
	return &adapter.RestartDryResult{
		StopOrder:  depsOrder,
		StartOrder: startOrder,
	}
}

// GetHealth returns the agent health status.
func (a *Agent) GetHealth() *adapter.HealthStatus {
	return &adapter.HealthStatus{
		Status:  "ok",
		Uptime:  time.Since(a.start).String(),
		Closed:  a.closed.Load(),
		TimeUTC: time.Now().UTC().Format(time.RFC3339),
	}
}
