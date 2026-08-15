package agent

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/carlosprados/keystone/internal/dataset"
	sysrt "github.com/carlosprados/keystone/internal/runtime"
)

// reloadTimeout bounds a reload script. A hook that hangs must not hold up the
// refresh of every other dataset behind it.
const reloadTimeout = 30 * time.Second

// reloadComponent tells a component its data changed, without restarting it.
//
// Restarting is what this exists to avoid: a discovery engine watching an OT
// network cannot go down every night because a vulnerability feed arrived.
func (a *Agent) reloadComponent(ctx context.Context, b datasetBinding) error {
	switch {
	case b.reload.Signal != "":
		return a.signalComponent(b)
	case b.reload.Script != "":
		runCtx, cancel := context.WithTimeout(ctx, reloadTimeout)
		defer cancel()
		out, err := runShellWithOutput(runCtx, b.workDir, b.reload.Script)
		if err != nil {
			return fmt.Errorf("reload script failed: %v\n--- output ---\n%s", err, out)
		}
		log.Printf("[dataset] component=%s msg=reload script ran", b.component)
		return nil
	default:
		return nil
	}
}

// signalComponent sends the reload signal to the component's main process.
//
// To the process itself, not its group: a reload signal delivered to the whole
// group would reach children that do not handle it, and the default action for
// SIGHUP and the user signals is to terminate. Reloading a component by killing
// its helpers is not a reload.
func (a *Agent) signalComponent(b datasetBinding) error {
	sig, err := dataset.ParseSignal(b.reload.Signal)
	if err != nil {
		return err
	}
	pid := a.currentPID(b.component)
	if pid <= 0 {
		return fmt.Errorf("no PID for %s: it is a container, or it is not running", b.component)
	}
	if !sysrt.IsProcessRunning(pid) {
		return fmt.Errorf("process %d for %s is gone", pid, b.component)
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("finding process %d: %w", pid, err)
	}
	if err := proc.Signal(sig); err != nil {
		return fmt.Errorf("sending %s to %d: %w", b.reload.Signal, pid, err)
	}
	log.Printf("[dataset] component=%s pid=%d signal=%s msg=reload signalled", b.component, pid, b.reload.Signal)
	return nil
}

// processAlive is a thin alias so the dataset code reads in its own terms.
func processAlive(pid int) bool { return sysrt.IsProcessRunning(pid) }
