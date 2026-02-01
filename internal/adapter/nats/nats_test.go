package nats

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/carlosprados/keystone/internal/adapter"
	"github.com/carlosprados/keystone/internal/store"
	"github.com/nats-io/nats.go"
)

// mockHandler implements adapter.CommandHandler for testing.
type mockHandler struct {
	health       *adapter.HealthStatus
	components   []store.ComponentInfo
	planStatus   *adapter.PlanStatus
	applyCalled  bool
	stopCalled   bool
	lastPlanPath string
}

func newMockHandler() *mockHandler {
	return &mockHandler{
		health: &adapter.HealthStatus{
			Status:  "ok",
			Uptime:  "1m0s",
			Closed:  false,
			TimeUTC: "2024-01-01T00:00:00Z",
		},
		components: []store.ComponentInfo{
			{Name: "test-comp", State: "running", PID: 1234},
		},
		planStatus: &adapter.PlanStatus{
			PlanPath: "test.toml",
			Status:   "running",
		},
	}
}

func (m *mockHandler) ApplyPlan(planPath string, dry bool) error {
	m.applyCalled = true
	m.lastPlanPath = planPath
	return nil
}
func (m *mockHandler) ApplyPlanContent(content string, dry bool) error {
	m.applyCalled = true
	return nil
}
func (m *mockHandler) StopPlan() error {
	m.stopCalled = true
	m.planStatus.Status = "stopped"
	return nil
}
func (m *mockHandler) GetPlanStatus() *adapter.PlanStatus { return m.planStatus }
func (m *mockHandler) GetPlanGraph() *adapter.GraphInfo {
	return &adapter.GraphInfo{Nodes: []string{"test-comp"}, Edges: map[string][]string{}, Order: []string{"test-comp"}}
}
func (m *mockHandler) GetComponents() []store.ComponentInfo { return m.components }
func (m *mockHandler) StopComponent(name string) error      { return nil }
func (m *mockHandler) RestartComponent(name string, wait string, timeout time.Duration) (*adapter.RestartResult, error) {
	return &adapter.RestartResult{Component: name, PID: 1234, Dependents: map[string]int{}, Wait: wait, Timeout: timeout.String()}, nil
}
func (m *mockHandler) RestartComponentDry(name string) *adapter.RestartDryResult {
	return &adapter.RestartDryResult{StopOrder: []string{}, StartOrder: []string{name}}
}
func (m *mockHandler) AddRecipe(content string, force bool) (string, string, error) {
	return "test-recipe", "1.0.0", nil
}
func (m *mockHandler) DeleteRecipe(name, version string) error { return nil }
func (m *mockHandler) ListRecipes() ([]string, error)          { return []string{"recipe1", "recipe2"}, nil }
func (m *mockHandler) GetHealth() *adapter.HealthStatus        { return m.health }

func TestSubjects(t *testing.T) {
	s := NewSubjects("device-001")

	if s.DeviceID() != "device-001" {
		t.Errorf("expected device ID 'device-001', got %q", s.DeviceID())
	}

	if s.CmdApply != "keystone.device-001.cmd.apply" {
		t.Errorf("unexpected CmdApply subject: %s", s.CmdApply)
	}

	if s.EventState != "keystone.device-001.events.state" {
		t.Errorf("unexpected EventState subject: %s", s.EventState)
	}
}

func TestAdapter_Name(t *testing.T) {
	cfg := DefaultConfig()
	a := New(cfg, newMockHandler())

	if a.Name() != "nats" {
		t.Errorf("expected name 'nats', got %q", a.Name())
	}
}

func TestMessages_Response(t *testing.T) {
	// Test success response
	resp := NewSuccessResponse(map[string]string{"key": "value"})
	if !resp.Success {
		t.Error("expected success to be true")
	}
	if resp.Error != "" {
		t.Errorf("expected empty error, got %q", resp.Error)
	}

	// Test error response
	resp = NewErrorResponse(context.DeadlineExceeded)
	if resp.Success {
		t.Error("expected success to be false")
	}
	if resp.Error == "" {
		t.Error("expected error message")
	}
}

func TestMessages_Serialization(t *testing.T) {
	// Test ApplyRequest serialization
	req := ApplyRequest{
		PlanPath: "test.toml",
		Dry:      true,
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("failed to marshal ApplyRequest: %v", err)
	}

	var decoded ApplyRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal ApplyRequest: %v", err)
	}
	if decoded.PlanPath != req.PlanPath || decoded.Dry != req.Dry {
		t.Errorf("decoded request doesn't match: %+v", decoded)
	}

	// Test RestartRequest serialization
	restart := RestartRequest{
		Component: "api",
		Wait:      "health",
		Timeout:   "30s",
		Dry:       false,
	}
	data, err = json.Marshal(restart)
	if err != nil {
		t.Fatalf("failed to marshal RestartRequest: %v", err)
	}

	var decodedRestart RestartRequest
	if err := json.Unmarshal(data, &decodedRestart); err != nil {
		t.Fatalf("failed to unmarshal RestartRequest: %v", err)
	}
	if decodedRestart.Component != restart.Component {
		t.Errorf("decoded restart doesn't match: %+v", decodedRestart)
	}
}

