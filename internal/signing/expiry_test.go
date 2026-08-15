package signing_test

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/carlosprados/keystone/internal/security"
	"github.com/carlosprados/keystone/internal/signing"
)

// Certificate expiry is the reason X.509 was chosen over a pinned key: it is
// what makes a compromised signer stop working on its own. So it has to
// actually bite.
func TestExpiredCertificateIsRejected(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "artifact.bin")
	if err := os.WriteFile(target, []byte("payload\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A leaf that was valid last month and expired yesterday.
	notBefore := time.Now().Add(-30 * 24 * time.Hour)
	notAfter := time.Now().Add(-24 * time.Hour)
	certPath, keyPath, bundlePath := materialValidBetween(t, dir, notBefore, notAfter)

	sig, err := signing.SignFile(signing.FileBackend{KeyPath: keyPath, CertPath: certPath}, target)
	if err != nil {
		t.Fatalf("signing with an expired certificate is fine; publishing it is not: %v", err)
	}
	sigPath := target + ".sig"
	if err := signing.WriteSignature(sigPath, sig); err != nil {
		t.Fatal(err)
	}

	roots, err := security.LoadTrustBundle(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := security.VerifyDetached(target, sigPath, certPath, roots); err == nil {
		t.Fatal("an expired certificate verified")
	}

	// And the point of VerifyDetachedAt: judged at a time when it was still
	// valid, the same signature is good. This is what lets a device whose clock
	// cannot be trusted verify against evidence instead of guessing.
	at := notBefore.Add(24 * time.Hour)
	if err := security.VerifyDetachedAt(target, sigPath, certPath, roots, at); err != nil {
		t.Errorf("valid at %s, but verification failed: %v", at.Format(time.RFC3339), err)
	}
}

// The failure a device with no RTC hits on first boot: its clock says 1970, so
// a perfectly good certificate is "not yet valid". Verified here as the concrete
// motivation for internal/clock.
func TestCertificateNotYetValidAtAnEpochClock(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "artifact.bin")
	if err := os.WriteFile(target, []byte("payload\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	certPath, keyPath, bundlePath := materialValidBetween(t,
		dir, time.Now().Add(-time.Hour), time.Now().Add(90*24*time.Hour))

	sig, err := signing.SignFile(signing.FileBackend{KeyPath: keyPath, CertPath: certPath}, target)
	if err != nil {
		t.Fatal(err)
	}
	sigPath := target + ".sig"
	if err := signing.WriteSignature(sigPath, sig); err != nil {
		t.Fatal(err)
	}
	roots, err := security.LoadTrustBundle(bundlePath)
	if err != nil {
		t.Fatal(err)
	}

	epoch := time.Unix(0, 0)
	if err := security.VerifyDetachedAt(target, sigPath, certPath, roots, epoch); err == nil {
		t.Error("a certificate issued today verified against a 1970 clock")
	}
	// Same signature, same certificate, judged at a defensible time.
	if err := security.VerifyDetachedAt(target, sigPath, certPath, roots, time.Now()); err != nil {
		t.Errorf("verification failed at the real time: %v", err)
	}
}

// materialValidBetween issues a CA and a leaf whose validity is the window
// given, writes all three files, and returns their paths.
func materialValidBetween(t *testing.T, dir string, notBefore, notAfter time.Time) (certPath, keyPath, bundlePath string) {
	t.Helper()

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "KeystoneExpiryTestCA"},
		NotBefore:             notBefore.Add(-time.Hour),
		NotAfter:              notAfter.Add(365 * 24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, caKey.Public(), caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "keystone-expiry-test-signer"},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, ca, leafKey.Public(), caKey)
	if err != nil {
		t.Fatal(err)
	}

	certPath = filepath.Join(dir, "leaf.pem")
	if err := os.WriteFile(certPath, encodeCert(leafDER), 0o644); err != nil {
		t.Fatal(err)
	}
	bundlePath = filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(bundlePath, encodeCert(caDER), 0o644); err != nil {
		t.Fatal(err)
	}
	keyPath = writeKeyPEM(t, dir, crypto.Signer(leafKey))
	return certPath, keyPath, bundlePath
}
