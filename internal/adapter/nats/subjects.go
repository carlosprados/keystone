package nats

import "fmt"

// Subject patterns for NATS communication.
// All subjects are prefixed with "keystone.{deviceId}." for multi-tenancy.
const (
	// Command subjects (Request/Reply pattern)
	SubjectCmdApply      = "keystone.%s.cmd.apply"      // Apply a deployment plan
	SubjectCmdStop       = "keystone.%s.cmd.stop"       // Stop all components
	SubjectCmdStatus     = "keystone.%s.cmd.status"     // Get plan status
	SubjectCmdComponents = "keystone.%s.cmd.components" // Get components list
	SubjectCmdGraph      = "keystone.%s.cmd.graph"      // Get dependency graph
	SubjectCmdRestart    = "keystone.%s.cmd.restart"    // Restart a component
	SubjectCmdStopComp   = "keystone.%s.cmd.stop-comp"  // Stop a component
	SubjectCmdHealth     = "keystone.%s.cmd.health"     // Get health status
	SubjectCmdRecipes    = "keystone.%s.cmd.recipes"    // List recipes
	SubjectCmdAddRecipe  = "keystone.%s.cmd.add-recipe" // Add a recipe

	// Event subjects (Publish pattern - agent publishes these)
	SubjectEventState  = "keystone.%s.events.state"  // State changes
	SubjectEventHealth = "keystone.%s.events.health" // Health updates

	// Wildcard for subscribing to all commands for a device
	SubjectCmdWildcard = "keystone.%s.cmd.>"
)

// Subjects holds the resolved subject strings for a specific device.
type Subjects struct {
	deviceID string

	// Commands (agent subscribes to these)
	CmdApply      string
	CmdStop       string
	CmdStatus     string
	CmdComponents string
	CmdGraph      string
	CmdRestart    string
	CmdStopComp   string
	CmdHealth     string
	CmdRecipes    string
	CmdAddRecipe  string
	CmdWildcard   string

	// Events (agent publishes to these)
	EventState  string
	EventHealth string
}

// NewSubjects creates a Subjects instance with the given device ID.
func NewSubjects(deviceID string) *Subjects {
	return &Subjects{
		deviceID: deviceID,

		CmdApply:      fmt.Sprintf(SubjectCmdApply, deviceID),
		CmdStop:       fmt.Sprintf(SubjectCmdStop, deviceID),
		CmdStatus:     fmt.Sprintf(SubjectCmdStatus, deviceID),
		CmdComponents: fmt.Sprintf(SubjectCmdComponents, deviceID),
		CmdGraph:      fmt.Sprintf(SubjectCmdGraph, deviceID),
		CmdRestart:    fmt.Sprintf(SubjectCmdRestart, deviceID),
		CmdStopComp:   fmt.Sprintf(SubjectCmdStopComp, deviceID),
		CmdHealth:     fmt.Sprintf(SubjectCmdHealth, deviceID),
		CmdRecipes:    fmt.Sprintf(SubjectCmdRecipes, deviceID),
		CmdAddRecipe:  fmt.Sprintf(SubjectCmdAddRecipe, deviceID),
		CmdWildcard:   fmt.Sprintf(SubjectCmdWildcard, deviceID),

		EventState:  fmt.Sprintf(SubjectEventState, deviceID),
		EventHealth: fmt.Sprintf(SubjectEventHealth, deviceID),
	}
}

// DeviceID returns the device identifier.
func (s *Subjects) DeviceID() string {
	return s.deviceID
}
