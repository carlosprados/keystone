package agent

import (
	"strings"
	"testing"

	"github.com/carlosprados/keystone/internal/recipe"
)

// In secure mode (the default), the integrity policy must reject artifacts
// before any download happens. These cases all fail at the gate, so no network
// access is required.
func TestEnsureArtifactSecureModeRejections(t *testing.T) {
	a := &Agent{} // insecureSkipVerify=false, trustPool=nil

	cases := []struct {
		name    string
		adef    recipe.Artifact
		wantSub string
	}{
		{"missing sha256", recipe.Artifact{URI: "https://x/a.tgz"}, "sha256 is required"},
		{"missing sig_uri", recipe.Artifact{URI: "https://x/a.tgz", SHA256: "abc"}, "sig_uri is required"},
		{"no trust bundle", recipe.Artifact{URI: "https://x/a.tgz", SHA256: "abc", SigURI: "https://x/a.sig"}, "trust bundle"},
	}
	for _, tc := range cases {
		err := a.ensureAndVerifyArtifact(t.TempDir(), t.TempDir(), tc.adef)
		if err == nil {
			t.Errorf("%s: expected rejection, got nil", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.wantSub) {
			t.Errorf("%s: error %q does not contain %q", tc.name, err.Error(), tc.wantSub)
		}
	}
}
