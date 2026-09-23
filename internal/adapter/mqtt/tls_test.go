package mqtt

import (
	"crypto/tls"
	"testing"
)

// TestTLSConfigSkipVerifyWithoutCA is the case that did not work: asking to
// skip verification and nothing else built no TLS config at all, so the flag
// was ignored and the connection still failed against a self-signed broker —
// the one situation the flag exists for.
func TestTLSConfigSkipVerifyWithoutCA(t *testing.T) {
	a := &Adapter{cfg: Config{TLSVerify: false}}

	cfg, err := a.buildTLSConfig()
	if err != nil {
		t.Fatalf("building a skip-verify config failed: %v", err)
	}
	if !cfg.InsecureSkipVerify {
		t.Error("InsecureSkipVerify was not set")
	}
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Errorf("MinVersion = %x, want TLS 1.2", cfg.MinVersion)
	}
}

// TestTLSConfigVerifiesByDefault: the default must stay strict. A device that
// silently stopped checking its broker's identity would be worse than one that
// fails to connect.
func TestTLSConfigVerifiesByDefault(t *testing.T) {
	a := &Adapter{cfg: DefaultConfig()}

	cfg, err := a.buildTLSConfig()
	if err != nil {
		t.Fatalf("building the default config failed: %v", err)
	}
	if cfg.InsecureSkipVerify {
		t.Error("the default config skips verification")
	}
}
