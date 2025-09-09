package metrics

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/shirou/gopsutil/v4/process"
)

var (
	procCPU = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Namespace: "keystone", Subsystem: "component", Name: "cpu_percent", Help: "Component CPU percent"},
		[]string{"name"},
	)
	procRSS = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Namespace: "keystone", Subsystem: "component", Name: "memory_rss_bytes", Help: "Component RSS bytes"},
		[]string{"name"},
	)
)

func init() {
	prometheus.MustRegister(procCPU, procRSS)
}

// SampleProcessMetrics samples CPU and RSS and records in Prometheus.
func SampleProcessMetrics(ctx context.Context, name string, pid int) {
	p, err := process.NewProcess(int32(pid))
	if err != nil {
		return
	}
	// Warm-up for CPU percent baseline
	_, _ = p.CPUPercentWithContext(ctx)
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if cpu, err := p.CPUPercentWithContext(ctx); err == nil {
				procCPU.WithLabelValues(name).Set(cpu)
			}
			if mi, err := p.MemoryInfoWithContext(ctx); err == nil && mi != nil {
				procRSS.WithLabelValues(name).Set(float64(mi.RSS))
			}
		}
	}
}
