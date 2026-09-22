package agent

import (
	"testing"

	"github.com/carlosprados/keystone/internal/version"
)

// TestHealthReportsAgentVersion: on a device reachable only outbound this is
// the only way to find out what build is deployed there. An empty field would
// be indistinguishable from an old agent that does not report it, so it has to
// arrive on every health response.
func TestHealthReportsAgentVersion(t *testing.T) {
	a := New(Options{InsecureSkipVerify: true})

	h := a.GetHealth()
	if h.AgentVersion == "" {
		t.Fatal("health reports no agent version")
	}
	if h.AgentVersion != version.Version {
		t.Errorf("AgentVersion = %q, want %q", h.AgentVersion, version.Version)
	}
	if h.AgentCommit != version.Commit {
		t.Errorf("AgentCommit = %q, want %q", h.AgentCommit, version.Commit)
	}
}
