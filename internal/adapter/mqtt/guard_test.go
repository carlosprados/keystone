package mqtt

import (
	"strings"
	"testing"
	"time"
)

// fakeMessage is the smallest thing that satisfies what the guards read.
type fakeMessage struct {
	topic    string
	payload  []byte
	retained bool
}

func (m fakeMessage) Duplicate() bool   { return false }
func (m fakeMessage) Qos() byte         { return 1 }
func (m fakeMessage) Retained() bool    { return m.retained }
func (m fakeMessage) Topic() string     { return m.topic }
func (m fakeMessage) MessageID() uint16 { return 1 }
func (m fakeMessage) Payload() []byte   { return m.payload }
func (m fakeMessage) Ack()              {}

// TestRejectRetainedCommand covers the widest replay window there is: a
// retained command is redelivered by the broker on every subscribe, so a
// gateway that has been offline for weeks executes a withdrawn order the
// moment it reconnects.
func TestRejectRetainedCommand(t *testing.T) {
	err := rejectRetained(fakeMessage{topic: "keystone/dev-1/cmd/apply", retained: true})
	if err == nil {
		t.Fatal("a retained command was accepted")
	}
	if !strings.Contains(err.Error(), "retain=false") {
		t.Errorf("error does not tell the publisher what to change: %v", err)
	}
}

func TestAcceptOrdinaryCommand(t *testing.T) {
	if err := rejectRetained(fakeMessage{topic: "keystone/dev-1/cmd/apply"}); err != nil {
		t.Fatalf("an ordinary command was refused: %v", err)
	}
}

// TestRejectPlanPath: the field is gone from the struct, so an old controller
// would otherwise get "content required" and go looking at its plan.
func TestRejectPlanPath(t *testing.T) {
	err := rejectPlanPath([]byte(`{"planPath":"/etc/keystone/plan.toml"}`))
	if err == nil {
		t.Fatal("planPath was accepted")
	}
	if !strings.Contains(err.Error(), "planPath") || !strings.Contains(err.Error(), "content") {
		t.Errorf("error names neither the removed field nor the replacement: %v", err)
	}

	if err := rejectPlanPath([]byte(`{"content":"[[components]]"}`)); err != nil {
		t.Fatalf("a content-carrying request was refused: %v", err)
	}
	if err := rejectPlanPath([]byte(`not json at all`)); err != nil {
		t.Fatalf("malformed JSON should be left to the handler's own decoding: %v", err)
	}
}

// TestDeduperDropsRepeats: QoS 1 is at-least-once by design, so a duplicate
// delivery is the protocol working correctly — and for an apply it is a second
// deployment.
func TestDeduperDropsRepeats(t *testing.T) {
	d := newCommandDeduper(time.Minute)

	if !d.firstSight("cmd-1") {
		t.Fatal("first delivery was treated as a duplicate")
	}
	if d.firstSight("cmd-1") {
		t.Fatal("second delivery of the same command was executed")
	}
	if !d.firstSight("cmd-2") {
		t.Fatal("a different command was treated as a duplicate")
	}
}

// TestDeduperTreatsMissingIDAsNew states the limit out loud: without an id the
// agent cannot tell a retry from a deliberate second apply, so it runs both.
func TestDeduperTreatsMissingIDAsNew(t *testing.T) {
	d := newCommandDeduper(time.Minute)

	if !d.firstSight("") || !d.firstSight("") {
		t.Fatal("commands without an id must not be deduplicated against each other")
	}
}

// TestDeduperForgetsAfterTTL: the map must not grow forever on a device that
// runs for months.
func TestDeduperForgetsAfterTTL(t *testing.T) {
	d := newCommandDeduper(time.Minute)
	now := time.Now()
	d.now = func() time.Time { return now }

	d.firstSight("cmd-1")
	if d.firstSight("cmd-1") {
		t.Fatal("duplicate accepted inside the TTL")
	}

	now = now.Add(2 * time.Minute)
	if !d.firstSight("cmd-1") {
		t.Fatal("the id was still remembered past its TTL")
	}
	if len(d.seen) != 1 {
		t.Errorf("expired entries were not swept: %d entries", len(d.seen))
	}
}
