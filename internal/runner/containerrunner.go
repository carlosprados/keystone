package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/containers"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/containerd/containerd/v2/pkg/netns"
	"github.com/containerd/containerd/v2/pkg/oci"
	gocni "github.com/containerd/go-cni"
	imagev1 "github.com/opencontainers/image-spec/specs-go/v1"
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

	// network-specific (bridge mode with CNI)
	netNS            *netns.NetNS
	cniNamespaceOpts []gocni.NamespaceOpts
	cniConfigured    bool
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

	cniOnce      sync.Once
	cni          gocni.CNI
	cniErr       error
	cniConfDir   string
	cniPluginDir []string
	cniNetNSDir  string
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

	r := &ContainerRunner{
		client:      c,
		namespace:   cfg.Namespace,
		snapshotter: cfg.Snapshotter,
		cniConfDir:  defaultString(os.Getenv("KEYSTONE_CNI_CONF_DIR"), "/etc/cni/net.d"),
		cniNetNSDir: defaultString(os.Getenv("KEYSTONE_CNI_NETNS_DIR"), "/var/run/netns"),
	}
	pluginDirs := os.Getenv("KEYSTONE_CNI_PLUGIN_DIRS")
	if pluginDirs == "" {
		r.cniPluginDir = []string{"/opt/cni/bin", "/usr/lib/cni"}
	} else {
		for _, p := range strings.Split(pluginDirs, ":") {
			p = strings.TrimSpace(p)
			if p != "" {
				r.cniPluginDir = append(r.cniPluginDir, p)
			}
		}
		if len(r.cniPluginDir) == 0 {
			r.cniPluginDir = []string{"/opt/cni/bin", "/usr/lib/cni"}
		}
	}
	return r, nil
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

	// 2. Create container spec (optionally with an explicit netns for bridge mode)
	var netNS *netns.NetNS
	specOpts := r.buildSpecOpts(opts, image)
	if defaultString(opts.NetworkMode, "bridge") == "bridge" {
		if err := os.MkdirAll(r.cniNetNSDir, 0o755); err != nil {
			return nil, fmt.Errorf("failed to create cni netns dir %s: %w", r.cniNetNSDir, err)
		}
		ns, err := netns.NewNetNS(r.cniNetNSDir)
		if err != nil {
			return nil, fmt.Errorf("failed to create network namespace: %w", err)
		}
		netNS = ns
		specOpts = append(specOpts, oci.WithLinuxNamespace(specs.LinuxNamespace{
			Type: specs.NetworkNamespace,
			Path: ns.GetPath(),
		}))
	}

	// 3. Create container
	containerID := fmt.Sprintf("keystone-%s-%d", opts.Name, time.Now().UnixNano())
	container, err := r.client.NewContainer(ctx, containerID,
		client.WithImage(image),
		client.WithNewSnapshot(containerID+"-snapshot", image),
		client.WithNewSpec(specOpts...),
	)
	if err != nil {
		if netNS != nil {
			_ = netNS.Remove()
		}
		return nil, fmt.Errorf("failed to create container: %w", err)
	}

	// 4. Create task (this prepares the container process)
	task, err := container.NewTask(ctx, cio.NewCreator(cio.WithStdio))
	if err != nil {
		if delErr := container.Delete(ctx, client.WithSnapshotCleanup); delErr != nil {
			log.Printf("[container] warning: failed to cleanup container after task creation failure: %v", delErr)
		}
		if netNS != nil {
			_ = netNS.Remove()
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
		if netNS != nil {
			_ = netNS.Remove()
		}
		return nil, fmt.Errorf("failed to start task: %w", err)
	}

	// 5.5 Setup CNI networking (bridge mode)
	var cniNSOpts []gocni.NamespaceOpts
	cniConfigured := false
	if defaultString(opts.NetworkMode, "bridge") == "bridge" {
		if err := r.ensureCNI(); err != nil {
			_ = task.Kill(ctx, syscall.SIGKILL)
			_, _ = task.Delete(ctx)
			_ = container.Delete(ctx, client.WithSnapshotCleanup)
			if netNS != nil {
				_ = netNS.Remove()
			}
			return nil, fmt.Errorf("failed to initialize CNI bridge networking: %w", err)
		}
		cniNSOpts = buildCNINamespaceOpts(opts)
		if _, err := r.cni.Setup(ctx, containerID, netNS.GetPath(), cniNSOpts...); err != nil {
			_ = task.Kill(ctx, syscall.SIGKILL)
			_, _ = task.Delete(ctx)
			_ = container.Delete(ctx, client.WithSnapshotCleanup)
			if netNS != nil {
				_ = netNS.Remove()
			}
			return nil, fmt.Errorf("failed to setup CNI networking: %w", err)
		}
		cniConfigured = true
	}

	// 6. Create handle
	handle := &ContainerHandle{
		containerID: containerID,
		name:        opts.Name,
		startedAt:   time.Now(),
		done:        make(chan error, 1),
		container:   container,
		task:        task,
		netNS:       netNS,
		// Keep namespace opts so we can execute CNI DEL with the same capabilities.
		cniNamespaceOpts: cniNSOpts,
		cniConfigured:    cniConfigured,
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
	r.cleanupContainerResources(ctx, ch)

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
			log.Printf("[container] component=%s error=%v msg=start attempt failed", name, err)
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
						r.cleanupContainerResources(ctx, ch)
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

	// Mount image-declared volumes (Config.Volumes) like Docker/nerdctl do.
	specOpts = append(specOpts, r.withImageDeclaredVolumes(image, opts))

	// Command and args
	if opts.Command != "" {
		args := append([]string{opts.Command}, opts.Args...)
		specOpts = append(specOpts, oci.WithProcessArgs(args...))
	}

	// Environment
	env := append([]string{}, opts.Env...)
	if opts.Hostname != "" && !hasEnvKey(env, "HOSTNAME") {
		env = append(env, "HOSTNAME="+opts.Hostname)
	}
	if len(env) > 0 {
		specOpts = append(specOpts, oci.WithEnv(env))
	}

	// Working directory
	if opts.WorkingDir != "" {
		specOpts = append(specOpts, oci.WithProcessCwd(opts.WorkingDir))
	}

	// Hostname
	if opts.Hostname != "" {
		specOpts = append(specOpts, oci.WithHostname(opts.Hostname))
	}

	// Parity with common container CLIs: ensure localhost/DNS resolution files are present.
	specOpts = append(specOpts, oci.WithHostHostsFile, oci.WithHostResolvconf)

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

func (r *ContainerRunner) withImageDeclaredVolumes(image client.Image, opts Options) oci.SpecOpts {
	return func(ctx context.Context, _ oci.Client, c *containers.Container, s *oci.Spec) error {
		declared, err := readImageDeclaredVolumes(ctx, image)
		if err != nil {
			return fmt.Errorf("read image-declared volumes: %w", err)
		}
		if len(declared) == 0 {
			return nil
		}

		explicitTargets := map[string]struct{}{}
		for _, m := range opts.Mounts {
			t := strings.TrimSpace(m.Target)
			if t == "" {
				continue
			}
			explicitTargets[t] = struct{}{}
		}

		var mounts []specs.Mount
		rootDir, err := resolveImageVolumeRootDir()
		if err != nil {
			return err
		}
		for _, target := range declared {
			if _, exists := explicitTargets[target]; exists {
				continue
			}
			hostPath := filepath.Join(rootDir, sanitizeComponentVolumeKey(opts.Name), sanitizeVolumeDestination(target))
			if err := os.MkdirAll(hostPath, 0o755); err != nil {
				return fmt.Errorf("create image volume path %s: %w", hostPath, err)
			}
			mounts = append(mounts, specs.Mount{
				Destination: target,
				Source:      hostPath,
				Type:        "bind",
				Options:     []string{"rbind", "rw"},
			})
		}
		if len(mounts) == 0 {
			return nil
		}
		return oci.WithMounts(mounts)(ctx, nil, c, s)
	}
}

func readImageDeclaredVolumes(ctx context.Context, image client.Image) ([]string, error) {
	configDesc, err := image.Config(ctx)
	if err != nil {
		return nil, err
	}
	if !images.IsConfigType(configDesc.MediaType) {
		return nil, fmt.Errorf("unsupported image config media type %q", configDesc.MediaType)
	}

	raw, err := content.ReadBlob(ctx, image.ContentStore(), configDesc)
	if err != nil {
		return nil, err
	}

	var img imagev1.Image
	if err := json.Unmarshal(raw, &img); err != nil {
		return nil, err
	}

	vols := make([]string, 0, len(img.Config.Volumes))
	for dst := range img.Config.Volumes {
		dst = strings.TrimSpace(dst)
		if dst == "" {
			continue
		}
		vols = append(vols, dst)
	}
	sort.Strings(vols)
	return vols, nil
}

func sanitizeVolumeDestination(dst string) string {
	s := strings.TrimSpace(strings.TrimPrefix(dst, "/"))
	if s == "" {
		return "_root"
	}
	s = strings.ReplaceAll(s, "/", "_")
	return s
}

func sanitizeComponentVolumeKey(name string) string {
	s := strings.TrimSpace(name)
	if s == "" {
		return "_default"
	}
	return strings.ReplaceAll(strings.ReplaceAll(s, "/", "_"), " ", "_")
}

func defaultImageVolumeRootDir() string {
	return defaultString(os.Getenv("KEYSTONE_IMAGE_VOLUME_DIR"), "runtime/containervolumes/image")
}

func hasEnvKey(env []string, key string) bool {
	prefix := key + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return true
		}
	}
	return false
}

