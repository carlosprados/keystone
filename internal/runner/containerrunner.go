package runner

import (
	"context"
	"fmt"
	"log"
	"os"
	"syscall"
	"time"

	"github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/containerd/containerd/v2/pkg/oci"
	"github.com/opencontainers/runtime-spec/specs-go"
)

// Ensure ContainerRunner implements Runner at compile time.
var _ Runner = (*ContainerRunner)(nil)

// ContainerHandle implements Handle for containers.
type ContainerHandle struct {
	containerID string
	name        string
	startedAt   time.Time
	done        chan error

	// containerd-specific
	container client.Container
	task      client.Task
}

// ID returns the container ID.
func (h *ContainerHandle) ID() string { return h.containerID }

// Name returns the component name.
func (h *ContainerHandle) Name() string { return h.name }

// StartedAt returns when the container started.
func (h *ContainerHandle) StartedAt() time.Time { return h.startedAt }

// Done returns a channel that receives an error when the container exits.
func (h *ContainerHandle) Done() <-chan error { return h.done }

// Type returns "container".
func (h *ContainerHandle) Type() string { return "container" }

// ContainerID returns the full container ID (container-specific accessor).
func (h *ContainerHandle) ContainerID() string { return h.containerID }

// DoneChan returns the done channel for writing (internal use).
func (h *ContainerHandle) DoneChan() chan error { return h.done }

// Task returns the containerd task (container-specific accessor).
func (h *ContainerHandle) Task() client.Task { return h.task }

// Container returns the containerd container (container-specific accessor).
func (h *ContainerHandle) Container() client.Container { return h.container }

// ContainerRunner implements Runner for containerd containers.
type ContainerRunner struct {
	client      *client.Client
	namespace   string
	snapshotter string
}

// ContainerRunnerConfig holds configuration for the container runner.
type ContainerRunnerConfig struct {
	// Socket path for containerd (default: /run/containerd/containerd.sock)
	Socket string

	// Namespace for containers (default: "keystone")
	Namespace string

	// Snapshotter to use (default: "overlayfs")
	Snapshotter string

	// DefaultRegistry for images (default: "docker.io")
	DefaultRegistry string
}

// DefaultContainerRunnerConfig returns a ContainerRunnerConfig with sensible defaults.
func DefaultContainerRunnerConfig() ContainerRunnerConfig {
	return ContainerRunnerConfig{
		Socket:          "/run/containerd/containerd.sock",
		Namespace:       "keystone",
		Snapshotter:     "overlayfs",
		DefaultRegistry: "docker.io",
	}
}

// ContainerRunnerConfigFromEnv returns a ContainerRunnerConfig populated from environment variables.
func ContainerRunnerConfigFromEnv() ContainerRunnerConfig {
	cfg := DefaultContainerRunnerConfig()
	if socket := os.Getenv("KEYSTONE_CONTAINERD_SOCKET"); socket != "" {
		cfg.Socket = socket
	}
	if namespace := os.Getenv("KEYSTONE_CONTAINERD_NAMESPACE"); namespace != "" {
		cfg.Namespace = namespace
	}
	if snapshotter := os.Getenv("KEYSTONE_CONTAINER_SNAPSHOTTER"); snapshotter != "" {
		cfg.Snapshotter = snapshotter
	}
	if registry := os.Getenv("KEYSTONE_CONTAINER_REGISTRY"); registry != "" {
		cfg.DefaultRegistry = registry
	}
	return cfg
}

// NewContainerRunner creates a new ContainerRunner connected to containerd.
func NewContainerRunner(cfg ContainerRunnerConfig) (*ContainerRunner, error) {
	c, err := client.New(cfg.Socket,
		client.WithDefaultNamespace(cfg.Namespace),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to containerd at %s: %w", cfg.Socket, err)
	}

	return &ContainerRunner{
		client:      c,
		namespace:   cfg.Namespace,
		snapshotter: cfg.Snapshotter,
	}, nil
}

