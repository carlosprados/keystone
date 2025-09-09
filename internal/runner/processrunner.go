package runner

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"syscall"
	"time"

	sysrt "github.com/carlosprados/keystone/internal/runtime"
	"github.com/rs/zerolog/log"
)

// ProcessHandle holds the running process information.
type ProcessHandle struct {
	PID       int
	Cmd       *exec.Cmd
	Name      string
	StartedAt time.Time
}

// Options specifies how to start the process.
type Options struct {
	Name       string
	Command    string
	Args       []string
	Env        []string
	WorkingDir string
	NoFile     uint64 // RLIMIT_NOFILE
}

// ProcessRunner starts and stops native processes.
type ProcessRunner struct{}

func New() *ProcessRunner { return &ProcessRunner{} }

// Start launches the process and returns a handle.
func (r *ProcessRunner) Start(ctx context.Context, opts Options) (*ProcessHandle, error) {
	if opts.Command == "" {
		return nil, fmt.Errorf("empty command")
	}
	if err := sysrt.ApplyRlimits(opts.NoFile); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, opts.Command, opts.Args...)
	cmd.Env = append(os.Environ(), opts.Env...)
	if opts.WorkingDir != "" {
		cmd.Dir = opts.WorkingDir
	}
	// Put in its own process group to manage signals for children
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Log capture pipes
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	if stdout != nil {
		go streamLogs(ctx, opts.Name, "stdout", stdout)
	}
	if stderr != nil {
		go streamLogs(ctx, opts.Name, "stderr", stderr)
	}
	return &ProcessHandle{PID: cmd.Process.Pid, Cmd: cmd, Name: opts.Command, StartedAt: time.Now()}, nil
}

// Stop sends SIGTERM to the process group and waits, then SIGKILL on timeout.
func (r *ProcessRunner) Stop(ctx context.Context, h *ProcessHandle, timeout time.Duration) error {
	if h == nil || h.Cmd == nil || h.Cmd.Process == nil {
		return nil
	}
	// Send SIGTERM to the group
	pgid := -h.Cmd.Process.Pid
	_ = syscall.Kill(pgid, syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- h.Cmd.Wait() }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		return err
	case <-time.After(timeout):
		_ = syscall.Kill(pgid, syscall.SIGKILL)
		return <-done
	}
}

// HealthConfig defines how to probe a process.
type HealthConfig struct {
	Check            string        // http://..., tcp://..., cmd:...
	Interval         time.Duration // default 10s
	Timeout          time.Duration // default 3s
	FailureThreshold int           // default 3
}

type RestartPolicy string

const (
	RestartNever     RestartPolicy = "never"
	RestartOnFailure RestartPolicy = "on-failure"
	RestartAlways    RestartPolicy = "always"
)

// RunManaged starts a process and keeps it healthy according to health config and restart policy.
// Returns when context is canceled or a terminal error occurs.
func (r *ProcessRunner) RunManaged(ctx context.Context, name string, opts Options, hc HealthConfig, policy RestartPolicy, onStart func(*ProcessHandle), onHealth func(bool)) error {
	if hc.Interval == 0 {
		hc.Interval = 10 * time.Second
	}
	if hc.Timeout == 0 {
		hc.Timeout = 3 * time.Second
	}
	if hc.FailureThreshold == 0 {
		hc.FailureThreshold = 3
	}

	for {
		// Start
		handle, err := r.Start(ctx, opts)
		if err != nil {
			return err
		}
		if onStart != nil {
			onStart(handle)
		}

		// Watch process exit
		exitCh := make(chan error, 1)
		go func(h *ProcessHandle) { exitCh <- h.Cmd.Wait() }(handle)

		// Health loop ticker
		failures := 0
		ticker := time.NewTicker(hc.Interval)
		// initial warmup small delay
		time.Sleep(200 * time.Millisecond)
		lastHealthSet := false
		lastHealthy := false

		// Inner loop
		run := true
		for run {
			select {
			case <-ctx.Done():
				_ = r.Stop(context.Background(), handle, 5*time.Second)
				return nil
			case err := <-exitCh:
				// Process exited
				if err != nil && policy != RestartNever {
					if policy == RestartAlways || policy == RestartOnFailure {
						// restart
						run = false
						break
					}
				}
				// no restart
				return err
			case <-ticker.C:
				if hc.Check == "" {
					continue
				}
				ok := probeOnce(hc, opts)
				if ok {
					failures = 0
				} else {
					failures++
					if failures >= hc.FailureThreshold {
						// unhealthy -> restart only for Always, else keep waiting for process exit
						if policy == RestartAlways {
							_ = r.Stop(context.Background(), handle, 5*time.Second)
							run = false
						}
					}
				}
				if onHealth != nil {
					if !lastHealthSet || ok != lastHealthy {
						onHealth(ok)
						lastHealthy = ok
						lastHealthSet = true
					}
				}
			}
		}
		ticker.Stop()
		// loop and restart
	}
}

func probeOnce(hc HealthConfig, opts Options) bool {
	// Support http://, https://
	if len(opts.Env) > 0 {
		_ = opts.Env
	} // placeholder for future env substitution
	if u := hc.Check; u != "" {
		if hasPrefix(u, "http://") || hasPrefix(u, "https://") {
			client := &http.Client{Timeout: hc.Timeout}
			req, _ := http.NewRequest("GET", u, nil)
			resp, err := client.Do(req)
			if err != nil {
				return false
			}
			defer resp.Body.Close()
			return resp.StatusCode >= 200 && resp.StatusCode < 300
		}
		if hasPrefix(u, "tcp://") {
			addr := u[len("tcp://"):]
			d := net.Dialer{Timeout: hc.Timeout}
			conn, err := d.Dial("tcp", addr)
			if err != nil {
				return false
			}
			_ = conn.Close()
			return true
		}
		if hasPrefix(u, "cmd:") {
			cmdStr := u[len("cmd:"):]
			ctx, cancel := context.WithTimeout(context.Background(), hc.Timeout)
			defer cancel()
			cmd := exec.CommandContext(ctx, "/bin/sh", "-c", cmdStr)
			if opts.WorkingDir != "" {
				cmd.Dir = opts.WorkingDir
			}
			return cmd.Run() == nil
		}
	}
	return true
}

func hasPrefix(s, prefix string) bool { return len(s) >= len(prefix) && s[:len(prefix)] == prefix }

func streamLogs(ctx context.Context, name, stream string, r io.Reader) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
			log.Info().Str("component", name).Str("stream", stream).Msg(scanner.Text())
		}
	}
}
