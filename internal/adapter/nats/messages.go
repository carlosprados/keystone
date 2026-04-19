package nats

import (
	"time"

	"github.com/carlosprados/keystone/internal/adapter"
	"github.com/carlosprados/keystone/internal/store"
)

// Request message types for NATS commands.

// ApplyRequest is the payload for cmd.apply.
type ApplyRequest struct {
	// PlanPath is the path to a plan file on the agent's filesystem.
	// If empty, Content should contain the plan TOML.
	PlanPath string `json:"planPath,omitempty"`

	// Content is the raw TOML content of the plan.
	// Used when PlanPath is empty.
	Content string `json:"content,omitempty"`

	// Dry indicates a dry-run (no actual execution).
	Dry bool `json:"dry,omitempty"`
}

// RestartRequest is the payload for cmd.restart.
type RestartRequest struct {
	// Component is the name of the component to restart.
	Component string `json:"component"`

	// Wait mode: "pid" (default) or "health".
	Wait string `json:"wait,omitempty"`

	// Timeout for waiting (e.g., "60s"). Default: 60s.
	Timeout string `json:"timeout,omitempty"`

	// Dry indicates a dry-run (returns order without executing).
	Dry bool `json:"dry,omitempty"`
}

// StopComponentRequest is the payload for cmd.stop-comp.
type StopComponentRequest struct {
	// Component is the name of the component to stop.
	Component string `json:"component"`
}

// AddRecipeRequest is the payload for cmd.add-recipe.
type AddRecipeRequest struct {
	// Content is the raw TOML content of the recipe.
	Content string `json:"content"`

	// Force overwrites an existing recipe with the same name/version.
	Force bool `json:"force,omitempty"`
}

// Response message types for NATS commands.

// Response is the generic response wrapper.
type Response struct {
	// Success indicates if the operation succeeded.
	Success bool `json:"success"`

	// Error contains the error message if Success is false.
	Error string `json:"error,omitempty"`

	// Data contains the response payload (type depends on the command).
	Data any `json:"data,omitempty"`
}

// NewSuccessResponse creates a successful response with data.
func NewSuccessResponse(data any) *Response {
	return &Response{
		Success: true,
		Data:    data,
	}
}

// NewErrorResponse creates an error response.
func NewErrorResponse(err error) *Response {
	return &Response{
		Success: false,
		Error:   err.Error(),
	}
}

// Specific response data types.

// StatusResponse is the data for cmd.status response.
type StatusResponse = adapter.PlanStatus

// GraphResponse is the data for cmd.graph response.
type GraphResponse = adapter.GraphInfo

// HealthResponse is the data for cmd.health response.
type HealthResponse = adapter.HealthStatus

// RestartResponse is the data for cmd.restart response.
type RestartResponse = adapter.RestartResult

// RestartDryResponse is the data for cmd.restart (dry) response.
type RestartDryResponse = adapter.RestartDryResult

// ComponentsResponse is the data for listing components.
type ComponentsResponse struct {
	Components []store.ComponentInfo `json:"components"`
}

// RecipesResponse is the data for cmd.recipes response.
type RecipesResponse struct {
	Recipes []string `json:"recipes"`
}

// AddRecipeResponse is the data for cmd.add-recipe response.
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
}

// HealthEvent is published periodically with health status.
type HealthEvent struct {
	Timestamp time.Time `json:"timestamp"`
	DeviceID  string    `json:"deviceId"`
	Status    string    `json:"status"`
	Uptime    string    `json:"uptime"`
}
