package runner

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"syscall"
	"time"

	sysrt "github.com/carlosprados/keystone/internal/runtime"
)

// ProcessHandle holds the running process information.
type ProcessHandle struct {
	PID       int
	Cmd       *exec.Cmd
	Name      string
	StartedAt time.Time
	Done      chan error // signaled when process exits
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
	return &ProcessHandle{PID: cmd.Process.Pid, Cmd: cmd, Name: opts.Command, StartedAt: time.Now(), Done: make(chan error, 1)}, nil
}

// Stop sends SIGTERM to the process group and waits, then SIGKILL on timeout.
func (r *ProcessRunner) Stop(ctx context.Context, h *ProcessHandle, timeout time.Duration) error {
	if h == nil || h.Cmd == nil || h.Cmd.Process == nil {
		return nil
	}
	// Send SIGTERM to the group
	pgid := -h.Cmd.Process.Pid
	_ = syscall.Kill(pgid, syscall.SIGTERM)
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-h.Done:
		return err
	case <-time.After(timeout):
		_ = syscall.Kill(pgid, syscall.SIGKILL)
		select {
		case err := <-h.Done:
			return err
		case <-time.After(3 * time.Second):
			return fmt.Errorf("process did not exit after SIGKILL")
		}
	}
}

// StopPIDs sends SIGTERM to a list of PIDs and waits, then SIGKILL on timeout.
func (r *ProcessRunner) StopPIDs(pids []int, timeout time.Duration) error {
	if len(pids) == 0 {
		return nil
	}
	for _, pid := range pids {
		if pid <= 0 {
			continue
		}
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}

	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		allGone := true
		for _, pid := range pids {
			if sysrt.IsProcessRunning(pid) {
				allGone = false
				break
			}
		}
		if allGone {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Force kill survivors
	for _, pid := range pids {
		if sysrt.IsProcessRunning(pid) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
	return nil
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
func (r *ProcessRunner) RunManaged(ctx context.Context, name string, opts Options, hc HealthConfig, policy RestartPolicy, maxRetries int, onStart func(*ProcessHandle), onHealth func(bool), onExit func(error)) error {
	if hc.Interval == 0 {
		hc.Interval = 10 * time.Second
	}
	if hc.Timeout == 0 {
		hc.Timeout = 3 * time.Second
	}
	if hc.FailureThreshold == 0 {
		hc.FailureThreshold = 3
	}

	retries := 0
	for {
		// Start
		handle, err := r.Start(ctx, opts)
		if err != nil {
			retries++
			if maxRetries > 0 && retries > maxRetries {
				errLimit := fmt.Errorf("start failed after %d retries: %w", maxRetries, err)
				if onExit != nil {
					onExit(errLimit)
				}
				return errLimit
			}
			// Wait before retry
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
				continue
			}
		}
		if onStart != nil {
			onStart(handle)
		}

		// Watch process exit
		exitCh := make(chan error, 1)
		go func(h *ProcessHandle) {
			err := h.Cmd.Wait()
			// log process exit for diagnostics
			if err != nil {
				log.Printf("[runner] component=%s pid=%d error=%v msg=process exited with error", name, h.PID, err)
			} else {
				log.Printf("[runner] component=%s pid=%d msg=process exited cleanly", name, h.PID)
			}
			exitCh <- err
			// notify handle.Done for external waiters
			select {
			case h.Done <- err:
			default:
			}
		}(handle)

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
				ticker.Stop()
				// External caller is responsible for stopping the process.
				// Avoid double-stop races with agent.stopComponent.
				return nil
			case err := <-exitCh:
				ticker.Stop()
				// Process exited
				if policy != RestartNever {
					if policy == RestartAlways || (policy == RestartOnFailure && err != nil) {
						retries++
						if maxRetries > 0 && retries > maxRetries {
							errLimit := fmt.Errorf("exited and reached restart limit of %d", maxRetries)
							if onExit != nil {
								onExit(errLimit)
							}
							return errLimit
						}
						// restart
						log.Printf("[runner] component=%s restarts=%d msg=restarting process", name, retries)
						run = false
						break
					}
				}
				// no restart
				if onExit != nil {
					onExit(err)
				}
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
							// run = false will be set when exitCh receives the exit signal
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
		// Wait before loop and restart
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(1 * time.Second):
		}
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
			log.Printf("[runner] component=%s stream=%s %s", name, stream, scanner.Text())
		}
	}
}
