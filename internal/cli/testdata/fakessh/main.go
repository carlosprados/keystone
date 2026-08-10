// Command fakessh stands in for the ssh binary in tests.
//
// It implements the only behaviour keystonectl relies on: `ssh [options] host
// -W addr` splices stdin and stdout to a TCP connection to addr. Everything
// else on the command line is ignored, exactly as an unused ssh option would be.
package main

import (
	"fmt"
	"io"
	"net"
	"os"
)

func main() {
	addr := ""
	for i, arg := range os.Args {
		if arg == "-W" && i+1 < len(os.Args) {
			addr = os.Args[i+1]
		}
	}
	if addr == "" {
		fmt.Fprintln(os.Stderr, "fakessh: no -W target")
		os.Exit(2)
	}

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakessh:", err)
		os.Exit(255)
	}
	defer conn.Close()

	done := make(chan struct{})
	go func() {
		io.Copy(conn, os.Stdin)
		if c, ok := conn.(*net.TCPConn); ok {
			c.CloseWrite()
		}
		close(done)
	}()
	io.Copy(os.Stdout, conn)
	<-done
}
