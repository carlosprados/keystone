package mqtt

import (
	"time"

	"github.com/carlosprados/keystone/internal/adapter"
	"github.com/carlosprados/keystone/internal/store"
)

// Request message types for MQTT commands.

// ApplyRequest is the payload for cmd/apply.
type ApplyRequest struct {
	// CorrelationID is an optional client-provided ID to correlate responses.
	CorrelationID string `json:"correlationId,omitempty"`

	// CommandID identifies this command so a duplicate delivery is executed
	// once. QoS 1 is at-least-once by design: without an id the agent has no
	// way to tell a retry from a second, deliberate apply, and will run both.
	// Distinct from CorrelationID, which may legitimately repeat.
	CommandID string `json:"commandId,omitempty"`

	// Content is the raw TOML content of the plan.
	//
	// There is deliberately no planPath: letting a remote publisher name a file
	// on the device and have it executed is a local-file-inclusion vector, and
	// the HTTP adapter has refused it for that reason for some time. Under an
	// outbound-only deployment this is the control plane, so it is the last
	// place that should accept it.
	Content string `json:"content,omitempty"`

	// Recipes are recipe TOML documents to store (add-recipe with force) BEFORE
	// the plan is reconciled, in the same apply. This lets a controller ship a
	// plan and the recipes it references atomically in one message, removing the
	// add-recipe→apply ordering race (no need for a separate cmd/add-recipe or a
	// sleep between the two). Empty = apply expects the recipes to already exist.
	Recipes []string `json:"recipes,omitempty"`

	// Dry indicates a dry-run (no actual execution).
	Dry bool `json:"dry,omitempty"`
}

// SelfUpdateRequest is the payload for cmd/self-update: replace the agent's
// own binary.
//
// It is the most dangerous message in this protocol — it installs code that
// will run as the agent, on a device nobody can reach — so every field that
// makes it verifiable is required rather than optional.
type SelfUpdateRequest struct {
	CorrelationID string `json:"correlationId,omitempty"`
	CommandID     string `json:"commandId,omitempty"`

	// Version names the new build. It becomes a directory name and the value
	// reported in telemetry, and it is what a rollback points back at.
	Version string `json:"version"`
	// URI is where the binary is fetched from.
	URI string `json:"uri"`
	// SHA256 of the binary. Required: this is the one artifact where "could
	// not check it" must never mean "install it anyway".
	SHA256 string `json:"sha256"`
	// SigURI and CertURI locate the detached signature. Empty means
	// "<uri>.sig" and the device's configured leaf certificate.
	SigURI  string `json:"sigUri,omitempty"`
	CertURI string `json:"certUri,omitempty"`

	// Restart asks the agent to exit once the new version is installed, so the
	// supervisor starts it. Default true: an update that is installed but
	// never started is a trial that never begins, and the device would report
	// the old version indefinitely while looking updated to whoever sent this.
	Restart *bool `json:"restart,omitempty"`
}

// SelfUpdateResponse reports what was staged.
type SelfUpdateResponse struct {
	Version   string `json:"version"`
	Installed bool   `json:"installed"`
	// Restarting says whether the agent is about to exit. When true, this is
	// the last message on this connection until it comes back.
	Restarting bool `json:"restarting"`
}

// RestartRequest is the payload for cmd/restart.
type RestartRequest struct {
	// CorrelationID is an optional client-provided ID to correlate responses.
	CorrelationID string `json:"correlationId,omitempty"`

	// Component is the name of the component to restart.
	Component string `json:"component"`

	// Wait mode: "pid" (default) or "health".
	Wait string `json:"wait,omitempty"`

	// Timeout for waiting (e.g., "60s"). Default: 60s.
	Timeout string `json:"timeout,omitempty"`

	// Dry indicates a dry-run (returns order without executing).
	Dry bool `json:"dry,omitempty"`
}

