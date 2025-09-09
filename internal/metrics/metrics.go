package metrics

import (
	"sync"

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
)

// initRegistry registers metrics once.
func init() {
	once.Do(func() {
		prometheus.MustRegister(componentState, componentStateHealth, componentRestarts, componentHealthy)
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

func IncRestarts(name string) { componentRestarts.WithLabelValues(name).Inc() }
func SetHealthy(name string, healthy bool) {
	if healthy {
		componentHealthy.WithLabelValues(name).Set(1)
	} else {
		componentHealthy.WithLabelValues(name).Set(0)
	}
}
