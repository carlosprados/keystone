//go:build unix

package cli

import (
	"fmt"
	"net"
	"os"
	"syscall"
)

// dialSSH starts `ssh -W addr` and returns its stdio as a net.Conn.
//
// The two ends are a socketpair rather than os.Pipe, so what comes back is a
// real *net.UnixConn: it honours the deadlines net/http sets on a connection,
// which a pipe silently would not.
func dialSSH(target sshTarget, addr string) (net.Conn, error) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		return nil, fmt.Errorf("ssh tunnel: socketpair: %w", err)
	}

	local := os.NewFile(uintptr(fds[0]), "keystonectl-ssh")
	remote := os.NewFile(uintptr(fds[1]), "ssh-stdio")
	defer remote.Close()

	// FileConn dups the descriptor, so the original is ours to close.
	conn, err := net.FileConn(local)
	local.Close()
	if err != nil {
		return nil, fmt.Errorf("ssh tunnel: %w", err)
	}

	cmd := sshCommand(target, addr)
	cmd.Stdin = remote
	cmd.Stdout = remote
	// ssh diagnostics (a refused key, an unknown host) go straight to the
	// operator's terminal; swallowing them turns a clear SSH error into an
	// opaque EOF from the HTTP client.
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("ssh tunnel: %w", err)
	}
	return &sshConn{Conn: conn, proc: cmd.Process}, nil
}

// sshConn ties the ssh process to the connection it carries.
type sshConn struct {
	net.Conn
	proc *os.Process
}

func (c *sshConn) Close() error {
	err := c.Conn.Close()
	// Closing our end normally ends ssh by itself; the kill is the backstop
	// for an ssh still waiting on a password prompt or a hung connect.
	_ = c.proc.Kill()
	_, _ = c.proc.Wait()
	return err
}