// StopComponentRequest is the payload for cmd/stop-comp.
type StopComponentRequest struct {
	// CorrelationID is an optional client-provided ID to correlate responses.
	CorrelationID string `json:"correlationId,omitempty"`

	// Component is the name of the component to stop.
	Component string `json:"component"`
}

// AddRecipeRequest is the payload for cmd/add-recipe.
type AddRecipeRequest struct {
	// CorrelationID is an optional client-provided ID to correlate responses.
	CorrelationID string `json:"correlationId,omitempty"`

	// Content is the raw TOML content of the recipe.
	Content string `json:"content"`

	// Force overwrites an existing recipe with the same name/version.
	Force bool `json:"force,omitempty"`
}

// SimpleRequest is used for commands that only need correlation.
type SimpleRequest struct {
	// CorrelationID is an optional client-provided ID to correlate responses.
	CorrelationID string `json:"correlationId,omitempty"`
}

// Response message types for MQTT commands.

// Response is the generic response wrapper.
type Response struct {
	// CorrelationID echoes back the client's correlation ID if provided.
	CorrelationID string `json:"correlationId,omitempty"`

	// Success indicates if the operation succeeded.
	Success bool `json:"success"`

	// Error contains the error message if Success is false.
	Error string `json:"error,omitempty"`

	// Data contains the response payload (type depends on the command).
	Data any `json:"data,omitempty"`
}

// NewSuccessResponse creates a successful response with data.
func NewSuccessResponse(correlationID string, data any) *Response {
	return &Response{
		CorrelationID: correlationID,
		Success:       true,
		Data:          data,
	}
}

// NewErrorResponse creates an error response.
func NewErrorResponse(correlationID string, err error) *Response {
	return &Response{
		CorrelationID: correlationID,
		Success:       false,
		Error:         err.Error(),
	}
}

// Specific response data types.

// StatusResponse is the data for cmd/status response.
type StatusResponse = adapter.PlanStatus

// GraphResponse is the data for cmd/graph response.
type GraphResponse = adapter.GraphInfo

// HealthResponse is the data for cmd/health response.
type HealthResponse = adapter.HealthStatus

// RestartResponse is the data for cmd/restart response.
type RestartResponse = adapter.RestartResult

// RestartDryResponse is the data for cmd/restart (dry) response.
type RestartDryResponse = adapter.RestartDryResult

// ComponentsResponse is the data for listing components.
type ComponentsResponse struct {
	Components []store.ComponentInfo `json:"components"`
}

// RecipesResponse is the data for cmd/recipes response.
type RecipesResponse struct {
	Recipes []string `json:"recipes"`
}

// AddRecipeResponse is the data for cmd/add-recipe response.
type AddRecipeResponse struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Event message types (agent publishes these).

// StateEvent is published when component state changes.
type StateEvent struct {
	Timestamp  time.Time             `json:"timestamp"`
	DeviceID   string                `json:"deviceId"`
	Components []store.ComponentInfo `json:"components"`
	PlanStatus string                `json:"planStatus"`
	PlanPath   string                `json:"planPath,omitempty"`
	// AgentVersion is the build running on the device. On a device reachable
	// only outbound it is the only way to know what is deployed there, so it
	// rides along with the state the device already reports rather than
	// waiting to be asked.
	AgentVersion string `json:"agentVersion,omitempty"`
	// UpdateStatus is "idle" when this install does not update itself,
	// "pending-confirmation" while a new version is on trial, and "confirmed"
	// once it has proved itself. A device stuck in pending-confirmation is one
	// whose next restart will roll it back, and that is worth seeing before it
	// happens rather than after.
	UpdateStatus string `json:"updateStatus,omitempty"`
}

// HealthEvent is published periodically with health status.
type HealthEvent struct {
	Timestamp time.Time `json:"timestamp"`
	DeviceID  string    `json:"deviceId"`
	Status    string    `json:"status"`
	Uptime    string    `json:"uptime"`
}