func resolveImageVolumeRootDir() (string, error) {
	root := defaultImageVolumeRootDir()
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve image volume root %q: %w", root, err)
	}
	return absRoot, nil
}

func (r *ContainerRunner) ensureCNI() error {
	r.cniOnce.Do(func() {
		c, err := gocni.New(
			gocni.WithPluginDir(r.cniPluginDir),
			gocni.WithPluginConfDir(r.cniConfDir),
			gocni.WithPluginMaxConfNum(1),
		)
		if err != nil {
			r.cniErr = err
			return
		}
		if err := c.Load(gocni.WithLoNetwork, gocni.WithDefaultConf); err != nil {
			r.cniErr = err
			return
		}
		r.cni = c
	})
	return r.cniErr
}

func buildCNINamespaceOpts(opts Options) []gocni.NamespaceOpts {
	var nsOpts []gocni.NamespaceOpts
	if len(opts.Labels) > 0 {
		nsOpts = append(nsOpts, gocni.WithLabels(opts.Labels))
	}
	if len(opts.Ports) > 0 {
		portMap := make([]gocni.PortMapping, 0, len(opts.Ports))
		for _, p := range opts.Ports {
			proto := defaultString(strings.ToLower(p.Protocol), "tcp")
			if proto != "tcp" && proto != "udp" {
				proto = "tcp"
			}
			hostIP := strings.TrimSpace(p.HostIP)
			// CNI portmap treats empty host IP as "all interfaces".
			// Passing 0.0.0.0 can lead to rules that never match.
			if hostIP == "0.0.0.0" || hostIP == "::" {
				hostIP = ""
			}
			portMap = append(portMap, gocni.PortMapping{
				HostPort:      int32(p.HostPort),
				ContainerPort: int32(p.ContainerPort),
				Protocol:      proto,
				HostIP:        hostIP,
			})
		}
		nsOpts = append(nsOpts, gocni.WithCapabilityPortMap(portMap))
	}
	return nsOpts
}

