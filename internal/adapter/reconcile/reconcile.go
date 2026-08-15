// Package reconcile provides a clock-driven control-plane adapter: it re-runs
// the plan already in effect on an interval, so a device repairs itself with no
// message from anybody.
//
// It is an adapter for the same reason HTTP and NATS are. An adapter turns
// external events into CommandHandler calls, and a clock is a transport whose
// events happen to be scheduled. Everything this package knows about repair it
// delegates to CommandHandler.ReconcileNow; what lives here is when to ask.
package reconcile

import (
	"context"
	"hash/fnv"
	"log"
	"sync"
	"time"

	"github.com/carlosprados/keystone/internal/adapter"
)

// DefaultMaxBackoff caps the wait after repeated failures. A recipe whose
// signing certificate expired overnight fails every pass; retrying it every
// interval produces log noise and work on the device and fixes nothing, but
// backing off past an hour would leave a device unrepaired long after a
// transient cause cleared.
const DefaultMaxBackoff = time.Hour

// Config configures the periodic reconcile adapter.
type Config struct {
	// Interval between passes. Zero disables the adapter entirely, which is the
	// default: a feature that switches itself on changes the behaviour of every
	// device in a fleet the moment the binary is updated.
	Interval time.Duration
	// Jitter spreads a fleet across the interval. The offset is derived from
	// DeviceID rather than drawn at random, so each device lands in the same
	// slot every time — which is what makes a report from one gateway at 03:14
	// reproducible.
	Jitter time.Duration
	// DeviceID seeds the jitter. Empty means no offset.
	DeviceID string
	// MaxBackoff caps the exponential backoff. Zero means DefaultMaxBackoff.
	MaxBackoff time.Duration
}

// Adapter runs reconcile passes on a timer.
type Adapter struct {
	cfg     Config
	handler adapter.CommandHandler

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// New creates a periodic reconcile adapter.
func New(cfg Config, h adapter.CommandHandler) *Adapter {
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = DefaultMaxBackoff
	}
	return &Adapter{cfg: cfg, handler: h}
}

// Name returns the adapter identifier.
func (a *Adapter) Name() string { return "reconcile" }

// Start begins the reconcile loop. It returns immediately; the first pass
// happens one interval from now.
func (a *Adapter) Start(ctx context.Context) error {
	if a.cfg.Interval <= 0 {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil {
		return nil
	}
	// Detached from the caller's context on purpose: Stop is the single way this
	// loop ends, the same contract the HTTP, NATS and MQTT adapters follow.
	// Letting the start context also kill it would leave Stop unable to tell
	// whether a pass is still running.
	loopCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	a.cancel = cancel
	a.done = make(chan struct{})
	go a.run(loopCtx, a.done)
	log.Printf("[reconcile] periodic reconcile every %s (jitter %s, offset %s)",
		a.cfg.Interval, a.cfg.Jitter, a.offset())
	return nil
}

// Stop ends the reconcile loop and waits for an in-flight pass to return, up to
// whatever deadline the caller's context carries.
func (a *Adapter) Stop(ctx context.Context) error {
	a.mu.Lock()
	cancel, done := a.cancel, a.done
	a.cancel, a.done = nil, nil
	a.mu.Unlock()

	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
	case <-ctx.Done():
		// A pass already running holds applyInProgress and will finish on its
		// own; the agent's own shutdown stops the components either way.
		log.Printf("[reconcile] stop deadline reached with a pass still running")
	}
	return nil
}

// run is the loop. The first wait is a full interval plus this device's offset,
// which both fixes the device's slot and keeps the first pass clear of the
// resume apply that agent.New may have started in the background.
func (a *Adapter) run(ctx context.Context, done chan struct{}) {
	defer close(done)

	failures := 0
	wait := a.cfg.Interval + a.offset()

	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}

		res, err := a.handler.ReconcileNow()
		switch {
		case err != nil:
			failures++
			wait = a.backoff(failures)
			log.Printf("[reconcile] pass failed (%d in a row, next in %s): %v", failures, wait, err)
		case res != nil && res.Skipped:
			// Not a failure: skipping is how the pass stays out of the way of an
			// apply, and how it honours a plan an operator stopped.
			failures = 0
			wait = a.cfg.Interval
			log.Printf("[reconcile] pass skipped: %s", res.Reason)
		default:
			failures = 0
			wait = a.cfg.Interval
			if res != nil && len(res.Repaired) > 0 {
				log.Printf("[reconcile] repaired %v in %s", res.Repaired, res.Duration)
			}
		}
	}
}

// backoff returns the wait after n consecutive failures: the interval doubled
// once per failure, capped.
func (a *Adapter) backoff(n int) time.Duration {
	wait := a.cfg.Interval
	for i := 1; i < n && wait < a.cfg.MaxBackoff; i++ {
		wait *= 2
	}
	if wait > a.cfg.MaxBackoff {
		wait = a.cfg.MaxBackoff
	}
	return wait
}

// offset is this device's fixed position inside the jitter window, derived from
// the device ID so it is the same on every run.
func (a *Adapter) offset() time.Duration {
	if a.cfg.Jitter <= 0 || a.cfg.DeviceID == "" {
		return 0
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(a.cfg.DeviceID))
	return time.Duration(h.Sum64() % uint64(a.cfg.Jitter))
}
