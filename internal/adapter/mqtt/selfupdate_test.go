package mqtt

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/carlosprados/keystone/internal/adapter"
)

// updaterHandler is a CommandHandler that also accepts self-updates, which is
// what the adapter type-asserts for.
type updaterHandler struct {
	adapter.CommandHandler

	mu        sync.Mutex
	staged    []adapter.SelfUpdateSpec
	restarts  []string
	stageErr  error
	reachable int
}

func (h *updaterHandler) StageSelfUpdate(_ context.Context, spec adapter.SelfUpdateSpec) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stageErr != nil {
		return h.stageErr
	}
	h.staged = append(h.staged, spec)
	return nil
}

func (h *updaterHandler) RequestRestart(reason string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.restarts = append(h.restarts, reason)
}

func (h *updaterHandler) MarkUpdateReported() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reachable++
}

func (h *updaterHandler) counts() (staged, restarts, reachable int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.staged), len(h.restarts), h.reachable
}

// TestArrivingCommandProvesReachability is the correction a real device forced:
// publishing to a broker on the same box proves nothing, so the evidence has to
// be a command ARRIVING — it required someone on the other side, and it is the
// direction a rollback order would travel.
func TestArrivingCommandProvesReachability(t *testing.T) {
	h := &updaterHandler{}
	a := New(Config{Broker: "tcp://127.0.0.1:1883", DeviceID: "dev"}, h)

	if !a.brokerLocal {
		t.Fatal("a loopback broker was not recognised as local")
	}

	// A command arriving counts, whatever the broker is.
	a.markReachable()
	if _, _, reachable := h.counts(); reachable != 1 {
		t.Errorf("an arriving command did not count as proof: %d", reachable)
	}
}

// TestRemoteBrokerIsRecognised: with the broker elsewhere, publishing is
// evidence too, because there is someone on the other side by construction.
func TestRemoteBrokerIsRecognised(t *testing.T) {
	a := New(Config{Broker: "ssl://broker.example.net:8883", DeviceID: "dev"}, &updaterHandler{})
	if a.brokerLocal {
		t.Error("a remote broker was treated as local; publishing would stop counting")
	}
}

// TestSelfUpdateRefusedByHandler: a staging failure must answer with an error
// and must NOT restart. Restarting after a failed install would put the device
// through an outage for nothing.
func TestSelfUpdateRefusedByHandler(t *testing.T) {
	h := &updaterHandler{stageErr: context.Canceled}
	a := New(Config{Broker: "tcp://broker.example.net:1883", DeviceID: "dev"}, h)

	a.handleSelfUpdate(nil, fakeMessage{
		topic:   a.topics.CmdSelfUpdate,
		payload: mustJSON(t, SelfUpdateRequest{Version: "v2", URI: "http://x/k", SHA256: "abc"}),
	})

	staged, restarts, _ := h.counts()
	if staged != 0 {
		t.Errorf("a rejected update was recorded as staged")
	}
	if restarts != 0 {
		t.Errorf("the agent was restarted after a failed install")
	}
}

// TestSelfUpdateRestartsByDefault: an update installed but never started is a
// trial that never begins, and the device would keep reporting the old version
// while looking updated to whoever sent the command.
func TestSelfUpdateRestartsByDefault(t *testing.T) {
	h := &updaterHandler{}
	a := New(Config{Broker: "tcp://broker.example.net:1883", DeviceID: "dev"}, h)

	a.handleSelfUpdate(nil, fakeMessage{
		topic:   a.topics.CmdSelfUpdate,
		payload: mustJSON(t, SelfUpdateRequest{Version: "v2", URI: "http://x/k", SHA256: "abc"}),
	})

	staged, restarts, _ := h.counts()
	if staged != 1 || restarts != 1 {
		t.Fatalf("staged=%d restarts=%d, want 1 and 1", staged, restarts)
	}
}

// TestSelfUpdateCanSkipTheRestart leaves the timing to the operator, for a
// device that cannot afford an outage right now.
func TestSelfUpdateCanSkipTheRestart(t *testing.T) {
	h := &updaterHandler{}
	a := New(Config{Broker: "tcp://broker.example.net:1883", DeviceID: "dev"}, h)

	no := false
	a.handleSelfUpdate(nil, fakeMessage{
		topic:   a.topics.CmdSelfUpdate,
		payload: mustJSON(t, SelfUpdateRequest{Version: "v2", URI: "http://x/k", SHA256: "abc", Restart: &no}),
	})

	staged, restarts, _ := h.counts()
	if staged != 1 {
		t.Errorf("the update was not staged")
	}
	if restarts != 0 {
		t.Errorf("the agent restarted although the command asked it not to")
	}
}

// TestSelfUpdateOnAnAgentWithoutIt: most installs are upgraded from outside and
// will never implement this. They must get a clear refusal, not a panic.
func TestSelfUpdateOnAnAgentWithoutIt(t *testing.T) {
	a := New(Config{Broker: "tcp://broker.example.net:1883", DeviceID: "dev"}, plainHandler{})

	a.handleSelfUpdate(nil, fakeMessage{
		topic:   a.topics.CmdSelfUpdate,
		payload: mustJSON(t, SelfUpdateRequest{Version: "v2", URI: "http://x/k", SHA256: "abc"}),
	})
	// Reaching here without panicking is the assertion.
}

// TestSelfUpdateTopicMapsToItsResponse guards the wiring: a response published
// to the wrong topic is a command that looks like it vanished.
func TestSelfUpdateTopicMapsToItsResponse(t *testing.T) {
	tp := NewTopics("dev")
	if got := tp.ResponseTopic(tp.CmdSelfUpdate); got != tp.RespSelfUpdate {
		t.Errorf("ResponseTopic(cmd/self-update) = %q, want %q", got, tp.RespSelfUpdate)
	}
}

type plainHandler struct{ adapter.CommandHandler }

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
