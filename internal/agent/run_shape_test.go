package agent

import (
	"strings"
	"testing"

	"github.com/carlosprados/keystone/internal/recipe"
)

func containerRecipe(c recipe.ContainerConfig, h recipe.Health) *recipe.Recipe {
	r := &recipe.Recipe{}
	r.Lifecycle.Run.Type = "container"
	r.Lifecycle.Run.Container = c
	r.Lifecycle.Run.Health = h
	return r
}

// TestValidateRunShapeRejectsBothHealthForms: check and exec say the same thing
// two ways, and silently preferring one would make the other a no-op.
func TestValidateRunShapeRejectsBothHealthForms(t *testing.T) {
	r := containerRecipe(
		recipe.ContainerConfig{Image: "app:sha-abc"},
		recipe.Health{Check: "cmd:true", Exec: []string{"/app", "healthcheck"}},
	)

	err := validateRunShape(r)
	if err == nil || !strings.Contains(err.Error(), "declare one") {
		t.Fatalf("expected a refusal naming the conflict, got %v", err)
	}
}

// TestValidateRunShapeRejectsAliasesOnContainerd guards the project rule that a
// declaration is honoured or refused: CNI has no alias, so accepting the field
// would strand a sibling on a name that never resolves.
func TestValidateRunShapeRejectsAliasesOnContainerd(t *testing.T) {
	r := containerRecipe(recipe.ContainerConfig{
		Image:          "app:sha-abc",
		Runtime:        "containerd",
		NetworkMode:    "rotaflux-net",
		NetworkAliases: []string{"solver"},
	}, recipe.Health{})

	err := validateRunShape(r)
	if err == nil || !strings.Contains(err.Error(), "containerd") {
		t.Fatalf("expected a refusal naming the runtime, got %v", err)
	}
}

// TestValidateRunShapeRejectsUserDefinedNetworkOnContainerd covers the silent
// failure that motivated the check: containerd only branches on host and
// bridge, so a named network falls through both and the container starts on an
// empty network namespace without an error.
func TestValidateRunShapeRejectsUserDefinedNetworkOnContainerd(t *testing.T) {
	r := containerRecipe(recipe.ContainerConfig{
		Image:       "app:sha-abc",
		Runtime:     "containerd",
		NetworkMode: "rotaflux-net",
	}, recipe.Health{})

	err := validateRunShape(r)
	if err == nil || !strings.Contains(err.Error(), "rotaflux-net") {
		t.Fatalf("expected a refusal naming the network, got %v", err)
	}
}

// TestValidateRunShapeAcceptsCNIModesOnContainerd: the three modes CNI does
// know must keep working.
func TestValidateRunShapeAcceptsCNIModesOnContainerd(t *testing.T) {
	for _, mode := range []string{"", "bridge", "host", "none"} {
		r := containerRecipe(recipe.ContainerConfig{
			Image:       "app:sha-abc",
			Runtime:     "containerd",
			NetworkMode: mode,
		}, recipe.Health{})

		if err := validateRunShape(r); err != nil {
			t.Errorf("network_mode=%q: expected it to be accepted, got %v", mode, err)
		}
	}
}

// TestValidateRunShapeRejectsAliasesWithoutUserDefinedNetwork: the default
// bridge, host and none have no embedded resolver, and the CLI rejects the flag
// there anyway.
func TestValidateRunShapeRejectsAliasesWithoutUserDefinedNetwork(t *testing.T) {
	for _, mode := range []string{"", "bridge", "host", "none"} {
		r := containerRecipe(recipe.ContainerConfig{
			Image:          "app:sha-abc",
			Runtime:        "docker",
			NetworkMode:    mode,
			NetworkAliases: []string{"solver"},
		}, recipe.Health{})

		if err := validateRunShape(r); err == nil {
			t.Errorf("network_mode=%q: expected a refusal, got nil", mode)
		}
	}
}

// TestValidateRunShapeAcceptsTheRotafluxShape is the case that motivated all of
// this: a named network, an alias and a probe with no shell behind it.
func TestValidateRunShapeAcceptsTheRotafluxShape(t *testing.T) {
	r := containerRecipe(recipe.ContainerConfig{
		Image:          "registry.lab.enredando.me:5000/rotaflux:sha-abc1234",
		Runtime:        "docker",
		NetworkMode:    "rotaflux-net",
		NetworkAliases: []string{"solver-service"},
	}, recipe.Health{Exec: []string{"/rotaflux", "healthcheck"}})

	if err := validateRunShape(r); err != nil {
		t.Fatalf("expected the recipe to be accepted, got %v", err)
	}
}

// TestValidateRunShapeRejectsAliasesOnProcess: a container-only field on a
// process component would be read as configuration that does nothing.
func TestValidateRunShapeRejectsAliasesOnProcess(t *testing.T) {
	r := &recipe.Recipe{}
	r.Lifecycle.Run.Type = "process"
	r.Lifecycle.Run.Container.NetworkAliases = []string{"solver"}

	if err := validateRunShape(r); err == nil {
		t.Fatal("expected a refusal for network_aliases on a process component")
	}
}
