package signing_test

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/carlosprados/keystone/internal/security"
	"github.com/carlosprados/keystone/internal/signing"
)

// The test that matters: what keystonectl signs, the agent verifies. Signer and
// verifier must agree on the scheme exactly — sign the SHA-256 digest, and for
// Ed25519 that means the digest is the message and NOT Ed25519ph — so this runs
// the real signing path into the real verification path for every algorithm.
func TestSignedFileVerifiesInTheAgent(t *testing.T) {
	algorithms := []struct {
		name string
		key  func(t *testing.T) crypto.Signer
	}{
		{"RSA-2048", func(t *testing.T) crypto.Signer {
			k, err := rsa.GenerateKey(rand.Reader, 2048)
			if err != nil {
				t.Fatal(err)
			}
			return k
		}},
		{"ECDSA-P256", func(t *testing.T) crypto.Signer {
			k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			return k
		}},
		{"Ed25519", func(t *testing.T) crypto.Signer {
			_, k, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			return k
		}},
	}

	for _, alg := range algorithms {
		t.Run(alg.name, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "artifact.bin")
			if err := os.WriteFile(target, []byte("payload the fleet will run\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			ca, caKey := newCA(t)
			leaf, leafPEM := issueLeaf(t, ca, caKey, alg.key(t))
			keyPath := writeKeyPEM(t, dir, leaf)
			certPath := filepath.Join(dir, "leaf.pem")
			if err := os.WriteFile(certPath, leafPEM, 0o644); err != nil {
				t.Fatal(err)
			}
			bundlePath := filepath.Join(dir, "ca.pem")
			if err := os.WriteFile(bundlePath, encodeCert(ca.Raw), 0o644); err != nil {
				t.Fatal(err)
			}

			sig, err := signing.SignFile(signing.FileBackend{KeyPath: keyPath, CertPath: certPath}, target)
			if err != nil {
				t.Fatalf("SignFile: %v", err)
			}
			sigPath := target + ".sig"
			if err := signing.WriteSignature(sigPath, sig); err != nil {
				t.Fatal(err)
			}

			roots, err := security.LoadTrustBundle(bundlePath)
			if err != nil {
				t.Fatal(err)
			}
			if err := security.VerifyDetached(target, sigPath, certPath, roots); err != nil {
				t.Fatalf("the agent rejected a signature keystonectl produced: %v", err)
			}

			// And the guarantee that makes the signature worth anything.
			if err := os.WriteFile(target, []byte("payload an attacker would rather run\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := security.VerifyDetached(target, sigPath, certPath, roots); err == nil {
				t.Error("a modified file still verified")
			}
		})
	}
}

// A key that does not match the certificate produces a signature no agent will
// accept. SignFile must refuse it at publication time rather than write it.
func TestSignFileRejectsMismatchedKeyAndCert(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "artifact.bin")
	if err := os.WriteFile(target, []byte("payload\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ca, caKey := newCA(t)
	signingKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	otherKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, otherCertPEM := issueLeaf(t, ca, caKey, otherKey)

	keyPath := writeKeyPEM(t, dir, signingKey)
	certPath := filepath.Join(dir, "other.pem")
	if err := os.WriteFile(certPath, otherCertPEM, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := signing.SignFile(signing.FileBackend{KeyPath: keyPath, CertPath: certPath}, target); err == nil {
		t.Error("signing with a key that does not match the certificate succeeded")
	}
}

// Ed25519 must be given crypto.Hash(0). Handing it crypto.SHA256 selects
// Ed25519ph, whose signatures the agent rejects — a one-line mistake that would
// only surface on a device.
func TestEd25519IsNotPrehashed(t *testing.T) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	digest := make([]byte, 32)
	for i := range digest {
		digest[i] = byte(i)
	}

	sig, err := signing.SignDigest(key, digest)
	if err != nil {
		t.Fatalf("SignDigest: %v", err)
	}
	if !ed25519.Verify(key.Public().(ed25519.PublicKey), digest, sig) {
		t.Error("the signature is not plain Ed25519 over the digest")
	}
}

func TestSignDigestRejectsWrongDigestLength(t *testing.T) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := signing.SignDigest(key, []byte("too short")); err == nil {
		t.Error("signing accepted something that is not a SHA-256 digest")
	}
}

// --- helpers -------------------------------------------------------------

func newCA(t *testing.T) (*x509.Certificate, crypto.Signer) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "KeystoneTestCA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, key
}

func issueLeaf(t *testing.T, ca *x509.Certificate, caKey crypto.Signer, leafKey crypto.Signer) (crypto.Signer, []byte) {
	t.Helper()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "keystone-test-signer"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, leafKey.Public(), caKey)
	if err != nil {
		t.Fatal(err)
	}
	return leafKey, encodeCert(der)
}

func encodeCert(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func writeKeyPEM(t *testing.T, dir string, key crypto.Signer) string {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "signer.key")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
