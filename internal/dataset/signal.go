package dataset

import (
	"fmt"
	"strings"
	"syscall"
)

// reloadSignals is the allow-list of signals a reload hook may send.
//
// An allow-list rather than a general parser: a reload is meant to make a
// component reread a file, and letting a recipe name SIGKILL would turn "your
// data changed" into an outage with no restart policy behind it. SIGHUP is the
// conventional one; the two user signals are here for components that already
// use them for this.
var reloadSignals = map[string]syscall.Signal{
	"SIGHUP":  syscall.SIGHUP,
	"HUP":     syscall.SIGHUP,
	"SIGUSR1": syscall.SIGUSR1,
	"USR1":    syscall.SIGUSR1,
	"SIGUSR2": syscall.SIGUSR2,
	"USR2":    syscall.SIGUSR2,
}

// ParseSignal resolves a reload signal name.
func ParseSignal(name string) (syscall.Signal, error) {
	sig, ok := reloadSignals[strings.ToUpper(strings.TrimSpace(name))]
	if !ok {
		return 0, fmt.Errorf("reload signal %q is not allowed (supported: SIGHUP, SIGUSR1, SIGUSR2)", name)
	}
	return sig, nil
}
