package runner

import (
	"fmt"
	"net"
	"os"
	"strings"
)

// journalSocket is journald's stdout-stream socket. A variable so tests can
// point it at a listener of their own.
var journalSocket = "/run/systemd/journal/stdout"

// Syslog priorities for the two streams, as journald records them.
const (
	journalPriorityInfo = 6
	journalPriorityErr  = 3
)

// journalStream opens a stream to journald for one output of a component and
// returns it as a file the child can inherit as its stdout or stderr.
//
// Why not a pipe the agent reads: the agent is then the only reader, so when it
// dies — a crash, or exiting on purpose to replace its own binary — the next
// write the component makes gets SIGPIPE, and a component that logs dies on its
// first line. Re-adoption would keep alive only the processes that never print.
// A journald stream belongs to the component once handed over: it survives the
// agent, and an adopted process goes on logging with no gap.
//
// The connection is made by the agent, so journald attributes the entries to
// the agent's unit (`journalctl -u keystone` still shows them) and tags them
// with the identifier (`journalctl -t keystone/<component>`).
//
// The header is the one sd_journal_stream_fd(3) sends: identifier, unit id
// (empty), priority, level prefix, then forwarding to syslog, kmsg and console.
func journalStream(identifier string, priority int) (*os.File, error) {
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: journalSocket, Net: "unix"})
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	// A newline in the identifier would shift every header field after it.
	identifier = strings.NewReplacer("\n", "_", "\r", "_").Replace(identifier)
	header := fmt.Sprintf("%s\n\n%d\n0\n0\n0\n0\n", identifier, priority)
	if _, err := conn.Write([]byte(header)); err != nil {
		return nil, fmt.Errorf("journald stream header: %w", err)
	}
	// journald never writes back; closing our read side is what
	// sd_journal_stream_fd does too.
	if err := conn.CloseRead(); err != nil {
		return nil, err
	}
	// File duplicates the descriptor, so closing conn above leaves this one open.
	return conn.File()
}

// journalStreams opens stdout and stderr streams for a component, or returns an
// error when journald is not available so the caller can fall back to pipes.
func journalStreams(component string) (stdout, stderr *os.File, err error) {
	id := "keystone/" + component
	stdout, err = journalStream(id, journalPriorityInfo)
	if err != nil {
		return nil, nil, err
	}
	stderr, err = journalStream(id, journalPriorityErr)
	if err != nil {
		stdout.Close()
		return nil, nil, err
	}
	return stdout, stderr, nil
}
