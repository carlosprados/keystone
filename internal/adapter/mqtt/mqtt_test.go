package mqtt

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	assert.Equal(t, "tcp://localhost:1883", cfg.Broker)
	assert.Equal(t, "keystone-agent", cfg.DeviceID)
	assert.Equal(t, 30*time.Second, cfg.KeepAlive)
	assert.Equal(t, 30*time.Second, cfg.ConnectTimeout)
	assert.True(t, cfg.CleanSession)
	assert.True(t, cfg.AutoReconnect)
	assert.Equal(t, 5*time.Minute, cfg.MaxReconnectWait)
	assert.Equal(t, byte(1), cfg.CommandQoS)
	assert.Equal(t, byte(1), cfg.ResponseQoS)
	assert.Equal(t, byte(0), cfg.EventQoS)
	assert.Equal(t, 10*time.Second, cfg.PublishStateInterval)
	assert.Equal(t, 30*time.Second, cfg.PublishHealthInterval)
	assert.True(t, cfg.LWTEnabled)
	assert.True(t, cfg.LWTRetain)
	assert.True(t, cfg.TLSVerify)
}

func TestNewTopics(t *testing.T) {
	topics := NewTopics("device-123")

	assert.Equal(t, "device-123", topics.DeviceID())
	assert.Equal(t, "keystone/device-123/cmd/apply", topics.CmdApply)
	assert.Equal(t, "keystone/device-123/cmd/stop", topics.CmdStop)
	assert.Equal(t, "keystone/device-123/cmd/status", topics.CmdStatus)
	assert.Equal(t, "keystone/device-123/cmd/graph", topics.CmdGraph)
	assert.Equal(t, "keystone/device-123/cmd/restart", topics.CmdRestart)
	assert.Equal(t, "keystone/device-123/cmd/stop-comp", topics.CmdStopComp)
	assert.Equal(t, "keystone/device-123/cmd/health", topics.CmdHealth)
	assert.Equal(t, "keystone/device-123/cmd/recipes", topics.CmdRecipes)
	assert.Equal(t, "keystone/device-123/cmd/add-recipe", topics.CmdAddRecipe)
	assert.Equal(t, "keystone/device-123/cmd/+", topics.CmdWildcard)

	assert.Equal(t, "keystone/device-123/resp/apply", topics.RespApply)
	assert.Equal(t, "keystone/device-123/resp/stop", topics.RespStop)
	assert.Equal(t, "keystone/device-123/resp/status", topics.RespStatus)
	assert.Equal(t, "keystone/device-123/resp/graph", topics.RespGraph)
	assert.Equal(t, "keystone/device-123/resp/restart", topics.RespRestart)
	assert.Equal(t, "keystone/device-123/resp/stop-comp", topics.RespStopComp)
	assert.Equal(t, "keystone/device-123/resp/health", topics.RespHealth)
	assert.Equal(t, "keystone/device-123/resp/recipes", topics.RespRecipes)
	assert.Equal(t, "keystone/device-123/resp/add-recipe", topics.RespAddRecipe)

	assert.Equal(t, "keystone/device-123/events/state", topics.EventState)
	assert.Equal(t, "keystone/device-123/events/health", topics.EventHealth)
}

func TestTopicsResponseTopic(t *testing.T) {
	topics := NewTopics("device-123")

	tests := []struct {
		cmdTopic string
		expected string
	}{
		{topics.CmdApply, topics.RespApply},
		{topics.CmdStop, topics.RespStop},
		{topics.CmdStatus, topics.RespStatus},
		{topics.CmdGraph, topics.RespGraph},
		{topics.CmdRestart, topics.RespRestart},
		{topics.CmdStopComp, topics.RespStopComp},
		{topics.CmdHealth, topics.RespHealth},
		{topics.CmdRecipes, topics.RespRecipes},
		{topics.CmdAddRecipe, topics.RespAddRecipe},
		{"unknown/topic", ""},
	}

	for _, tc := range tests {
		t.Run(tc.cmdTopic, func(t *testing.T) {
			result := topics.ResponseTopic(tc.cmdTopic)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestNewSuccessResponse(t *testing.T) {
	data := map[string]string{"key": "value"}
	resp := NewSuccessResponse("corr-123", data)

	assert.True(t, resp.Success)
	assert.Equal(t, "corr-123", resp.CorrelationID)
	assert.Equal(t, "", resp.Error)
	assert.Equal(t, data, resp.Data)
}

func TestNewErrorResponse(t *testing.T) {
	err := assert.AnError
	resp := NewErrorResponse("corr-456", err)

	assert.False(t, resp.Success)
	assert.Equal(t, "corr-456", resp.CorrelationID)
	assert.Equal(t, err.Error(), resp.Error)
	assert.Nil(t, resp.Data)
}

func TestAdapterName(t *testing.T) {
	cfg := DefaultConfig()
	adapter := New(cfg, nil)

	assert.Equal(t, "mqtt", adapter.Name())
}

func TestAdapterNotConnectedInitially(t *testing.T) {
	cfg := DefaultConfig()
	adapter := New(cfg, nil)

	assert.False(t, adapter.Connected())
}
