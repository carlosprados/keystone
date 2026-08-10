package cli

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strings"
)

// The agent binds loopback by default, and binding anything else demands a
// token — so on a real device the API is usually only reachable from the device
// itself. Rather than asking the operator to keep an `ssh -L` running in another
// terminal, keystonectl can carry its own request over SSH.
//
// The tunnel is the local `ssh` binary invoked with -W, which connects stdio to
// a host and port on the far side. That choice is deliberate: it inherits the
// operator's ~/.ssh/config, agent, keys, known_hosts and ProxyJump chain, none
// of which an in-process SSH client would get for free.

// sshTarget is a parsed --ssh destination.
type sshTarget struct {
	// Host is what gets handed to ssh: [user@]hostname, or a Host alias from
	// ~/.ssh/config.
	Host string
	// Port is the SSH port, empty when the destination did not name one.
	Port string
}

// parseSSHTarget splits an optional :port off an [user@]host destination.
//
// A bracketed literal IPv6 address ([::1]:22) keeps its brackets for ssh, and a
// bare IPv6 address is left alone: a colon there is part of the address, not a
// port separator.
func parseSSHTarget(dest string) (sshTarget, error) {
	dest = strings.TrimSpace(dest)
	if dest == "" {
		return sshTarget{}, fmt.Errorf("empty SSH destination")
	}
	if strings.HasPrefix(dest, "-") {
		return sshTarget{}, fmt.Errorf("invalid SSH destination %q", dest)
	}

	userPart, hostPart := "", dest
	if at := strings.LastIndex(dest, "@"); at >= 0 {
		if at == 0 {
			return sshTarget{}, fmt.Errorf("invalid SSH destination %q: empty user", dest)
		}
		userPart, hostPart = dest[:at+1], dest[at+1:]
	}

	host, port := hostPart, ""
	switch {
	case strings.HasPrefix(hostPart, "["):
		end := strings.LastIndex(hostPart, "]")
		if end < 0 {
			return sshTarget{}, fmt.Errorf("invalid SSH destination %q", dest)
		}
		if rest := hostPart[end+1:]; strings.HasPrefix(rest, ":") {
			host, port = hostPart[:end+1], rest[1:]
		}
	case strings.Count(hostPart, ":") == 1:
		host, port = hostPart[:strings.Index(hostPart, ":")], hostPart[strings.Index(hostPart, ":")+1:]
	}

	if host == "" || host == "[]" {
		return sshTarget{}, fmt.Errorf("invalid SSH destination %q", dest)
	}
	// Several colons left in an unbracketed host is only legitimate for a bare
	// IPv6 address; anything else is a malformed destination, not a hostname.
	if !strings.HasPrefix(host, "[") && strings.Count(host, ":") > 1 && net.ParseIP(host) == nil {
		return sshTarget{}, fmt.Errorf("invalid SSH destination %q", dest)
	}
	return sshTarget{Host: userPart + host, Port: port}, nil
}

// sshTransport returns an HTTP transport that reaches the agent through SSH.
//
// The address it dials is the one in --addr, resolved on the far side: with
// --ssh edge-001 the default --addr http://127.0.0.1:8080 means the agent's own
// loopback, which is exactly the interface it is bound to.
func sshTransport(target sshTarget) *http.Transport {
	return &http.Transport{
		// One tunnel per request, torn down with the connection. Keeping an
		// ssh process alive for a pool that a one-shot CLI never reuses would
		// only leave it to be killed at exit.
		DisableKeepAlives: true,
		DialContext: func(_ context.Context, _, addr string) (net.Conn, error) {
			return dialSSH(target, addr)
		},
	}
}

// sshCommand builds the ssh invocation that forwards stdio to addr.
//
// The process is not tied to the request context on purpose: its lifetime is
// the connection's, and Close on the returned conn is what ends it.
func sshCommand(target sshTarget, addr string) *exec.Cmd {
	args := []string{"-o", "LogLevel=ERROR"}
	if target.Port != "" {
		args = append(args, "-p", target.Port)
	}
	// -W implies -N and ExitOnForwardFailure: ssh does nothing but splice
	// stdio to addr, and fails rather than dropping to a shell.
	args = append(args, target.Host, "-W", addr)
	return exec.Command("ssh", args...)
}
