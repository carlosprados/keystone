package runner

import (
	"strings"
	"testing"
)

// argAfter returns the values of every occurrence of flag in args.
func argValues(args []string, flag string) []string {
	var out []string
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			out = append(out, args[i+1])
		}
	}
	return out
}

// TestBuildRunArgsNetworkAliasDefaultsToComponentName pins the behaviour that
// makes service discovery work at all: the container name carries a timestamp,
// so the alias is the only stable name a sibling can resolve.
func TestBuildRunArgsNetworkAliasDefaultsToComponentName(t *testing.T) {
	r := &CLIRunner{cli: "docker"}
	args := r.buildRunArgs(Options{Name: "solver-service", NetworkMode: "rotaflux-net"})

	got := argValues(args, "--network-alias")
	if len(got) != 1 || got[0] != "solver-service" {
		t.Fatalf("expected the component name as the default alias, got %v", got)
	}
}

// TestBuildRunArgsNetworkAliasExplicit verifies declared aliases replace the
// default rather than adding to it.
func TestBuildRunArgsNetworkAliasExplicit(t *testing.T) {
	r := &CLIRunner{cli: "docker"}
	args := r.buildRunArgs(Options{
		Name:           "solver-service",
		NetworkMode:    "rotaflux-net",
		NetworkAliases: []string{"solver", "solver.internal"},
	})

	got := argValues(args, "--network-alias")
	if len(got) != 2 || got[0] != "solver" || got[1] != "solver.internal" {
		t.Fatalf("expected the declared aliases, got %v", got)
	}
}

// TestBuildRunArgsNoAliasOnSharedNetworks guards against emitting a flag the
// CLI rejects: only user-defined networks have an embedded DNS resolver.
func TestBuildRunArgsNoAliasOnSharedNetworks(t *testing.T) {
	for _, mode := range []string{"", "host", "none", "bridge", "default", "container:other"} {
		r := &CLIRunner{cli: "docker"}
		args := r.buildRunArgs(Options{Name: "api", NetworkMode: mode})
		if got := argValues(args, "--network-alias"); len(got) != 0 {
			t.Errorf("network_mode=%q: expected no alias, got %v", mode, got)
		}
	}
}

// TestBuildRunArgsContainerNameIsUnique documents why the alias exists: two
// starts of the same component never share a container name.
func TestBuildRunArgsContainerNameIsUnique(t *testing.T) {
	r := &CLIRunner{cli: "docker"}
	first := argValues(r.buildRunArgs(Options{Name: "api"}), "--name")
	second := argValues(r.buildRunArgs(Options{Name: "api"}), "--name")

	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("expected exactly one --name, got %v and %v", first, second)
	}
	if first[0] == second[0] {
		t.Fatalf("container name %q was reused across starts", first[0])
	}
	if !strings.HasPrefix(first[0], "keystone-api-") {
		t.Fatalf("unexpected container name %q", first[0])
	}
}

// TestHealthExecArgsHasNoShell is the FROM scratch case: an exec probe must
// reach the container as a bare argv, because there is no /bin/sh to route it
// through. A shell here would fail every probe, and a failed probe rolls the
// deployment back while reporting that it did the right thing.
func TestHealthExecArgsHasNoShell(t *testing.T) {
	args := healthExecArgs(HealthConfig{Exec: []string{"/rotaflux", "healthcheck"}}, "abc123")

	want := []string{"exec", "abc123", "/rotaflux", "healthcheck"}
	if len(args) != len(want) {
		t.Fatalf("got %v, want %v", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("got %v, want %v", args, want)
		}
	}
}

// TestHealthExecArgsShellProbeStillUsesShell keeps the old "cmd:" form working
// for images that do have an interpreter.
func TestHealthExecArgsShellProbeStillUsesShell(t *testing.T) {
	args := healthExecArgs(HealthConfig{Check: "cmd:test -f /tmp/ready"}, "abc123")

	want := []string{"exec", "abc123", "/bin/sh", "-c", "test -f /tmp/ready"}
	if len(args) != len(want) {
		t.Fatalf("got %v, want %v", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("got %v, want %v", args, want)
		}
	}
}
