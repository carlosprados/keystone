package signing_test

import (
	"os/exec"
	"strings"
	"testing"
)

// The agent verifies signatures and never makes them. A gateway in a customer's
// plant is the most exposed thing in the system, and one carrying signing
// machinery hands whoever takes it a head start on forging updates for the rest
// of the fleet.
//
// That is an invariant, not a convention, so it is checked rather than written
// down: an import added in a hurry three releases from now would otherwise put
// the signer on every device without anybody noticing.
func TestAgentDoesNotLinkSigning(t *testing.T) {
	const signingPkg = "github.com/carlosprados/keystone/internal/signing"

	for _, bin := range []string{"./../../cmd/keystone", "./../../cmd/keystoneserver"} {
		out, err := exec.Command("go", "list", "-deps", bin).Output()
		if err != nil {
			t.Fatalf("go list -deps %s: %v", bin, err)
		}
		for dep := range strings.FieldsSeq(string(out)) {
			if dep == signingPkg {
				t.Errorf("%s links %s: the agent must never be able to sign", bin, signingPkg)
			}
		}
	}
}

// The reverse of the same rule: keystonectl is where signing lives, so if this
// stops being true the commands have been moved and the test above is passing
// for the wrong reason.
func TestKeystonectlLinksSigning(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "./../../cmd/keystonectl").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	if !strings.Contains(string(out), "github.com/carlosprados/keystone/internal/signing") {
		t.Error("keystonectl does not link internal/signing; has the signing command moved?")
	}
}
