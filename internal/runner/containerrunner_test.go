package runner

import (
	"os"
	"testing"
)

// TestContainerRunnerInterface verifies ContainerRunner implements Runner interface.
func TestContainerRunnerInterface(t *testing.T) {
	// This is a compile-time check via the var _ line in containerrunner.go
	// but we add this test for explicit documentation
	var _ Runner = (*ContainerRunner)(nil)
}

// TestCLIRunnerInterface verifies CLIRunner implements Runner interface.
func TestCLIRunnerInterface(t *testing.T) {
	var _ Runner = (*CLIRunner)(nil)
}

// TestContainerHandleInterface verifies ContainerHandle implements Handle interface.
func TestContainerHandleInterface(t *testing.T) {
	var _ Handle = (*ContainerHandle)(nil)
}

// TestCLIHandleInterface verifies CLIHandle implements Handle interface.
func TestCLIHandleInterface(t *testing.T) {
	var _ Handle = (*CLIHandle)(nil)
}

// TestDefaultContainerRunnerConfig verifies default configuration values.
func TestDefaultContainerRunnerConfig(t *testing.T) {
	cfg := DefaultContainerRunnerConfig()

	if cfg.Socket != "/run/containerd/containerd.sock" {
		t.Errorf("expected default socket /run/containerd/containerd.sock, got %s", cfg.Socket)
	}
	if cfg.Namespace != "keystone" {
		t.Errorf("expected default namespace keystone, got %s", cfg.Namespace)
	}
	if cfg.Snapshotter != "overlayfs" {
		t.Errorf("expected default snapshotter overlayfs, got %s", cfg.Snapshotter)
	}
	if cfg.DefaultRegistry != "docker.io" {
		t.Errorf("expected default registry docker.io, got %s", cfg.DefaultRegistry)
	}
}

// TestContainerRunnerConfigFromEnv verifies configuration from environment.
func TestContainerRunnerConfigFromEnv(t *testing.T) {
	// Set env vars
	os.Setenv("KEYSTONE_CONTAINERD_SOCKET", "/custom/socket.sock")
	os.Setenv("KEYSTONE_CONTAINERD_NAMESPACE", "custom-ns")
	os.Setenv("KEYSTONE_CONTAINER_SNAPSHOTTER", "native")
	os.Setenv("KEYSTONE_CONTAINER_REGISTRY", "gcr.io")
	defer func() {
		os.Unsetenv("KEYSTONE_CONTAINERD_SOCKET")
		os.Unsetenv("KEYSTONE_CONTAINERD_NAMESPACE")
		os.Unsetenv("KEYSTONE_CONTAINER_SNAPSHOTTER")
		os.Unsetenv("KEYSTONE_CONTAINER_REGISTRY")
	}()

	cfg := ContainerRunnerConfigFromEnv()

	if cfg.Socket != "/custom/socket.sock" {
		t.Errorf("expected socket /custom/socket.sock, got %s", cfg.Socket)
	}
	if cfg.Namespace != "custom-ns" {
		t.Errorf("expected namespace custom-ns, got %s", cfg.Namespace)
	}
	if cfg.Snapshotter != "native" {
		t.Errorf("expected snapshotter native, got %s", cfg.Snapshotter)
	}
	if cfg.DefaultRegistry != "gcr.io" {
		t.Errorf("expected registry gcr.io, got %s", cfg.DefaultRegistry)
	}
}

// TestCLIRunnerBuildRunArgs verifies CLI argument building.
func TestCLIRunnerBuildRunArgs(t *testing.T) {
	r := &CLIRunner{cli: "docker"}

	opts := Options{
		Name:        "test",
		Image:       "nginx:latest",
		Env:         []string{"FOO=bar", "BAZ=qux"},
		NetworkMode: "host",
		User:        "1000:1000",
		Mounts: []Mount{
			{Source: "/host/data", Target: "/data", Type: "bind", ReadOnly: false},
		},
		Ports: []PortMapping{
			{HostPort: 8080, ContainerPort: 80, Protocol: "tcp"},
		},
		Resources: ResourceLimits{
			MemoryMB:  512,
			CPUShares: 1024,
		},
	}

	args := r.buildRunArgs(opts)

	// Check that expected flags are present
	contains := func(args []string, flag string) bool {
		for _, a := range args {
			if a == flag {
				return true
			}
		}
		return false
	}
	containsPair := func(args []string, flag, value string) bool {
		for i := 0; i < len(args)-1; i++ {
			if args[i] == flag && args[i+1] == value {
				return true
			}
		}
		return false
	}

	if !contains(args, "run") {
		t.Error("expected 'run' in args")
	}
	if !contains(args, "-d") {
		t.Error("expected '-d' in args")
	}
	if !containsPair(args, "--network", "host") {
		t.Error("expected '--network host' in args")
	}
	if !containsPair(args, "-u", "1000:1000") {
		t.Error("expected '-u 1000:1000' in args")
	}
	if !containsPair(args, "-m", "512m") {
		t.Error("expected '-m 512m' in args")
	}
}

// Integration test - only runs if TEST_CONTAINERD is set
func TestContainerRunner_Integration(t *testing.T) {
	if os.Getenv("TEST_CONTAINERD") == "" {
		t.Skip("skipping containerd integration test (set TEST_CONTAINERD=1 to run)")
	}

	// This would test actual container operations
	// Skipped by default since it requires containerd to be running
}
