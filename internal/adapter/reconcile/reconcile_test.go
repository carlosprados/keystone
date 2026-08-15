package reconcile

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/carlosprados/keystone/internal/adapter"
	"github.com/carlosprados/keystone/internal/store"
)

// stubHandler implements adapter.CommandHandler and records ReconcileNow calls.
type stubHandler struct {
	calls  atomic.Int32
	fired  chan struct{}
	once   sync.Once
	result *adapter.ReconcileResult
	err    error
}

func newStub() *stubHandler {
	return &stubHandler{fired: make(chan struct{}), result: &adapter.ReconcileResult{Duration: "1ms"}}
}

func (s *stubHandler) ReconcileNow() (*adapter.ReconcileResult, error) {
	s.calls.Add(1)
	s.once.Do(func() { close(s.fired) })
	return s.result, s.err
}

func (s *stubHandler) ApplyPlan(string, bool) error         { return nil }
func (s *stubHandler) ApplyPlanContent(string, bool) error  { return nil }
func (s *stubHandler) StopPlan() error                      { return nil }
func (s *stubHandler) GetPlanStatus() *adapter.PlanStatus   { return &adapter.PlanStatus{} }
func (s *stubHandler) GetPlanGraph() *adapter.GraphInfo     { return &adapter.GraphInfo{} }
func (s *stubHandler) GetComponents() []store.ComponentInfo { return nil }
func (s *stubHandler) StopComponent(string) error           { return nil }
func (s *stubHandler) RestartComponentDry(string) *adapter.RestartDryResult {
	return &adapter.RestartDryResult{}
}
func (s *stubHandler) RestartComponent(string, string, time.Duration) (*adapter.RestartResult, error) {
	return &adapter.RestartResult{}, nil
}
func (s *stubHandler) AddRecipe(string, bool) (string, string, error) { return "", "", nil }
func (s *stubHandler) DeleteRecipe(string, string) error              { return nil }
func (s *stubHandler) ListRecipes() ([]string, error)                 { return nil, nil }
func (s *stubHandler) GetHealth() *adapter.HealthStatus               { return &adapter.HealthStatus{} }

// A zero interval means the operator did not ask for periodic reconcile. The
// adapter must then be completely inert — not a loop that ticks and skips.
func TestDisabledByDefault(t *testing.T) {
	stub := newStub()
	a := New(Config{}, stub)

	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("Start with interval 0: %v", err)
	}
	if err := a.Stop(context.Background()); err != nil {
		t.Fatalf("Stop with interval 0: %v", err)
	}
	if got := stub.calls.Load(); got != 0 {
		t.Errorf("disabled adapter ran %d passes, want 0", got)
	}
}

func TestRunsAndStops(t *testing.T) {
	stub := newStub()
	a := New(Config{Interval: 10 * time.Millisecond}, stub)

	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-stub.fired:
	case <-time.After(2 * time.Second):
		t.Fatal("no reconcile pass within 2s")
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := a.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// After Stop returns, the loop is gone and the count must stay put.
	settled := stub.calls.Load()
	time.Sleep(50 * time.Millisecond)
	if got := stub.calls.Load(); got != settled {
		t.Errorf("adapter kept running after Stop: %d -> %d passes", settled, got)
	}
}

// Stop must be safe without Start, and idempotent: the shutdown path calls it
// on every registered adapter regardless of how they were configured.
func TestStopWithoutStart(t *testing.T) {
	a := New(Config{Interval: time.Second}, newStub())
	if err := a.Stop(context.Background()); err != nil {
		t.Fatalf("Stop before Start: %v", err)
	}
	if err := a.Stop(context.Background()); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
}

func TestBackoffGrowsAndIsCapped(t *testing.T) {
	a := New(Config{Interval: time.Minute, MaxBackoff: 10 * time.Minute}, newStub())

	cases := []struct {
		failures int
		want     time.Duration
	}{
		{1, time.Minute},
		{2, 2 * time.Minute},
		{3, 4 * time.Minute},
		{4, 8 * time.Minute},
		{5, 10 * time.Minute}, // capped
		{50, 10 * time.Minute},
	}
	for _, c := range cases {
		if got := a.backoff(c.failures); got != c.want {
			t.Errorf("backoff(%d)=%s, want %s", c.failures, got, c.want)
		}
	}
}

// The offset must be stable for a device and spread across the fleet: a random
// offset would make one gateway's 03:14 report impossible to reproduce.
func TestOffsetIsDeterministicAndBounded(t *testing.T) {
	jitter := 5 * time.Minute
	cfg := func(id string) Config {
		return Config{Interval: time.Hour, Jitter: jitter, DeviceID: id}
	}

	first := New(cfg("edge-001"), newStub()).offset()
	again := New(cfg("edge-001"), newStub()).offset()
	if first != again {
		t.Errorf("offset for the same device differs between runs: %s vs %s", first, again)
	}
	if first < 0 || first >= jitter {
		t.Errorf("offset %s is outside [0, %s)", first, jitter)
	}

	other := New(cfg("edge-002"), newStub()).offset()
	if other == first {
		t.Errorf("two devices landed on the same offset %s; the fleet would not spread", first)
	}

	if got := New(Config{Interval: time.Hour, Jitter: jitter}, newStub()).offset(); got != 0 {
		t.Errorf("offset without a device ID = %s, want 0", got)
	}
	if got := New(Config{Interval: time.Hour, DeviceID: "edge-001"}, newStub()).offset(); got != 0 {
		t.Errorf("offset without jitter = %s, want 0", got)
	}
}
