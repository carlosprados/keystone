package runner

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeJournal listens where journald would and hands each accepted stream's
// lines to the test, header included.
func fakeJournal(t *testing.T) <-chan []string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "stdout")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	old := journalSocket
	journalSocket = sock
	t.Cleanup(func() { journalSocket = old; ln.Close() })

	streams := make(chan []string, 4)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				var lines []string
				sc := bufio.NewScanner(c)
				for sc.Scan() {
					lines = append(lines, sc.Text())
					// Header (7 lines) plus the first line of output.
					if len(lines) == 8 {
						streams <- lines
					}
				}
			}(conn)
		}
	}()
	return streams
}

func TestJournalStreamHeader(t *testing.T) {
	streams := fakeJournal(t)

	f, err := journalStream("keystone/api\nforged", journalPriorityErr)
	if err != nil {
		t.Fatalf("journalStream: %v", err)
	}
	fmt.Fprintln(f, "hello")
	f.Close()

	got := <-streams
	want := []string{"keystone/api_forged", "", "3", "0", "0", "0", "0", "hello"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("stream = %q, want %q", got, want)
	}
}

// TestComponentOutputDoesNotDependOnTheAgent is the property that matters: the
// component's stdout is a socket journald reads, not a pipe the agent reads.
// With a pipe, the agent going away means SIGPIPE on the component's next write.
func TestComponentOutputDoesNotDependOnTheAgent(t *testing.T) {
	streams := fakeJournal(t)

	h, err := New().Start(context.Background(), Options{
		Name:    "chatty",
		Command: "/bin/sh",
		Args:    []string{"-c", "echo tick; sleep 30"},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	ph := h.(*ProcessHandle)
	t.Cleanup(func() { ph.cmd.Process.Kill(); ph.cmd.Wait() })

	target, err := os.Readlink(fmt.Sprintf("/proc/%d/fd/1", ph.pid))
	if err != nil {
		t.Fatalf("readlink fd 1: %v", err)
	}
	if !strings.HasPrefix(target, "socket:") {
		t.Errorf("component stdout is %s, want a journald socket", target)
	}

	select {
	case got := <-streams:
		if got[0] != "keystone/chatty" || got[7] != "tick" {
			t.Errorf("journald received %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing reached journald")
	}
}

// TestFallsBackToPipesWithoutJournald: no journald is not a reason to refuse to
// run the component, only to log through the agent as before.
func TestFallsBackToPipesWithoutJournald(t *testing.T) {
	old := journalSocket
	journalSocket = filepath.Join(t.TempDir(), "absent")
	t.Cleanup(func() { journalSocket = old })

	h, err := New().Start(context.Background(), Options{
		Name:    "nojournal",
		Command: "/bin/sh",
		Args:    []string{"-c", "sleep 30"},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	ph := h.(*ProcessHandle)
	t.Cleanup(func() { ph.cmd.Process.Kill(); ph.cmd.Wait() })

	target, err := os.Readlink(fmt.Sprintf("/proc/%d/fd/1", ph.pid))
	if err != nil {
		t.Fatalf("readlink fd 1: %v", err)
	}
	if !strings.HasPrefix(target, "pipe:") {
		t.Errorf("component stdout is %s, want the fallback pipe", target)
	}
}