func (r *ContainerRunner) cleanupContainerResources(ctx context.Context, ch *ContainerHandle) {
	if ch == nil {
		return
	}
	cleanupCtx, cancel := cleanupContext(ctx, 8*time.Second)
	defer cancel()

	if ch.cniConfigured && ch.netNS != nil && r.cni != nil {
		if err := r.cni.Remove(cleanupCtx, ch.containerID, ch.netNS.GetPath(), ch.cniNamespaceOpts...); err != nil {
			log.Printf("[container] warning: cni remove failed for %s: %v", ch.containerID, err)
		}
		ch.cniConfigured = false
	}
	if ch.task != nil {
		if _, err := ch.task.Delete(cleanupCtx); err != nil {
			log.Printf("[container] warning: task delete for %s failed: %v", ch.containerID, err)
		}
	}
	if ch.container != nil {
		if err := ch.container.Delete(cleanupCtx, client.WithSnapshotCleanup); err != nil {
			log.Printf("[container] warning: container delete for %s failed: %v", ch.containerID, err)
		}
	}
	if ch.netNS != nil {
		if err := ch.netNS.Remove(); err != nil {
			log.Printf("[container] warning: netns remove failed for %s: %v", ch.containerID, err)
		}
		ch.netNS = nil
	}
}

func defaultString(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func cleanupContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	if ctx != nil && ctx.Err() == nil {
		return context.WithTimeout(ctx, timeout)
	}
	return context.WithTimeout(context.Background(), timeout)
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
				Env:  buildHealthExecEnv(opts.Env),
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

func buildHealthExecEnv(containerEnv []string) []string {
	env := make([]string, 0, len(containerEnv)+2)
	hasPath := false
	for _, kv := range containerEnv {
		if strings.HasPrefix(kv, "PATH=") {
			hasPath = true
		}
		env = append(env, kv)
	}
	if !hasPath {
		// Keep a sane default PATH for shell-based probes.
		env = append(env, "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	}
	return env
}
