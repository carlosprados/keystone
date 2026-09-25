package runner

import (
	"fmt"
	"log"
	"time"

	sysrt "github.com/carlosprados/keystone/internal/runtime"
)

// adoptInterval is how often an adopted process is checked for having exited.
//
// An adopted process is not a child of this agent — the previous agent died and
// the kernel reparented it to PID 1 — so there is no wait() to block on and
// nothing to receive SIGCHLD. Polling is the only option, and a second is
// frequent enough: the restart policy reacts in seconds anyway, and a tighter
// loop on a gateway costs battery for nothing.
const adoptInterval = time.Second

// Adopt takes over supervision of a process that is already running.
//
// This exists so that restarting the agent does not restart everything it
// supervises. Without it, an agent that comes back after a crash — or after
// replacing its own binary — kills the processes it finds and starts them
// again, which turns every agent update into an outage for every component.
//
// What the caller gets back is a handle that behaves like one from Start, with
// two differences that cannot be papered over:
//
//   - **Exit detection is by polling**, not wait(). The process is not our
//     child, so an exit is noticed within adoptInterval rather than instantly,
//     and the exit STATUS is not available: the kernel gave it to init. Callers
//     see "it exited", never "it exited with 3".
//   - **Logs depend on how it was started.** Under journald its stdout and
//     stderr are journald streams (see journalStream), which outlive the agent:
//     the adopted process goes on logging with no gap. Without journald they
//     were pipes the dead agent read, and the process died on its next write
//     from SIGPIPE, so there is usually nothing left to adopt.
//
// The exit status is worth losing: a component that keeps running is better
// than one restarted to get its exit code back, and the
// component's own health probe still reports on it.
func (r *ProcessRunner) Adopt(pid int, name string) (*ProcessHandle, error) {
	if pid <= 0 {
		return nil, fmt.Errorf("adopt %s: invalid pid %d", name, pid)
	}
	if !sysrt.IsProcessRunning(pid) {
		return nil, fmt.Errorf("adopt %s: process %d is not running", name, pid)
	}

	h := &ProcessHandle{
		pid:  pid,
		name: name,
		// The real start time is unknown: it belongs to a run this agent did
		// not see. Recording adoption time is honest about what is known, and
		// uptime reported from here means "supervised since", not "running
		// since".
		startedAt: time.Now(),
		done:      make(chan error, 1),
	}

	go func() {
		ticker := time.NewTicker(adoptInterval)
		defer ticker.Stop()
		for range ticker.C {
			if !sysrt.IsProcessRunning(pid) {
				// No exit status to report: it went to init, not to us.
				h.done <- nil
				return
			}
		}
	}()

	log.Printf("[runner] component=%s msg=adopted existing process pid=%d (exit detected by polling)", name, pid)
	return h, nil
}
