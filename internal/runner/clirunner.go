package runner

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Ensure CLIRunner implements Runner at compile time.
var _ Runner = (*CLIRunner)(nil)

// CLIHandle implements Handle for CLI-managed containers.
type CLIHandle struct {
	containerID string
	name        string
	cli         string
	startedAt   time.Time
	done        chan error
}

// ID returns the container ID.
func (h *CLIHandle) ID() string { return h.containerID }

// Name returns the component name.
func (h *CLIHandle) Name() string { return h.name }

// StartedAt returns when the container started.
func (h *CLIHandle) StartedAt() time.Time { return h.startedAt }

// Done returns a channel that receives an error when the container exits.
func (h *CLIHandle) Done() <-chan error { return h.done }

// Type returns "container".
func (h *CLIHandle) Type() string { return "container" }

// ContainerID returns the full container ID (container-specific accessor).
func (h *CLIHandle) ContainerID() string { return h.containerID }

// CLI returns the CLI tool name (docker, nerdctl, podman).
func (h *CLIHandle) CLI() string { return h.cli }

// DoneChan returns the done channel for writing (internal use).
func (h *CLIHandle) DoneChan() chan error { return h.done }

// CLIRunner implements Runner using docker/nerdctl/podman CLI.
// This is a fallback when containerd socket is not accessible.
type CLIRunner struct {
	cli     string        // "docker", "nerdctl", "podman"
	timeout time.Duration // Default timeout for CLI commands
}

// NewCLIRunner creates a new CLIRunner by detecting available CLI tools.
// It prefers nerdctl > docker > podman.
func NewCLIRunner() (*CLIRunner, error) {
	for _, cli := range []string{"nerdctl", "docker", "podman"} {
		if _, err := exec.LookPath(cli); err == nil {
			log.Printf("[clirunner] using %s as container CLI", cli)
			return &CLIRunner{cli: cli, timeout: 30 * time.Second}, nil
		}
	}
	return nil, fmt.Errorf("no container CLI found (nerdctl, docker, podman)")
}

// NewCLIRunnerWithCLI creates a CLIRunner with a specific CLI tool.
func NewCLIRunnerWithCLI(cli string) (*CLIRunner, error) {
	if _, err := exec.LookPath(cli); err != nil {
		return nil, fmt.Errorf("CLI tool %s not found: %w", cli, err)
	}
	return &CLIRunner{cli: cli, timeout: 30 * time.Second}, nil
}

// Start launches a container using the CLI and returns a handle.
func (r *CLIRunner) Start(ctx context.Context, opts Options) (Handle, error) {
	if opts.Image == "" {
		return nil, fmt.Errorf("empty image")
	}

	// Build run arguments
	args := r.buildRunArgs(opts)
	args = append(args, opts.Image)

	// Add command and args if specified
	if opts.Command != "" {
		args = append(args, opts.Command)
		args = append(args, opts.Args...)
	}

	// Run container
	cmd := exec.CommandContext(ctx, r.cli, args...)
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("%s run failed: %s", r.cli, string(exitErr.Stderr))
		}
		return nil, fmt.Errorf("%s run failed: %w", r.cli, err)
	}

	containerID := strings.TrimSpace(string(output))
	if containerID == "" {
		return nil, fmt.Errorf("%s run returned empty container ID", r.cli)
	}

	handle := &CLIHandle{
		containerID: containerID,
		name:        opts.Name,
		cli:         r.cli,
		startedAt:   time.Now(),
		done:        make(chan error, 1),
	}

	// Monitor container in background
	go r.monitorContainer(ctx, handle)

	log.Printf("[clirunner] component=%s container_id=%s cli=%s msg=container started", opts.Name, containerID[:12], r.cli)
	return handle, nil
}

// Stop stops a container using the CLI.
func (r *CLIRunner) Stop(ctx context.Context, h Handle, timeout time.Duration) error {
	ch, ok := h.(*CLIHandle)
	if !ok {
		return fmt.Errorf("invalid handle type for CLIRunner: expected *CLIHandle, got %T", h)
	}
	if ch == nil || ch.containerID == "" {
		return nil
	}

	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	// Stop container with timeout
	stopArgs := []string{"stop", "-t", strconv.Itoa(int(timeout.Seconds())), ch.containerID}
	cmd := exec.CommandContext(ctx, r.cli, stopArgs...)
	if err := cmd.Run(); err != nil {
		log.Printf("[clirunner] warning: %s stop failed: %v", r.cli, err)
	}

	// Wait for container to exit
	select {
	case <-ch.done:
		// Container exited
	case <-time.After(timeout + 5*time.Second):
		// Force kill
		log.Printf("[clirunner] timeout waiting for container %s, forcing kill", ch.containerID[:12])
		killArgs := []string{"kill", ch.containerID}
		killCmd := exec.CommandContext(ctx, r.cli, killArgs...)
		if err := killCmd.Run(); err != nil {
			log.Printf("[clirunner] warning: %s kill failed: %v", r.cli, err)
		}
	case <-ctx.Done():
		return ctx.Err()
	}

	// Remove container
	rmArgs := []string{"rm", "-f", ch.containerID}
	rmCmd := exec.CommandContext(ctx, r.cli, rmArgs...)
	if err := rmCmd.Run(); err != nil {
		log.Printf("[clirunner] warning: %s rm failed: %v", r.cli, err)
	}

	log.Printf("[clirunner] component=%s container_id=%s msg=container stopped", ch.name, ch.containerID[:12])
	return nil
}