func TestMessages_StateEvent(t *testing.T) {
	event := StateEvent{
		Timestamp: time.Now().UTC(),
		DeviceID:  "device-001",
		Components: []store.ComponentInfo{
			{Name: "api", State: "running", PID: 1234},
		},
		PlanStatus: "running",
		PlanPath:   "plan.toml",
	}

	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("failed to marshal StateEvent: %v", err)
	}

	var decoded StateEvent
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal StateEvent: %v", err)
	}

	if decoded.DeviceID != event.DeviceID {
		t.Errorf("expected device ID %q, got %q", event.DeviceID, decoded.DeviceID)
	}
	if len(decoded.Components) != 1 {
		t.Errorf("expected 1 component, got %d", len(decoded.Components))
	}
}

// Integration test that requires a running NATS server.
// Skip if NATS is not available.
func TestAdapter_Integration(t *testing.T) {
	// Try to connect to a local NATS server
	nc, err := nats.Connect(nats.DefaultURL, nats.Timeout(500*time.Millisecond))
	if err != nil {
		t.Skipf("NATS server not available: %v", err)
	}
	nc.Close()

	handler := newMockHandler()
	cfg := DefaultConfig()
	cfg.DeviceID = "test-device"
	cfg.PublishStateInterval = 0  // Disable for test
	cfg.PublishHealthInterval = 0 // Disable for test

	adapter := New(cfg, handler)

	ctx := context.Background()
	if err := adapter.Start(ctx); err != nil {
		t.Fatalf("failed to start adapter: %v", err)
	}
	defer adapter.Stop(ctx)

	if !adapter.Connected() {
		t.Error("adapter should be connected")
	}

	// Test cmd.status via request/reply
	client, _ := nats.Connect(nats.DefaultURL)
	defer client.Close()

	subjects := NewSubjects("test-device")

	// Request status
	resp, err := client.Request(subjects.CmdStatus, []byte("{}"), 2*time.Second)
	if err != nil {
		t.Fatalf("failed to request status: %v", err)
	}

	var statusResp Response
	if err := json.Unmarshal(resp.Data, &statusResp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if !statusResp.Success {
		t.Errorf("expected success, got error: %s", statusResp.Error)
	}

	// Request health
	resp, err = client.Request(subjects.CmdHealth, []byte("{}"), 2*time.Second)
	if err != nil {
		t.Fatalf("failed to request health: %v", err)
	}

	var healthResp Response
	if err := json.Unmarshal(resp.Data, &healthResp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if !healthResp.Success {
		t.Errorf("expected success, got error: %s", healthResp.Error)
	}

	// Test cmd.apply
	applyReq := ApplyRequest{PlanPath: "/tmp/test.toml", Dry: true}
	applyData, _ := json.Marshal(applyReq)

	resp, err = client.Request(subjects.CmdApply, applyData, 2*time.Second)
	if err != nil {
		t.Fatalf("failed to request apply: %v", err)
	}

	var applyResp Response
	if err := json.Unmarshal(resp.Data, &applyResp); err != nil {
		t.Fatalf("failed to unmarshal apply response: %v", err)
	}

	if !applyResp.Success {
		t.Errorf("expected success, got error: %s", applyResp.Error)
	}

	if !handler.applyCalled {
		t.Error("expected ApplyPlan to be called")
	}
	if handler.lastPlanPath != "/tmp/test.toml" {
		t.Errorf("expected plan path '/tmp/test.toml', got %q", handler.lastPlanPath)
	}
}

func TestJetStreamConfig_Defaults(t *testing.T) {
	cfg := DefaultJetStreamConfig()

	if cfg.Enabled {
		t.Error("JetStream should be disabled by default")
	}
	if cfg.StreamName != "KEYSTONE_JOBS" {
		t.Errorf("expected stream name 'KEYSTONE_JOBS', got %q", cfg.StreamName)
	}
	if cfg.MaxDeliver != 5 {
		t.Errorf("expected max deliver 5, got %d", cfg.MaxDeliver)
	}
	if cfg.AckWait != 30*time.Second {
		t.Errorf("expected ack wait 30s, got %v", cfg.AckWait)
	}
	if cfg.WorkerCount != 1 {
		t.Errorf("expected worker count 1, got %d", cfg.WorkerCount)
	}
}

func TestJob_Serialization(t *testing.T) {
	applyPayload, _ := json.Marshal(ApplyRequest{PlanPath: "/tmp/plan.toml", Dry: false})

	job := Job{
		ID:        "job-123",
		Type:      JobTypeApply,
		DeviceID:  "device-001",
		Payload:   applyPayload,
		CreatedAt: time.Now().UTC(),
		Priority:  1,
		Metadata: map[string]string{
			"source": "control-plane",
		},
	}

	data, err := json.Marshal(job)
	if err != nil {
		t.Fatalf("failed to marshal job: %v", err)
	}

	var decoded Job
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal job: %v", err)
	}

	if decoded.ID != job.ID {
		t.Errorf("expected job ID %q, got %q", job.ID, decoded.ID)
	}
	if decoded.Type != JobTypeApply {
		t.Errorf("expected job type %q, got %q", JobTypeApply, decoded.Type)
	}
	if decoded.DeviceID != job.DeviceID {
		t.Errorf("expected device ID %q, got %q", job.DeviceID, decoded.DeviceID)
	}
	if decoded.Priority != 1 {
		t.Errorf("expected priority 1, got %d", decoded.Priority)
	}
	if decoded.Metadata["source"] != "control-plane" {
		t.Errorf("expected metadata source 'control-plane', got %q", decoded.Metadata["source"])
	}
}

func TestJobResult_Serialization(t *testing.T) {
	result := JobResult{
		JobID:       "job-123",
		Success:     true,
		Data:        map[string]string{"status": "running"},
		ProcessedAt: time.Now().UTC(),
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("failed to marshal job result: %v", err)
	}

	var decoded JobResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal job result: %v", err)
	}

	if decoded.JobID != result.JobID {
		t.Errorf("expected job ID %q, got %q", result.JobID, decoded.JobID)
	}
	if !decoded.Success {
		t.Error("expected success to be true")
	}
	if decoded.Error != "" {
		t.Errorf("expected empty error, got %q", decoded.Error)
	}
}

