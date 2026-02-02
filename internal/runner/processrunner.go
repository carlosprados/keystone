package runner

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"os"
	"os/exec"
	"syscall"
	"time"

	sysrt "github.com/carlosprados/keystone/internal/runtime"
)

// Backoff configuration for process restarts
const (
	backoffMin        = 1 * time.Second   // Minimum wait between restarts
	backoffMax        = 60 * time.Second  // Maximum wait between restarts
	backoffMultiplier = 2.0               // Exponential multiplier
	backoffJitter     = 0.25              // Random jitter (25%)
)

// calculateBackoff returns the backoff duration with exponential increase and jitter.
// The backoff doubles with each attempt up to backoffMax.
func calculateBackoff(attempt int) time.Duration {
	if attempt <= 0 {
		attempt = 1
	}

	// Calculate exponential backoff
	backoff := float64(backoffMin)
	for i := 1; i < attempt; i++ {
		backoff *= backoffMultiplier
		if backoff > float64(backoffMax) {
			backoff = float64(backoffMax)
			break
		}
	}

	// Add jitter to avoid thundering herd
	jitter := backoff * backoffJitter * (rand.Float64()*2 - 1) // -25% to +25%
	backoff += jitter

	// Ensure within bounds
	if backoff < float64(backoffMin) {
		backoff = float64(backoffMin)
	}
	if backoff > float64(backoffMax) {
		backoff = float64(backoffMax)
	}

	return time.Duration(backoff)
}

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
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Printf("[runner] warning: failed to capture stdout for %s: %v", opts.Name, err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		log.Printf("[runner] warning: failed to capture stderr for %s: %v", opts.Name, err)
	}
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
	// Send SIGTERM to the process group
	pgid := -h.Cmd.Process.Pid
	if err := syscall.Kill(pgid, syscall.SIGTERM); err != nil {
		log.Printf("[runner] warning: SIGTERM to pgid %d failed: %v", pgid, err)
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-h.Done:
		return err
	case <-time.After(timeout):
		log.Printf("[runner] timeout waiting for process group %d, sending SIGKILL", -pgid)
		if err := syscall.Kill(pgid, syscall.SIGKILL); err != nil {
			log.Printf("[runner] warning: SIGKILL to pgid %d failed: %v", pgid, err)
		}
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
		if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
			log.Printf("[runner] warning: SIGTERM to pid %d failed: %v", pid, err)
		}
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
			log.Printf("[runner] forcing SIGKILL on pid %d after timeout", pid)
			if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
				log.Printf("[runner] warning: SIGKILL to pid %d failed: %v", pid, err)
			}
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
			// Wait before retry with exponential backoff
			backoff := calculateBackoff(retries)
			log.Printf("[runner] component=%s msg=waiting %v before retry attempt %d", name, backoff, retries)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
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
							log.Printf("[runner] component=%s msg=health check failed %d times, stopping for restart", name, failures)
							stopCtx, stopCancel := context.WithTimeout(ctx, 10*time.Second)
							_ = r.Stop(stopCtx, handle, 5*time.Second)
							stopCancel()
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
		// Wait before restart with exponential backoff
		backoff := calculateBackoff(retries)
		log.Printf("[runner] component=%s msg=waiting %v before restart attempt %d", name, backoff, retries)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
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
