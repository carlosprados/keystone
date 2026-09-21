package runner

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestNamespaceMissingErrorNamesMoby: the whole value of this error is that it
// points at the real cause, so the Docker case must be spelled out rather than
// left for the reader to infer from a list.
func TestNamespaceMissingErrorNamesMoby(t *testing.T) {
	err := &NamespaceMissingError{Configured: "keystone", Existing: []string{"moby", "k8s.io"}}

	msg := err.Error()
	if !strings.Contains(msg, `"keystone"`) {
		t.Errorf("error does not name the configured namespace: %s", msg)
	}
	if !strings.Contains(msg, "moby") || !strings.Contains(msg, "runtime = \"docker\"") {
		t.Errorf("error does not point at Docker's namespace: %s", msg)
	}
}

// TestNamespaceMissingErrorWithoutMoby still has to list what is there, so the
// operator can see the typo.
func TestNamespaceMissingErrorWithoutMoby(t *testing.T) {
	err := &NamespaceMissingError{Configured: "keystone", Existing: []string{"k8s.io"}}

	msg := err.Error()
	if !strings.Contains(msg, "k8s.io") {
		t.Errorf("error does not list the existing namespaces: %s", msg)
	}
	if strings.Contains(msg, "moby") {
		t.Errorf("error invents a Docker namespace that is not there: %s", msg)
	}
}

// TestEnsureNetworkExistsSkipsSharedModes: the check costs a subprocess, and
// the shared modes have no network to inspect.
func TestEnsureNetworkExistsSkipsSharedModes(t *testing.T) {
	// A CLI that does not exist: if the check ran, this would fail.
	r := &CLIRunner{cli: "definitely-not-a-container-cli", timeout: time.Second}

	for _, mode := range []string{"", "host", "none", "bridge", "default"} {
		if err := r.ensureNetworkExists(context.Background(), mode); err != nil {
			t.Errorf("network_mode=%q: expected the check to be skipped, got %v", mode, err)
		}
	}
}
