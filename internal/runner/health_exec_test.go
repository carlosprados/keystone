package runner

import (
	"context"
	"testing"
	"time"
)

// TestHealthConfigConfigured pins what "declares a probe" means, in one place.
// It used to be spelled `Check == ""` in six, and when the argv form arrived
// only the probing code learned about it.
func TestHealthConfigConfigured(t *testing.T) {
	if (HealthConfig{}).Configured() {
		t.Error("an empty config claims to have a probe")
	}
	if !(HealthConfig{Check: "http://127.0.0.1/healthz"}).Configured() {
		t.Error("a check probe was not recognised")
	}
	if !(HealthConfig{Exec: []string{"/bin/true"}}).Configured() {
		t.Error("an exec probe was not recognised: this is the bug that made a declared probe never run")
	}
}

// TestExecProbeActuallyRuns is the test that was missing. A recipe declaring
// `health.exec` and no `check` reported "unknown" forever: the probe was
// implemented and never reached, so the component ran unsupervised while
// looking fine.
//
// Worse than the failure it replaced — a probe that cannot run fails and gets
// the component restarted; a probe that is never run makes everything look
// healthy.
func TestExecProbeActuallyRuns(t *testing.T) {
	r := NewProcessRunner()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	health := make(chan bool, 4)
	go func() {
		_ = r.RunManaged(ctx, "probed",
			Options{Command: "/bin/sh", Args: []string{"-c", "while true; do sleep 60; done"}},
			HealthConfig{Exec: []string{"/bin/true"}, Interval: 200 * time.Millisecond, Timeout: time.Second, FailureThreshold: 3},
			RestartNever, 1,
			nil,
			func(ok bool) { health <- ok },
			nil,
		)
	}()

	select {
	case ok := <-health:
		if !ok {
			t.Fatal("an exec probe running /bin/true reported unhealthy")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("an exec probe never reported: the component would sit at 'unknown' with nothing watching it")
	}
}

// TestExecProbeReportsFailure: the other direction, so a component that is
// genuinely sick is not reported healthy.
func TestExecProbeReportsFailure(t *testing.T) {
	r := NewProcessRunner()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	health := make(chan bool, 4)
	go func() {
		_ = r.RunManaged(ctx, "sick",
			Options{Command: "/bin/sh", Args: []string{"-c", "while true; do sleep 60; done"}},
			HealthConfig{Exec: []string{"/bin/false"}, Interval: 200 * time.Millisecond, Timeout: time.Second, FailureThreshold: 3},
			RestartNever, 1,
			nil,
			func(ok bool) { health <- ok },
			nil,
		)
	}()

	select {
	case ok := <-health:
		if ok {
			t.Fatal("an exec probe running /bin/false reported healthy")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a failing exec probe never reported")
	}
}
