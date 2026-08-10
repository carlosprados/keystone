//go:build !unix

package cli

import (
	"errors"
	"net"
)

// dialSSH is unavailable off Unix: the tunnel hands ssh one end of a socketpair
// as its stdio, and there is no equivalent here.
//
// The agent itself is Linux-only, but keystonectl runs on the operator's
// machine, so the rest of the client still builds and works here — only --ssh
// does not. Use ssh -L and point --addr at the forwarded port instead.
func dialSSH(sshTarget, string) (net.Conn, error) {
	return nil, errors.New("--ssh is not supported on this platform; forward the port with ssh -L and use --addr")
}
