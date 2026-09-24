package mqtt

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/carlosprados/keystone/internal/adapter"
	pahomqtt "github.com/eclipse/paho.mqtt.golang"
)

// brokerIsLoopback reports whether the configured broker lives on this device.
//
// It matters for exactly one thing: a self-update confirms itself partly on
// "a control plane has heard from me", and publishing to a broker running on
// the same box proves nothing about that. The device would confirm with its
// network cable pulled out — the guardrail would be certifying itself.
//
// A loopback broker is not wrong: bridging a local mosquitto to a remote one is
// an ordinary IoT arrangement. But then the proof of reachability is a message
// arriving FROM outside, not one leaving.
func brokerIsLoopback(broker string) bool {
	s := strings.TrimSpace(broker)
	if s == "" {
		return false
	}
	if !strings.Contains(s, "//") {
		s = "tcp://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return false
	}

	host := u.Hostname()
	switch strings.ToLower(host) {
	case "localhost", "ip6-localhost", "ip6-loopback":
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// markReachable tells the agent a control plane can still reach this device,
// which is half of what a pending self-update has to prove.
func (a *Adapter) markReachable() {
	if r, ok := a.handler.(interface{ MarkUpdateReported() }); ok {
		r.MarkUpdateReported()
	}
}

// selfUpdater is the part of the agent this adapter needs for cmd/self-update.
// Declared here rather than added to adapter.CommandHandler so that an install
// which never updates itself is not forced to grow the methods.
type selfUpdater interface {
	StageSelfUpdate(ctx context.Context, spec adapter.SelfUpdateSpec) error
	RequestRestart(reason string)
}

// updateStatus asks the handler for its self-update state, when it has one.
// Declared as a small optional interface so the adapter does not have to know
// about self-update: an install without it simply reports nothing.
func (a *Adapter) updateStatus() string {
	if s, ok := a.handler.(interface{ UpdateStatus() string }); ok {
		return s.UpdateStatus()
	}
	return ""
}

// rejectPlanPath reports a request that still carries the removed planPath
// field, instead of letting it fall through to "content required".
//
// A controller that has not been updated would otherwise get an error naming
// the wrong field and conclude its plan content was malformed. Naming what was
// removed is the difference between a five-minute fix and an afternoon.
func rejectPlanPath(payload []byte) error {
	var legacy struct {
		PlanPath string `json:"planPath"`
	}
	if json.Unmarshal(payload, &legacy) == nil && legacy.PlanPath != "" {
		return fmt.Errorf("planPath is no longer accepted (it let a publisher execute an arbitrary file on the device); send the plan as content instead")
	}
	return nil
}

// rejectRetained refuses a command that arrived with the MQTT retained flag.
//
// A retained message is redelivered by the broker on every subscribe, forever.
// For telemetry that is the point; for a command it means a device executes an
// order every time it reconnects — including an order that was withdrawn weeks
// earlier, on a gateway that has been offline since. No legitimate command is
// ever retained, so this costs nothing and closes the widest replay window
// there is.
//
// It also covers the case operators walk into on their own: the natural fix for
// "the device misses orders while it has no coverage" is CleanSession=false,
// and that is precisely the setting that makes replay possible for ordinary
// publishes too.
//
// Note when testing this: MQTT sets the retained flag only when delivering to a
// client that subscribes AFTERWARDS. Publishing with retain to a topic this
// agent is already subscribed to arrives as an ordinary message with the flag
// clear, so it is executed rather than refused — correctly, since it is a
// first delivery. The refusal happens on the redelivery at the next subscribe,
// which is the repeat this guard exists to stop.
func rejectRetained(msg pahomqtt.Message) error {
	if msg.Retained() {
		return fmt.Errorf("refusing a retained command on %s: retained commands are redelivered on every reconnect; publish commands with retain=false", msg.Topic())
	}
	return nil
}

// commandDeduper drops commands the agent has already executed.
//
// QoS 1 is at-least-once BY DESIGN: a duplicate delivery is the protocol
// working correctly, not a broker fault. For a status query a duplicate is
// harmless; for "apply this plan" it is a second deployment, and for anything
// that replaces software it is worse.
//
// Deduplication is keyed on an id the controller supplies. Commands without one
// are not deduplicated — the agent cannot invent identity for them — which is
// stated rather than hidden: a controller that wants exactly-once has to say
// which command it is.
type commandDeduper struct {
	mu   sync.Mutex
	seen map[string]time.Time
	ttl  time.Duration
	now  func() time.Time
}

func newCommandDeduper(ttl time.Duration) *commandDeduper {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	return &commandDeduper{
		seen: make(map[string]time.Time),
		ttl:  ttl,
		now:  time.Now,
	}
}

// firstSight records id and reports whether this is the first time it is seen.
// An empty id is always treated as new: absence of an id is absence of a claim
// about identity, not a claim of uniqueness.
func (d *commandDeduper) firstSight(id string) bool {
	if id == "" {
		return true
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	now := d.now()
	// Opportunistic expiry: this map only grows with distinct command ids, so
	// sweeping on write is cheap enough and avoids a background goroutine.
	for k, t := range d.seen {
		if now.Sub(t) > d.ttl {
			delete(d.seen, k)
		}
	}

	if _, dup := d.seen[id]; dup {
		return false
	}
	d.seen[id] = now
	return true
}

// guardMutating wraps a handler that changes the device's state with the two
// checks a command channel needs, and leaves read-only handlers alone.
//
// The asymmetry is deliberate: a replayed status query is noise, a replayed
// apply is a second deployment. Applying the checks to reads would add failure
// modes to the one operation an operator reaches for when things are already
// going wrong.
func (a *Adapter) guardMutating(respTopic string, h pahomqtt.MessageHandler) pahomqtt.MessageHandler {
	return func(client pahomqtt.Client, msg pahomqtt.Message) {
		var env struct {
			CorrelationID string `json:"correlationId"`
			CommandID     string `json:"commandId"`
		}
		_ = json.Unmarshal(msg.Payload(), &env)

		if err := rejectRetained(msg); err != nil {
			log.Printf("[mqtt] %v", err)
			a.respond(respTopic, env.CorrelationID, NewErrorResponse(env.CorrelationID, err))
			return
		}

		// A command that arrived is the strongest evidence there is that the
		// way in still works — stronger than anything this device can publish,
		// because it required someone on the other side. It counts even when
		// the broker is local: whatever bridged it in came from outside.
		a.markReachable()

		if !a.deduper.firstSight(env.CommandID) {
			err := fmt.Errorf("command %q was already executed; ignoring a duplicate delivery", env.CommandID)
			log.Printf("[mqtt] %v", err)
			a.respond(respTopic, env.CorrelationID, NewErrorResponse(env.CorrelationID, err))
			return
		}

		h(client, msg)
	}
}
