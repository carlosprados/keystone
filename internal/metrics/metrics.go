package metrics

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	once           sync.Once
	componentState = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "keystone",
			Subsystem: "component",
			Name:      "state",
			Help:      "Component state gauge (1 for current state).",
		},
		[]string{"name", "state"},
	)
	componentStateHealth = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "keystone",
			Subsystem: "component",
			Name:      "state_health",
			Help:      "Component state with health label (1 when active).",
		},
		[]string{"name", "state", "health"},
	)
	componentRestarts = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "keystone",
			Subsystem: "component",
			Name:      "restarts_total",
			Help:      "Number of restarts for the component.",
		},
		[]string{"name"},
	)
	componentHealthy = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "keystone",
			Subsystem: "component",
			Name:      "healthy",
			Help:      "Component health (1 healthy, 0 unhealthy).",
		},
		[]string{"name"},
	)
	reconcileTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "keystone",
			Subsystem: "reconcile",
			Name:      "total",
			Help:      "Reconcile passes by outcome (ok, skipped, failed).",
		},
		[]string{"result"},
	)
	reconcileDuration = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: "keystone",
			Subsystem: "reconcile",
			Name:      "duration_seconds",
			Help:      "Duration of a reconcile pass.",
			Buckets:   prometheus.DefBuckets,
		},
	)
	// reconcileRepairs is the counter that answers whether periodic reconcile
	// is worth its cost on this device: a component that is repaired every
	// night is a component with a problem nobody has looked at.
	reconcileRepairs = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "keystone",
			Subsystem: "reconcile",
			Name:      "repairs_total",
			Help:      "Components brought back to running by a reconcile pass.",
		},
		[]string{"component"},
	)
	// clockTrusted is worth alerting on: a device verifying certificates against
	// an approximate time is not enforcing expiry the way its configuration
	// suggests, and nothing else about it looks wrong.
	clockTrusted = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "keystone",
			Subsystem: "clock",
			Name:      "trusted",
			Help:      "1 when the system clock is at least as late as the agent's own evidence, 0 when it is behind it.",
		},
	)
	reconcileLast = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "keystone",
			Subsystem: "reconcile",
			Name:      "last_timestamp_seconds",
			Help:      "Unix timestamp of the last reconcile pass, whatever its outcome.",
		},
	)
)

// initRegistry registers metrics once.
func init() {
	once.Do(func() {
		prometheus.MustRegister(
			componentState, componentStateHealth, componentRestarts, componentHealthy,
			reconcileTotal, reconcileDuration, reconcileRepairs, reconcileLast,
			clockTrusted,
		)
	})
}

// ObserveComponentState sets the gauge for the given component's current state to 1.
// In a richer implementation we would set 0 for other states too.
func ObserveComponentState(name, state string) {
	componentState.WithLabelValues(name, state).Set(1)
}

func ObserveComponentStateWithHealth(name, state, health string) {
	if health == "" {
		health = "unknown"
	}
	componentStateHealth.WithLabelValues(name, state, health).Set(1)
}

// ObserveReconcile records one reconcile pass: its outcome ("ok", "skipped" or
// "failed"), how long it took, and which components it brought back.
func ObserveReconcile(result string, d time.Duration, repaired []string) {
	reconcileTotal.WithLabelValues(result).Inc()
	reconcileDuration.Observe(d.Seconds())
	reconcileLast.SetToCurrentTime()
	for _, name := range repaired {
		reconcileRepairs.WithLabelValues(name).Inc()
	}
}

// SetClockTrusted publishes whether the system clock can be believed.
func SetClockTrusted(trusted bool) {
	if trusted {
		clockTrusted.Set(1)
		return
	}
	clockTrusted.Set(0)
}

func IncRestarts(name string) { componentRestarts.WithLabelValues(name).Inc() }
func SetHealthy(name string, healthy bool) {
	if healthy {
		componentHealthy.WithLabelValues(name).Set(1)
	} else {
		componentHealthy.WithLabelValues(name).Set(0)
	}
}