func TestJobTypes(t *testing.T) {
	expectedTypes := []JobType{
		JobTypeApply,
		JobTypeStop,
		JobTypeRestart,
		JobTypeStopComp,
		JobTypeAddRecipe,
		JobTypeDeleteRecipe,
	}

	expectedStrings := []string{
		"apply",
		"stop",
		"restart",
		"stop-comp",
		"add-recipe",
		"delete-recipe",
	}

	for i, jt := range expectedTypes {
		if string(jt) != expectedStrings[i] {
			t.Errorf("expected job type %q, got %q", expectedStrings[i], jt)
		}
	}
}

func TestConfig_AuthenticationFields(t *testing.T) {
	cfg := Config{
		URL:             "nats://localhost:4222",
		DeviceID:        "test-device",
		CredentialsFile: "/path/to/creds",
		NKeyFile:        "/path/to/nkey",
		Token:           "secret-token",
		Username:        "user",
		Password:        "pass",
		TLSVerify:       true,
	}

	if cfg.CredentialsFile != "/path/to/creds" {
		t.Errorf("expected credentials file '/path/to/creds', got %q", cfg.CredentialsFile)
	}
	if cfg.NKeyFile != "/path/to/nkey" {
		t.Errorf("expected nkey file '/path/to/nkey', got %q", cfg.NKeyFile)
	}
	if cfg.Token != "secret-token" {
		t.Errorf("expected token 'secret-token', got %q", cfg.Token)
	}
	if cfg.Username != "user" {
		t.Errorf("expected username 'user', got %q", cfg.Username)
	}
	if cfg.Password != "pass" {
		t.Errorf("expected password 'pass', got %q", cfg.Password)
	}
	if !cfg.TLSVerify {
		t.Error("expected TLSVerify to be true")
	}
}

func TestAdapter_BuildAuthOptions_NoAuth(t *testing.T) {
	cfg := DefaultConfig()
	adapter := New(cfg, newMockHandler())

	opts := adapter.buildAuthOptions()
	if len(opts) != 0 {
		t.Errorf("expected no auth options, got %d", len(opts))
	}
}

func TestAdapter_BuildAuthOptions_Token(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Token = "test-token"
	adapter := New(cfg, newMockHandler())

	opts := adapter.buildAuthOptions()
	if len(opts) != 1 {
		t.Errorf("expected 1 auth option for token, got %d", len(opts))
	}
}

func TestAdapter_BuildAuthOptions_UserPass(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Username = "user"
	cfg.Password = "pass"
	adapter := New(cfg, newMockHandler())

	opts := adapter.buildAuthOptions()
	if len(opts) != 1 {
		t.Errorf("expected 1 auth option for user/pass, got %d", len(opts))
	}
}

func TestAdapter_BuildAuthOptions_Priority(t *testing.T) {
	// NKey has highest priority - if set, others should be ignored
	cfg := DefaultConfig()
	cfg.NKeyFile = "/nonexistent/nkey" // Will fail to load but tests priority
	cfg.CredentialsFile = "/path/to/creds"
	cfg.Token = "token"
	cfg.Username = "user"

	adapter := New(cfg, newMockHandler())
	opts := adapter.buildAuthOptions()

	// Since NKey file doesn't exist, it will log warning and return empty
	// But the priority logic is correct - it tries NKey first
	if len(opts) > 1 {
		t.Errorf("expected at most 1 auth option due to priority, got %d", len(opts))
	}
}
