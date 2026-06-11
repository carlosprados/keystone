package agent

import (
	"crypto/x509"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRecipeFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "r.recipe.toml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestVerifyRecipeFileSignature(t *testing.T) {
	t.Setenv("KEYSTONE_LEAF_CERT", "") // ensure no ambient cert

	// Dev opt-out: never rejects, even for a path with no signature.
	dev := &Agent{insecureSkipVerify: true}
	if err := dev.verifyRecipeFileSignature("/does/not/exist.toml"); err != nil {
		t.Errorf("insecure mode should skip verification, got %v", err)
	}

	// Secure mode, no trust bundle -> rejected.
	noTrust := &Agent{}
	if err := noTrust.verifyRecipeFileSignature(writeRecipeFile(t, "x")); err == nil ||
		!strings.Contains(err.Error(), "no trust bundle") {
		t.Errorf("expected 'no trust bundle' rejection, got %v", err)
	}

	// Secure mode, trust bundle present but missing .sig -> rejected.
	withTrust := &Agent{trustPool: x509.NewCertPool()}
	if err := withTrust.verifyRecipeFileSignature(writeRecipeFile(t, "x")); err == nil ||
		!strings.Contains(err.Error(), "missing detached signature") {
		t.Errorf("expected 'missing detached signature' rejection, got %v", err)
	}

	// Secure mode, .sig present but no certificate available -> rejected.
	rp := writeRecipeFile(t, "x")
	if err := os.WriteFile(rp+".sig", []byte("sig"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := withTrust.verifyRecipeFileSignature(rp); err == nil ||
		!strings.Contains(err.Error(), "no certificate") {
		t.Errorf("expected 'no certificate' rejection, got %v", err)
	}
}