// RunManaged starts a container with health monitoring and restart policies.
func (r *CLIRunner) RunManaged(ctx context.Context, name string, opts Options, hc HealthConfig, policy RestartPolicy, maxRetries int, onStart func(Handle), onHealth func(bool), onExit func(error)) error {
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
			log.Printf("[clirunner] component=%s error=%v msg=start attempt failed", name, err)
			retries++
			if maxRetries > 0 && retries > maxRetries {
				errLimit := fmt.Errorf("start failed after %d retries: %w", maxRetries, err)
				if onExit != nil {
					onExit(errLimit)
				}
				return errLimit
			}
			backoff := calculateBackoff(retries)
			log.Printf("[clirunner] component=%s msg=waiting %v before retry attempt %d", name, backoff, retries)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
				continue
			}
		}

		ch := handle.(*CLIHandle)
		if onStart != nil {
			onStart(handle)
		}

		// Watch container exit
		exitCh := make(chan error, 1)
		go func(h *CLIHandle) {
			err := <-h.done
			exitCh <- err
		}(ch)

		// Health loop
		failures := 0
		ticker := time.NewTicker(hc.Interval)
		time.Sleep(200 * time.Millisecond)
		lastHealthSet := false
		lastHealthy := false

		run := true
		for run {
			select {
			case <-ctx.Done():
				ticker.Stop()
				return nil
			case err := <-exitCh:
				ticker.Stop()
				if err != nil {
					log.Printf("[clirunner] component=%s container_id=%s error=%v msg=container exited with error", name, ch.containerID[:12], err)
				} else {
					log.Printf("[clirunner] component=%s container_id=%s msg=container exited cleanly", name, ch.containerID[:12])
				}
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
						log.Printf("[clirunner] component=%s restarts=%d msg=restarting container", name, retries)
						// Cleanup before restart
						rmArgs := []string{"rm", "-f", ch.containerID}
						rmCmd := exec.CommandContext(ctx, r.cli, rmArgs...)
						_ = rmCmd.Run()
						run = false
						break
					}
				}
				if onExit != nil {
					onExit(err)
				}
				return err
			case <-ticker.C:
				if hc.Check == "" {
					continue
				}
				ok := r.probeHealth(ctx, hc, ch)
				if ok {
					failures = 0
				} else {
					failures++
					if failures >= hc.FailureThreshold {
						if policy == RestartAlways {
							log.Printf("[clirunner] component=%s msg=health check failed %d times, stopping for restart", name, failures)
							stopCtx, stopCancel := context.WithTimeout(ctx, 10*time.Second)
							_ = r.Stop(stopCtx, handle, 5*time.Second)
							stopCancel()
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

		backoff := calculateBackoff(retries)
		log.Printf("[clirunner] component=%s msg=waiting %v before restart attempt %d", name, backoff, retries)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
	}
}

// buildRunArgs builds the CLI arguments for running a container.
func (r *CLIRunner) buildRunArgs(opts Options) []string {
	args := []string{"run", "-d", "--name", fmt.Sprintf("keystone-%s-%d", opts.Name, time.Now().UnixNano())}

	// Environment variables
	for _, env := range opts.Env {
		args = append(args, "-e", env)
	}

	// Working directory
	if opts.WorkingDir != "" {
		args = append(args, "-w", opts.WorkingDir)
	}

	// Hostname
	if opts.Hostname != "" {
		args = append(args, "--hostname", opts.Hostname)
	}

	// User
	if opts.User != "" {
		args = append(args, "-u", opts.User)
	}

	// Mounts
	for _, m := range opts.Mounts {
		mountType := m.Type
		if mountType == "" {
			mountType = "bind"
		}
		if mountType == "bind" {
			mountOpt := fmt.Sprintf("%s:%s", m.Source, m.Target)
			if m.ReadOnly {
				mountOpt += ":ro"
			}
			args = append(args, "-v", mountOpt)
		} else {
			// --mount syntax for other types
			mountOpt := fmt.Sprintf("type=%s,source=%s,target=%s", mountType, m.Source, m.Target)
			if m.ReadOnly {
				mountOpt += ",readonly"
			}
			args = append(args, "--mount", mountOpt)
		}
	}

	// Ports
	for _, p := range opts.Ports {
		protocol := p.Protocol
		if protocol == "" {
			protocol = "tcp"
		}
		hostIP := p.HostIP
		if hostIP == "" {
			hostIP = "0.0.0.0"
		}
		portOpt := fmt.Sprintf("%s:%d:%d/%s", hostIP, p.HostPort, p.ContainerPort, protocol)
		args = append(args, "-p", portOpt)
	}

	// Resources
	if opts.Resources.MemoryMB > 0 {
		args = append(args, "-m", fmt.Sprintf("%dm", opts.Resources.MemoryMB))
	}
	if opts.Resources.CPUShares > 0 {
		args = append(args, "--cpu-shares", strconv.FormatInt(opts.Resources.CPUShares, 10))
	}
	if opts.Resources.CPUQuota > 0 {
		args = append(args, "--cpu-quota", strconv.FormatInt(opts.Resources.CPUQuota, 10))
	}
	if opts.Resources.PidsLimit > 0 {
		args = append(args, "--pids-limit", strconv.FormatInt(opts.Resources.PidsLimit, 10))
	}

	// Privileged
	if opts.Privileged {
		args = append(args, "--privileged")
	}

	// Network mode
	if opts.NetworkMode != "" {
		args = append(args, "--network", opts.NetworkMode)
	}

	// Labels
	for k, v := range opts.Labels {
		args = append(args, "--label", fmt.Sprintf("%s=%s", k, v))
	}

	// Pull policy (docker/podman use --pull, nerdctl uses --pull)
	if opts.PullPolicy != "" {
		pullArg := opts.PullPolicy
		// Normalize pull policy
		switch pullArg {
		case "if-not-present":
			pullArg = "missing" // docker/podman terminology
		}
		args = append(args, "--pull", pullArg)
	}

	return args
}

// monitorContainer monitors the container and sends exit status to done channel.
func (r *CLIRunner) monitorContainer(ctx context.Context, h *CLIHandle) {
	// Use 'wait' command to wait for container to exit
	cmd := exec.CommandContext(ctx, r.cli, "wait", h.containerID)
	output, err := cmd.Output()
	if err != nil {
		h.done <- err
		return
	}

	exitCodeStr := strings.TrimSpace(string(output))
	exitCode, _ := strconv.Atoi(exitCodeStr)
	if exitCode != 0 {
		h.done <- fmt.Errorf("container exited with code %d", exitCode)
	} else {
		h.done <- nil
	}
}

// probeHealth performs a health check for a CLI-managed container.
func (r *CLIRunner) probeHealth(ctx context.Context, hc HealthConfig, ch *CLIHandle) bool {
	u := hc.Check
	if u == "" {
		return true
	}

	// HTTP/HTTPS and TCP probes work the same
	if hasPrefix(u, "http://") || hasPrefix(u, "https://") || hasPrefix(u, "tcp://") {
		return ProbeHealth(hc, Options{}, nil)
	}

	// Command probe - exec into container
	if hasPrefix(u, "cmd:") {
		cmdStr := u[len("cmd:"):]
		execCtx, cancel := context.WithTimeout(ctx, hc.Timeout)
		defer cancel()

		execArgs := []string{"exec", ch.containerID, "/bin/sh", "-c", cmdStr}
		cmd := exec.CommandContext(execCtx, r.cli, execArgs...)
		return cmd.Run() == nil
	}

	return true
}

// StreamLogs streams container logs to the log output.
func (r *CLIRunner) StreamLogs(ctx context.Context, h *CLIHandle) {
	args := []string{"logs", "-f", h.containerID}
	cmd := exec.CommandContext(ctx, r.cli, args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Printf("[clirunner] warning: failed to get stdout pipe for logs: %v", err)
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		log.Printf("[clirunner] warning: failed to get stderr pipe for logs: %v", err)
		return
	}

	if err := cmd.Start(); err != nil {
		log.Printf("[clirunner] warning: failed to start logs command: %v", err)
		return
	}

	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			select {
			case <-ctx.Done():
				return
			default:
				log.Printf("[clirunner] component=%s stream=stdout %s", h.name, scanner.Text())
			}
		}
	}()

	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			select {
			case <-ctx.Done():
				return
			default:
				log.Printf("[clirunner] component=%s stream=stderr %s", h.name, scanner.Text())
			}
		}
	}()

	_ = cmd.Wait()
}
