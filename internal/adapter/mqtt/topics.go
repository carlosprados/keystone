package mqtt

import (
	"fmt"
	"regexp"
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
// "<tenant>/<device>".
func DevicePath(tenant, deviceID string) string { return tenant + "/" + deviceID }

// The identity rules are the control plane's, exactly. With looser rules here,
// the agent would accept a name the control plane refuses, and under MQTT
// 3.1.1 its messages would then be dropped without either side saying so.
var (
	tenantRule = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	deviceRule = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
)

// reservedDeviceIDs would read as topic keywords, or as path navigation.
var reservedDeviceIDs = map[string]bool{".": true, "..": true, "cmd": true, "resp": true}

// ValidateTenant accepts a DNS label: lowercase letters, digits and inner
// hyphens, at most 63 characters. Required: every topic is under a tenant.
func ValidateTenant(v string) error {
	if v == "" {
		return fmt.Errorf("a tenant is required (--mqtt-tenant, KEYSTONE_MQTT_TENANT, or an enrolment): every topic is keystone/<tenant>/<device>/…")
	}
	if !tenantRule.MatchString(v) {
		return fmt.Errorf("tenant %q must be a DNS label: lowercase letters, digits and inner hyphens, at most 63 characters", v)
	}
	return nil
}

// ValidateDeviceID accepts letters, digits, ".", "_" and "-", 1 to 128
// characters, except the reserved ".", "..", "cmd" and "resp". A "/" is not
// allowed: it would add topic levels and change which filters the device's
// topics match.
func ValidateDeviceID(v string) error {
	if !deviceRule.MatchString(v) {
		return fmt.Errorf("device ID %q must be 1 to 128 of A-Z a-z 0-9 . _ -", v)
	}
	if reservedDeviceIDs[v] {
		return fmt.Errorf("device ID %q is reserved", v)
	}
	return nil
}

// NewTopics creates the topics for a device under its tenant.
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
