// Package adapter defines the interface for control plane adapters.
// Adapters handle communication between the agent and external systems
// (HTTP API, NATS, MQTT, etc.) while delegating business logic to the CommandHandler.
package adapter

import (
	"context"
	"time"

	"github.com/carlosprados/keystone/internal/store"
)

// Adapter defines the interface for control plane adapters.
// Each adapter handles a specific transport protocol (HTTP, NATS, MQTT, etc.)
// and translates external requests into CommandHandler calls.
type Adapter interface {
	// Name returns the adapter identifier for logging and diagnostics.
	Name() string

	// Start initializes the adapter (connections, listeners, subscriptions).
	// It should block until the adapter is ready to receive requests.
	Start(ctx context.Context) error

	// Stop gracefully shuts down the adapter.
	// It should close connections and release resources.
	Stop(ctx context.Context) error
}

// CommandHandler defines the operations that any adapter can invoke.
// This interface is implemented by the Agent and provides all business logic.
type CommandHandler interface {
	// Plan operations
	ApplyPlan(planPath string, dry bool) error
	ApplyPlanContent(content string, dry bool) error
	StopPlan() error
	GetPlanStatus() *PlanStatus
	GetPlanGraph() *GraphInfo

	// Component operations
	GetComponents() []store.ComponentInfo
	StopComponent(name string) error
	RestartComponent(name string, wait string, timeout time.Duration) (*RestartResult, error)
	RestartComponentDry(name string) *RestartDryResult

	// Recipe operations
	AddRecipe(content string, force bool) (name, version string, err error)
	DeleteRecipe(name, version string) error
	ListRecipes() ([]string, error)

	// Health
	GetHealth() *HealthStatus
}

// PlanStatus represents the current state of the deployment plan.
type PlanStatus struct {
	PlanPath   string                `json:"planPath"`
	Status     string                `json:"status"`
	Error      string                `json:"error,omitempty"`
	Components []store.ComponentInfo `json:"components"`
}

// GraphInfo represents the dependency graph of the current plan.
type GraphInfo struct {
	Nodes []string            `json:"nodes"`
	Edges map[string][]string `json:"edges"`
	Order []string            `json:"order"`
}

// RestartResult contains the outcome of a component restart operation.
type RestartResult struct {
	Component  string         `json:"component"`
	PID        int            `json:"pid"`
	Dependents map[string]int `json:"dependents"`
	Wait       string         `json:"wait"`
	Timeout    string         `json:"timeout"`
}

// RestartDryResult contains the planned stop/start order for a restart (dry-run).
type RestartDryResult struct {
	StopOrder  []string `json:"stopOrder"`
	StartOrder []string `json:"startOrder"`
}

// HealthStatus represents the agent health check response.
type HealthStatus struct {
	Status  string `json:"status"`
	Uptime  string `json:"uptime"`
	Closed  bool   `json:"closed"`
	TimeUTC string `json:"time_utc"`
}

// Registry manages multiple adapters for the agent.
type Registry struct {
	adapters []Adapter
}

// NewRegistry creates a new adapter registry.
func NewRegistry() *Registry {
	return &Registry{
		adapters: make([]Adapter, 0),
	}
}

// Register adds an adapter to the registry.
func (r *Registry) Register(a Adapter) {
	r.adapters = append(r.adapters, a)
}

// StartAll starts all registered adapters.
// Returns the first error encountered, but attempts to start all adapters.
func (r *Registry) StartAll(ctx context.Context) error {
	var firstErr error
	for _, a := range r.adapters {
		if err := a.Start(ctx); err != nil {
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// StopAll stops all registered adapters in reverse order.
func (r *Registry) StopAll(ctx context.Context) error {
	var firstErr error
	// Stop in reverse order (LIFO)
	for i := len(r.adapters) - 1; i >= 0; i-- {
		if err := r.adapters[i].Stop(ctx); err != nil {
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// List returns the names of all registered adapters.
func (r *Registry) List() []string {
	names := make([]string, len(r.adapters))
	for i, a := range r.adapters {
		names[i] = a.Name()
	}
	return names
}
