package mqtt

import (
	"fmt"
	"strings"
)

// Topic patterns for MQTT communication.
// All topics are prefixed with "keystone/{deviceId}/" for multi-tenancy.
const (
	// Command topics (agent subscribes to these)
	TopicCmdApply      = "keystone/%s/cmd/apply"       // Apply a deployment plan
	TopicCmdStop       = "keystone/%s/cmd/stop"        // Stop all components
	TopicCmdStatus     = "keystone/%s/cmd/status"      // Get plan status
	TopicCmdComponents = "keystone/%s/cmd/components"  // Get components list
	TopicCmdGraph      = "keystone/%s/cmd/graph"       // Get dependency graph
	TopicCmdRestart    = "keystone/%s/cmd/restart"     // Restart a component
	TopicCmdStopComp   = "keystone/%s/cmd/stop-comp"   // Stop a component
	TopicCmdHealth     = "keystone/%s/cmd/health"      // Get health status
	TopicCmdRecipes    = "keystone/%s/cmd/recipes"     // List recipes
	TopicCmdAddRecipe  = "keystone/%s/cmd/add-recipe"  // Add a recipe
	TopicCmdSelfUpdate = "keystone/%s/cmd/self-update" // Replace the agent's own binary

	// Response topics (agent publishes responses here)
	// The response topic is derived from the command: cmd/X -> resp/X
	TopicRespApply      = "keystone/%s/resp/apply"
	TopicRespStop       = "keystone/%s/resp/stop"
	TopicRespStatus     = "keystone/%s/resp/status"
	TopicRespComponents = "keystone/%s/resp/components"
	TopicRespGraph      = "keystone/%s/resp/graph"
	TopicRespRestart    = "keystone/%s/resp/restart"
	TopicRespStopComp   = "keystone/%s/resp/stop-comp"
	TopicRespHealth     = "keystone/%s/resp/health"
	TopicRespRecipes    = "keystone/%s/resp/recipes"
	TopicRespAddRecipe  = "keystone/%s/resp/add-recipe"
	TopicRespSelfUpdate = "keystone/%s/resp/self-update"

	// Event topics (agent publishes these)
	TopicStatus      = "keystone/%s/status"        // Presence: last will and "online"
	TopicEventState  = "keystone/%s/events/state"  // State changes
	TopicEventHealth = "keystone/%s/events/health" // Health updates

	// Wildcard for subscribing to all commands for a device
	TopicCmdWildcard = "keystone/%s/cmd/+"
)

// Topics holds the resolved topic strings for a specific device.
type Topics struct {
	deviceID string

	// Status carries presence: the last will's "offline" and "online".
	Status string

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
	CmdSelfUpdate string
	CmdWildcard   string

	// Responses (agent publishes to these)
	RespApply      string
	RespStop       string
	RespStatus     string
	RespComponents string
	RespGraph      string
	RespRestart    string
	RespStopComp   string
	RespHealth     string
	RespRecipes    string
	RespAddRecipe  string
	RespSelfUpdate string

	// Events (agent publishes to these)
	EventState  string
	EventHealth string
}

// DevicePath is the part of every topic that names the device:
// "<device>" or, with a tenant, "<tenant>/<device>".
//
// Without a tenant the path is exactly what it has always been, so existing
// brokers, ACLs and controllers keep working unchanged. A new default would
// have broken the ACL and the command channel together, and under MQTT 3.1.1 a
// publish the ACL denies is dropped silently: the device would simply go mute.
func DevicePath(tenant, deviceID string) string {
	if tenant == "" {
		return deviceID
	}
	return tenant + "/" + deviceID
}

// ValidateTenant refuses a tenant that would not stay one topic level: a "/"
// would shift every topic, and "+" or "#" would turn the device's subscription
// into a wildcard over other tenants' devices.
func ValidateTenant(v string) error {
	if strings.TrimSpace(v) != v || v == "" {
		return fmt.Errorf("tenant %q must be non-empty with no surrounding spaces", v)
	}
	if strings.ContainsAny(v, "/+#\x00") {
		return fmt.Errorf("tenant %q must be a single MQTT topic level: no '/', '+', '#' or NUL", v)
	}
	return nil
}

// ValidateDeviceID refuses the characters that make a topic a wildcard. A "/"
// is still allowed: installs already use it, and it only adds levels.
func ValidateDeviceID(v string) error {
	if strings.TrimSpace(v) == "" {
		return fmt.Errorf("device ID must not be empty")
	}
	if strings.ContainsAny(v, "+#\x00") {
		return fmt.Errorf("device ID %q must not contain '+', '#' or NUL: they would make the device subscribe to other devices' commands", v)
	}
	return nil
}

// NewTopics creates the topics for a device, under a tenant when one is set.
func NewTopics(tenant, deviceID string) *Topics {
	path := DevicePath(tenant, deviceID)
	return &Topics{
		deviceID: deviceID,
		Status:   fmt.Sprintf(TopicStatus, path),

		CmdApply:      fmt.Sprintf(TopicCmdApply, path),
		CmdStop:       fmt.Sprintf(TopicCmdStop, path),
		CmdStatus:     fmt.Sprintf(TopicCmdStatus, path),
		CmdComponents: fmt.Sprintf(TopicCmdComponents, path),
		CmdGraph:      fmt.Sprintf(TopicCmdGraph, path),
		CmdRestart:    fmt.Sprintf(TopicCmdRestart, path),
		CmdStopComp:   fmt.Sprintf(TopicCmdStopComp, path),
		CmdHealth:     fmt.Sprintf(TopicCmdHealth, path),
		CmdRecipes:    fmt.Sprintf(TopicCmdRecipes, path),
		CmdAddRecipe:  fmt.Sprintf(TopicCmdAddRecipe, path),
		CmdSelfUpdate: fmt.Sprintf(TopicCmdSelfUpdate, path),
		CmdWildcard:   fmt.Sprintf(TopicCmdWildcard, path),

		RespApply:      fmt.Sprintf(TopicRespApply, path),
		RespStop:       fmt.Sprintf(TopicRespStop, path),
		RespStatus:     fmt.Sprintf(TopicRespStatus, path),
		RespComponents: fmt.Sprintf(TopicRespComponents, path),
		RespGraph:      fmt.Sprintf(TopicRespGraph, path),
		RespRestart:    fmt.Sprintf(TopicRespRestart, path),
		RespStopComp:   fmt.Sprintf(TopicRespStopComp, path),
		RespHealth:     fmt.Sprintf(TopicRespHealth, path),
		RespRecipes:    fmt.Sprintf(TopicRespRecipes, path),
		RespAddRecipe:  fmt.Sprintf(TopicRespAddRecipe, path),
		RespSelfUpdate: fmt.Sprintf(TopicRespSelfUpdate, path),

		EventState:  fmt.Sprintf(TopicEventState, path),
		EventHealth: fmt.Sprintf(TopicEventHealth, path),
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
	case t.CmdComponents:
		return t.RespComponents
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
	case t.CmdSelfUpdate:
		return t.RespSelfUpdate
	default:
		return ""
	}
}
