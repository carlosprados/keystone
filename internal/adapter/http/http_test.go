package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/carlosprados/keystone/internal/adapter"
	"github.com/carlosprados/keystone/internal/store"
)

// mockHandler implements adapter.CommandHandler for testing.
type mockHandler struct {
	health     *adapter.HealthStatus
	components []store.ComponentInfo
	planStatus *adapter.PlanStatus
}

func (m *mockHandler) ApplyPlan(planPath string, dry bool) error       { return nil }
func (m *mockHandler) ApplyPlanContent(content string, dry bool) error { return nil }
func (m *mockHandler) StopPlan() error                                 { return nil }
func (m *mockHandler) GetPlanStatus() *adapter.PlanStatus              { return m.planStatus }
func (m *mockHandler) GetPlanGraph() *adapter.GraphInfo                { return &adapter.GraphInfo{} }
func (m *mockHandler) GetComponents() []store.ComponentInfo            { return m.components }
func (m *mockHandler) StopComponent(name string) error                 { return nil }
func (m *mockHandler) RestartComponent(name string, wait string, timeout time.Duration) (*adapter.RestartResult, error) {
	return &adapter.RestartResult{Component: name, PID: 1234}, nil
}
func (m *mockHandler) RestartComponentDry(name string) *adapter.RestartDryResult {
	return &adapter.RestartDryResult{StopOrder: []string{}, StartOrder: []string{name}}
}
func (m *mockHandler) AddRecipe(content string, force bool) (string, string, error) {
	return "test", "1.0.0", nil
}
func (m *mockHandler) DeleteRecipe(name, version string) error { return nil }
func (m *mockHandler) ListRecipes() ([]string, error)          { return []string{}, nil }
func (m *mockHandler) GetHealth() *adapter.HealthStatus        { return m.health }

func TestAdapter_Name(t *testing.T) {
	a := New(Config{Addr: ":0"}, &mockHandler{})
	if a.Name() != "http" {
		t.Errorf("expected name 'http', got %q", a.Name())
	}
}

func TestAdapter_Healthz(t *testing.T) {
	handler := &mockHandler{
		health: &adapter.HealthStatus{
			Status:  "ok",
			Uptime:  "1m0s",
			Closed:  false,
			TimeUTC: "2024-01-01T00:00:00Z",
		},
	}
	a := New(Config{Addr: ":0"}, handler)
	router := a.buildRouter()

	req := httptest.NewRequest("GET", "/healthz", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	var resp adapter.HealthStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if resp.Status != "ok" {
		t.Errorf("expected status 'ok', got %q", resp.Status)
	}
}

func TestAdapter_Components(t *testing.T) {
	handler := &mockHandler{
		components: []store.ComponentInfo{
			{Name: "test", State: "running", PID: 1234},
		},
	}
	a := New(Config{Addr: ":0"}, handler)
	router := a.buildRouter()

	req := httptest.NewRequest("GET", "/v1/components", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	var resp []store.ComponentInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if len(resp) != 1 || resp[0].Name != "test" {
		t.Errorf("unexpected components: %+v", resp)
	}
}

func TestAdapter_PlanStatus(t *testing.T) {
	handler := &mockHandler{
		planStatus: &adapter.PlanStatus{
			PlanPath: "test.toml",
			Status:   "running",
		},
	}
	a := New(Config{Addr: ":0"}, handler)
	router := a.buildRouter()

	req := httptest.NewRequest("GET", "/v1/plan/status", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	var resp adapter.PlanStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if resp.Status != "running" {
		t.Errorf("expected status 'running', got %q", resp.Status)
	}
}

func TestAdapter_StartStop(t *testing.T) {
	handler := &mockHandler{
		health: &adapter.HealthStatus{Status: "ok"},
	}
	a := New(Config{Addr: ":18080"}, handler)

	ctx := context.Background()
	if err := a.Start(ctx); err != nil {
		t.Fatalf("failed to start adapter: %v", err)
	}

	// Give server time to start
	time.Sleep(50 * time.Millisecond)

	// Test that the server is responding
	resp, err := http.Get("http://127.0.0.1:18080/healthz")
	if err != nil {
		t.Fatalf("failed to GET /healthz: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	// Stop the server
	if err := a.Stop(ctx); err != nil {
		t.Errorf("failed to stop adapter: %v", err)
	}
}