// Close closes the containerd client connection.
func (r *ContainerRunner) Close() error {
	if r.client != nil {
		return r.client.Close()
	}
	return nil
}

// Start launches a container and returns a handle.
func (r *ContainerRunner) Start(ctx context.Context, opts Options) (Handle, error) {
	if opts.Image == "" {
		return nil, fmt.Errorf("empty image")
	}

	// 1. Pull image if needed
	image, err := r.ensureImage(ctx, opts.Image, opts.PullPolicy)
	if err != nil {
		return nil, fmt.Errorf("failed to ensure image: %w", err)
	}

	// 2. Create container spec
	specOpts := r.buildSpecOpts(opts, image)

	// 3. Create container
	containerID := fmt.Sprintf("keystone-%s-%d", opts.Name, time.Now().UnixNano())
	container, err := r.client.NewContainer(ctx, containerID,
		client.WithImage(image),
		client.WithNewSnapshot(containerID+"-snapshot", image),
		client.WithNewSpec(specOpts...),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create container: %w", err)
	}

	// 4. Create task (this prepares the container process)
	task, err := container.NewTask(ctx, cio.NewCreator(cio.WithStdio))
	if err != nil {
		if delErr := container.Delete(ctx, client.WithSnapshotCleanup); delErr != nil {
			log.Printf("[container] warning: failed to cleanup container after task creation failure: %v", delErr)
		}
		return nil, fmt.Errorf("failed to create task: %w", err)
	}

	// 5. Start task
	if err := task.Start(ctx); err != nil {
		if _, delErr := task.Delete(ctx); delErr != nil {
			log.Printf("[container] warning: failed to delete task after start failure: %v", delErr)
		}
		if delErr := container.Delete(ctx, client.WithSnapshotCleanup); delErr != nil {
			log.Printf("[container] warning: failed to cleanup container after start failure: %v", delErr)
		}
		return nil, fmt.Errorf("failed to start task: %w", err)
	}

	// 6. Create handle
	handle := &ContainerHandle{
		containerID: containerID,
		name:        opts.Name,
		startedAt:   time.Now(),
		done:        make(chan error, 1),
		container:   container,
		task:        task,
	}

	// 7. Monitor task exit in background
	go r.monitorTask(ctx, handle)

	log.Printf("[container] component=%s container_id=%s msg=container started", opts.Name, containerID)
	return handle, nil
}

// Stop sends SIGTERM to the container and waits, then SIGKILL on timeout.
func (r *ContainerRunner) Stop(ctx context.Context, h Handle, timeout time.Duration) error {
	ch, ok := h.(*ContainerHandle)
	if !ok {
		return fmt.Errorf("invalid handle type for ContainerRunner: expected *ContainerHandle, got %T", h)
	}
	if ch == nil || ch.task == nil {
		return nil
	}

	// 1. Send SIGTERM
	if err := ch.task.Kill(ctx, syscall.SIGTERM); err != nil {
		log.Printf("[container] warning: SIGTERM to container %s failed: %v", ch.containerID, err)
	}

	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	// 2. Wait for exit or timeout
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-ch.done:
		// Exited gracefully
	case <-time.After(timeout):
		// Force kill
		log.Printf("[container] timeout waiting for container %s, sending SIGKILL", ch.containerID)
		if err := ch.task.Kill(ctx, syscall.SIGKILL); err != nil {
			log.Printf("[container] warning: SIGKILL to container %s failed: %v", ch.containerID, err)
		}
		select {
		case <-ch.done:
		case <-time.After(5 * time.Second):
			return fmt.Errorf("container did not exit after SIGKILL")
		}
	}

	// 3. Delete task and container
	if _, err := ch.task.Delete(ctx); err != nil {
		log.Printf("[container] warning: task delete for %s failed: %v", ch.containerID, err)
	}
	if err := ch.container.Delete(ctx, client.WithSnapshotCleanup); err != nil {
		log.Printf("[container] warning: container delete for %s failed: %v", ch.containerID, err)
	}

	log.Printf("[container] component=%s container_id=%s msg=container stopped", ch.name, ch.containerID)
	return nil
}

