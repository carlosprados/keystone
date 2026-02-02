package mqtt

import "fmt"

// Topic patterns for MQTT communication.
// All topics are prefixed with "keystone/{deviceId}/" for multi-tenancy.
const (
	// Command topics (agent subscribes to these)
	TopicCmdApply     = "keystone/%s/cmd/apply"      // Apply a deployment plan
	TopicCmdStop      = "keystone/%s/cmd/stop"       // Stop all components
	TopicCmdStatus    = "keystone/%s/cmd/status"     // Get plan status
	TopicCmdGraph     = "keystone/%s/cmd/graph"      // Get dependency graph
	TopicCmdRestart   = "keystone/%s/cmd/restart"    // Restart a component
	TopicCmdStopComp  = "keystone/%s/cmd/stop-comp"  // Stop a component
	TopicCmdHealth    = "keystone/%s/cmd/health"     // Get health status
	TopicCmdRecipes   = "keystone/%s/cmd/recipes"    // List recipes
	TopicCmdAddRecipe = "keystone/%s/cmd/add-recipe" // Add a recipe

	// Response topics (agent publishes responses here)
	// The response topic is derived from the command: cmd/X -> resp/X
	TopicRespApply     = "keystone/%s/resp/apply"
	TopicRespStop      = "keystone/%s/resp/stop"
	TopicRespStatus    = "keystone/%s/resp/status"
	TopicRespGraph     = "keystone/%s/resp/graph"
	TopicRespRestart   = "keystone/%s/resp/restart"
	TopicRespStopComp  = "keystone/%s/resp/stop-comp"
	TopicRespHealth    = "keystone/%s/resp/health"
	TopicRespRecipes   = "keystone/%s/resp/recipes"
	TopicRespAddRecipe = "keystone/%s/resp/add-recipe"

	// Event topics (agent publishes these)
	TopicEventState  = "keystone/%s/events/state"  // State changes
	TopicEventHealth = "keystone/%s/events/health" // Health updates

	// Wildcard for subscribing to all commands for a device
	TopicCmdWildcard = "keystone/%s/cmd/+"
)

// Topics holds the resolved topic strings for a specific device.
type Topics struct {
	deviceID string

	// Commands (agent subscribes to these)
	CmdApply     string
	CmdStop      string
	CmdStatus    string
	CmdGraph     string
	CmdRestart   string
	CmdStopComp  string
	CmdHealth    string
	CmdRecipes   string
	CmdAddRecipe string
	CmdWildcard  string

	// Responses (agent publishes to these)
	RespApply     string
	RespStop      string
	RespStatus    string
	RespGraph     string
	RespRestart   string
	RespStopComp  string
	RespHealth    string
	RespRecipes   string
	RespAddRecipe string

	// Events (agent publishes to these)
	EventState  string
	EventHealth string
}

// NewTopics creates a Topics instance with the given device ID.
func NewTopics(deviceID string) *Topics {
	return &Topics{
		deviceID: deviceID,

		CmdApply:     fmt.Sprintf(TopicCmdApply, deviceID),
		CmdStop:      fmt.Sprintf(TopicCmdStop, deviceID),
		CmdStatus:    fmt.Sprintf(TopicCmdStatus, deviceID),
		CmdGraph:     fmt.Sprintf(TopicCmdGraph, deviceID),
		CmdRestart:   fmt.Sprintf(TopicCmdRestart, deviceID),
		CmdStopComp:  fmt.Sprintf(TopicCmdStopComp, deviceID),
		CmdHealth:    fmt.Sprintf(TopicCmdHealth, deviceID),
		CmdRecipes:   fmt.Sprintf(TopicCmdRecipes, deviceID),
		CmdAddRecipe: fmt.Sprintf(TopicCmdAddRecipe, deviceID),
		CmdWildcard:  fmt.Sprintf(TopicCmdWildcard, deviceID),

		RespApply:     fmt.Sprintf(TopicRespApply, deviceID),
		RespStop:      fmt.Sprintf(TopicRespStop, deviceID),
		RespStatus:    fmt.Sprintf(TopicRespStatus, deviceID),
		RespGraph:     fmt.Sprintf(TopicRespGraph, deviceID),
		RespRestart:   fmt.Sprintf(TopicRespRestart, deviceID),
		RespStopComp:  fmt.Sprintf(TopicRespStopComp, deviceID),
		RespHealth:    fmt.Sprintf(TopicRespHealth, deviceID),
		RespRecipes:   fmt.Sprintf(TopicRespRecipes, deviceID),
		RespAddRecipe: fmt.Sprintf(TopicRespAddRecipe, deviceID),

		EventState:  fmt.Sprintf(TopicEventState, deviceID),
		EventHealth: fmt.Sprintf(TopicEventHealth, deviceID),
	}
}

// DeviceID returns the device identifier.
func (t *Topics) DeviceID() string {
	return t.deviceID
}

// ResponseTopic returns the response topic for a given command topic.
func (t *Topics) ResponseTopic(cmdTopic string) string {
	switch cmdTopic {
	case t.CmdApply:
		return t.RespApply
	case t.CmdStop:
		return t.RespStop
	case t.CmdStatus:
		return t.RespStatus
	case t.CmdGraph:
		return t.RespGraph
	case t.CmdRestart:
		return t.RespRestart
	case t.CmdStopComp:
		return t.RespStopComp
	case t.CmdHealth:
		return t.RespHealth
	case t.CmdRecipes:
		return t.RespRecipes
	case t.CmdAddRecipe:
		return t.RespAddRecipe
	default:
		return ""
	}
}
