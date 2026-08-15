// Package adapter defines the interface for control plane adapters.
// Adapters handle communication between the agent and external systems
// (HTTP API, NATS, MQTT, etc.) while delegating business logic to the CommandHandler.
package adapter

import (
	"context"
	"errors"
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

	// ReconcileNow re-applies the plan already in effect, so components that
	// died and exhausted their restart budget are started again.
	//
	// It is deliberately not "call ApplyPlan with the current path". That path
	// enables the rollback, whose "previous plan" would be the very plan that
	// just failed — so a failing pass would stop every healthy component and
	// re-apply the failure. The implementation applies with rollback disabled,
	// and refuses to resurrect a plan an operator stopped. See
	// docs/periodic-reconcile-design.md.
	//
	// A pass that decides to do nothing returns a result with Skipped set and
	// a nil error: "an apply is already running" is an ordinary outcome for a
	// timer, not a failure anyone should act on.
	ReconcileNow() (*ReconcileResult, error)

	// Component operations
	GetComponents() []store.ComponentInfo
	StopComponent(name string) error
	RestartComponent(name string, wait string, timeout time.Duration) (*RestartResult, error)
	RestartComponentDry(name string) *RestartDryResult

	// Recipe operations
	AddRecipe(content string, force bool) (name, version string, err error)
	DeleteRecipe(name, version string) error
	ListRecipes() ([]string, error)

	// Datasets
	DatasetStates() []DatasetInfo
	// RefreshDatasets checks every dataset for a new version immediately,
	// rather than waiting for its interval. Signature verification and the
	// anti-replay rule still apply to each one.
	RefreshDatasets()

	// Health
	GetHealth() *HealthStatus
}

// ErrInvalidInput marks a failure caused by what the caller submitted rather
// than by anything wrong on the device. Transports map it to a client error:
// answering 500 to a malformed plan tells an operator the agent broke, and tells
// automation to retry — which it will do forever, since the file will not fix
// itself in the meantime.
//
// Wrap with %w at the point the input is judged; check with errors.Is.
var ErrInvalidInput = errors.New("invalid input")

// ErrNotReady marks a failure the device expects to recover from on its own,
// without anybody changing what they submitted — the clock being behind
// known-good time under the strict clock policy, for instance, which fixes
// itself the moment NTP runs.
//
// It is the mirror image of ErrInvalidInput and exists for the same reason:
// retrying is exactly the right thing to do here, and exactly the wrong thing
// for a signature that will never validate. Transports map it to 503.
var ErrNotReady = errors.New("not ready")

// PlanStatus represents the current state of the deployment plan.
type PlanStatus struct {
	PlanPath string `json:"planPath"`
	Status   string `json:"status"`
	Error    string `json:"error,omitempty"`
	// UnknownFields lists keys the applied plan and its recipes carry that this
	// agent does not understand, each as `"lifecycle.run.restart_polciy"
	// (line 6)`. It is not an error: the agent may predate the field, and
	// refusing would strand a device over a recipe newer than its binary. It is
	// here so that a typo — which is the same thing seen from the other side —
	// is visible without reading the agent's logs. A dry-run apply refuses
	// outright instead.
	UnknownFields []string `json:"unknownFields,omitempty"`
	// LastReconcile and LastReconcileResult report the most recent reconcile
	// pass, so a periodic reconcile can be seen working — or seen skipping —
	// without reading the agent's logs. Empty until one has run.
	LastReconcile       string                `json:"lastReconcile,omitempty"`
	LastReconcileResult string                `json:"lastReconcileResult,omitempty"`
	Components          []store.ComponentInfo `json:"components"`
}

// ReconcileResult reports what one reconcile pass did.
type ReconcileResult struct {
	// Skipped is true when the pass deliberately changed nothing.
	Skipped bool `json:"skipped"`
	// Reason says why it was skipped, in words an operator can act on.
	Reason string `json:"reason,omitempty"`
	// Repaired names the components that were not running before the pass and
	// are running after it. This is the number that says whether periodic
	// reconcile is earning its keep: a device that repairs nothing and one that
	// repairs the same component nightly are very different situations.
	Repaired []string `json:"repaired,omitempty"`
	// Duration of the pass, as a Go duration string.
	Duration string `json:"duration"`
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
	// ClockTrusted is false when the system clock is behind what the agent can
	// prove has already happened — a device with no RTC, or one whose clock was
	// set back. Signature checks still work (they use the later time), but an
	// operator needs to see this: a fleet running on approximate time is not
	// enforcing certificate expiry the way it looks like it is.
	ClockTrusted bool `json:"clock_trusted"`
	// ClockSource says where the time being used came from: "system",
	// "high-water" (a mark persisted from an earlier run) or "build" (the
	// binary's own build timestamp).
	ClockSource string `json:"clock_source"`
}

// DatasetInfo is what an operator needs to answer "is this device's data
// current?" without reading logs.
type DatasetInfo struct {
	Name        string `json:"name"`
	Version     string `json:"version,omitempty"`
	Published   string `json:"published,omitempty"`
	ManifestURI string `json:"manifestUri,omitempty"`
	Path        string `json:"path,omitempty"`
	LastRefresh string `json:"lastRefresh,omitempty"`
	LastResult  string `json:"lastResult,omitempty"`
	Refresh     string `json:"refresh,omitempty"`
	MaxAge      string `json:"maxAge,omitempty"`
	// AgeSeconds is how old the data is. Stale says it has passed max_age —
	// a scanner working from a six-week-old feed still answers, which is why
	// this is reported rather than left to be inferred.
	AgeSeconds int64 `json:"ageSeconds,omitempty"`
	Stale      bool  `json:"stale"`
	// AgeUnknown is true when the device's clock cannot be trusted, so age
	// cannot be computed. A plausible wrong number would be worse: it would
	// silence the alert that should have fired.
	AgeUnknown bool `json:"ageUnknown,omitempty"`
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