// RunManaged starts a container with health monitoring and restart policies.
// It blocks until the context is canceled or a terminal error occurs.
func (r *ContainerRunner) RunManaged(ctx context.Context, name string, opts Options, hc HealthConfig, policy RestartPolicy, maxRetries int, onStart func(Handle), onHealth func(bool), onExit func(error)) error {
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
			log.Printf("[container] component=%s msg=waiting %v before retry attempt %d", name, backoff, retries)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
				continue
			}
		}

		ch := handle.(*ContainerHandle)
		if onStart != nil {
			onStart(handle)
		}

		// Watch container exit
		exitCh := make(chan error, 1)
		go func(h *ContainerHandle) {
			err := <-h.done
			exitCh <- err
		}(ch)

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
				// External caller is responsible for stopping the container.
				return nil
			case err := <-exitCh:
				ticker.Stop()
				// Container exited
				if err != nil {
					log.Printf("[container] component=%s container_id=%s error=%v msg=container exited with error", name, ch.containerID, err)
				} else {
					log.Printf("[container] component=%s container_id=%s msg=container exited cleanly", name, ch.containerID)
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
						// restart
						log.Printf("[container] component=%s restarts=%d msg=restarting container", name, retries)
						// Cleanup before restart
						if _, delErr := ch.task.Delete(ctx); delErr != nil {
							log.Printf("[container] warning: task delete failed during restart: %v", delErr)
						}
						if delErr := ch.container.Delete(ctx, client.WithSnapshotCleanup); delErr != nil {
							log.Printf("[container] warning: container delete failed during restart: %v", delErr)
						}
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
				ok := ProbeHealthContainer(hc, opts, ch)
				if ok {
					failures = 0
				} else {
					failures++
					if failures >= hc.FailureThreshold {
						// unhealthy -> restart only for Always, else keep waiting for exit
						if policy == RestartAlways {
							log.Printf("[container] component=%s msg=health check failed %d times, stopping for restart", name, failures)
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
		log.Printf("[container] component=%s msg=waiting %v before restart attempt %d", name, backoff, retries)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
	}
}

// ensureImage pulls the image if needed based on pull policy.
func (r *ContainerRunner) ensureImage(ctx context.Context, ref string, pullPolicy string) (client.Image, error) {
	// Check if image exists
	image, err := r.client.GetImage(ctx, ref)
	if err == nil && pullPolicy != "always" {
		log.Printf("[container] image=%s msg=using existing image", ref)
		return image, nil
	}

	if pullPolicy == "never" {
		return nil, fmt.Errorf("image %s not found and pull policy is 'never'", ref)
	}

	// Pull image
	log.Printf("[container] image=%s msg=pulling image", ref)
	image, err = r.client.Pull(ctx, ref,
		client.WithPullUnpack,
		client.WithPullSnapshotter(r.snapshotter),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to pull image %s: %w", ref, err)
	}

	log.Printf("[container] image=%s msg=image pulled successfully", ref)
	return image, nil
}

// buildSpecOpts builds the OCI spec options from runner Options.
func (r *ContainerRunner) buildSpecOpts(opts Options, image client.Image) []oci.SpecOpts {
	specOpts := []oci.SpecOpts{
		oci.WithImageConfig(image),
	}

	// Command and args
	if opts.Command != "" {
		args := append([]string{opts.Command}, opts.Args...)
		specOpts = append(specOpts, oci.WithProcessArgs(args...))
	}

	// Environment
	if len(opts.Env) > 0 {
		specOpts = append(specOpts, oci.WithEnv(opts.Env))
	}

	// Working directory
	if opts.WorkingDir != "" {
		specOpts = append(specOpts, oci.WithProcessCwd(opts.WorkingDir))
	}

	// Hostname
	if opts.Hostname != "" {
		specOpts = append(specOpts, oci.WithHostname(opts.Hostname))
	}

	// User
	if opts.User != "" {
		specOpts = append(specOpts, oci.WithUser(opts.User))
	}

	// Mounts
	for _, m := range opts.Mounts {
		mountType := m.Type
		if mountType == "" {
			mountType = "bind"
		}
		mountOpts := []string{}
		if mountType == "bind" {
			mountOpts = append(mountOpts, "rbind")
		}
		if m.ReadOnly {
			mountOpts = append(mountOpts, "ro")
		} else {
			mountOpts = append(mountOpts, "rw")
		}
		specOpts = append(specOpts, oci.WithMounts([]specs.Mount{
			{
				Destination: m.Target,
				Source:      m.Source,
				Type:        mountType,
				Options:     mountOpts,
			},
		}))
	}

	// Resources
	if opts.Resources.MemoryMB > 0 {
		specOpts = append(specOpts, oci.WithMemoryLimit(uint64(opts.Resources.MemoryMB)*1024*1024))
	}
	if opts.Resources.CPUShares > 0 {
		specOpts = append(specOpts, oci.WithCPUShares(uint64(opts.Resources.CPUShares)))
	}
	if opts.Resources.PidsLimit > 0 {
		specOpts = append(specOpts, oci.WithPidsLimit(opts.Resources.PidsLimit))
	}

	// Privileged
	if opts.Privileged {
		specOpts = append(specOpts, oci.WithPrivileged)
	}

	// Network mode
	if opts.NetworkMode == "host" {
		specOpts = append(specOpts, oci.WithHostNamespace(specs.NetworkNamespace))
	}

	return specOpts
}

// monitorTask monitors the task exit and sends the result to the handle's done channel.
func (r *ContainerRunner) monitorTask(ctx context.Context, h *ContainerHandle) {
	exitCh, err := h.task.Wait(ctx)
	if err != nil {
		h.done <- err
		return
	}

	status := <-exitCh
	if status.Error() != nil {
		h.done <- status.Error()
	} else if status.ExitCode() != 0 {
		h.done <- fmt.Errorf("container exited with code %d", status.ExitCode())
	} else {
		h.done <- nil
	}
}

// ProbeHealthContainer performs a health check probe for a container.
// It supports http://, https://, tcp://, and cmd: probes.
func ProbeHealthContainer(hc HealthConfig, opts Options, ch *ContainerHandle) bool {
	u := hc.Check
	if u == "" {
		return true
	}

	// HTTP/HTTPS and TCP probes work the same as for processes
	if hasPrefix(u, "http://") || hasPrefix(u, "https://") || hasPrefix(u, "tcp://") {
		return ProbeHealth(hc, opts, nil)
	}

	// Command probe - exec into container
	if hasPrefix(u, "cmd:") {
		cmdStr := u[len("cmd:"):]
		ctx, cancel := context.WithTimeout(context.Background(), hc.Timeout)
		defer cancel()

		// Exec into container
		execID := fmt.Sprintf("health-%d", time.Now().UnixNano())
		proc, err := ch.task.Exec(ctx, execID,
			&specs.Process{
				Args: []string{"/bin/sh", "-c", cmdStr},
				Cwd:  "/",
			},
			cio.NullIO,
		)
		if err != nil {
			log.Printf("[container] component=%s msg=health check exec failed: %v", ch.name, err)
			return false
		}
		defer func() {
			if _, delErr := proc.Delete(ctx); delErr != nil {
				log.Printf("[container] component=%s msg=health check exec delete failed: %v", ch.name, delErr)
			}
		}()

		if err := proc.Start(ctx); err != nil {
			log.Printf("[container] component=%s msg=health check exec start failed: %v", ch.name, err)
			return false
		}

		exitCh, err := proc.Wait(ctx)
		if err != nil {
			return false
		}
		status := <-exitCh
		return status.ExitCode() == 0
	}

	return true
}
