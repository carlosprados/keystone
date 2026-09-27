package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/carlosprados/keystone/internal/security"
	"github.com/carlosprados/keystone/internal/selfupdate"
)

func mintCert(t *testing.T, serial int64, parent *x509.Certificate, parentKey *ecdsa.PrivateKey, isCA bool, eku []x509.ExtKeyUsage) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(serial),
		Subject:               pkix.Name{CommonName: "test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		ExtKeyUsage:           eku,
		BasicConstraintsValid: true,
		IsCA:                  isCA,
		KeyUsage:              x509.KeyUsageDigitalSignature,
	}
	if isCA {
		tmpl.KeyUsage |= x509.KeyUsageCertSign
		parent, parentKey = tmpl, key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, &key.PublicKey, parentKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, key
}

func writePEM(t *testing.T, path string, cert *x509.Certificate) {
	t.Helper()
	b := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// stageProposal lays out what the agent stages — a real ELF for this machine,
// its signature by a leaf with the given EKU, and the leaf certificate — and
// points KEYSTONE_TRUST_BUNDLE at the CA that issued it.
func stageProposal(t *testing.T, eku []x509.ExtKeyUsage) string {
	t.Helper()
	ca, caKey := mintCert(t, 1, nil, nil, true, nil)
	leaf, leafKey := mintCert(t, 2, ca, caKey, false, eku)

	root := t.TempDir()
	bundle := filepath.Join(root, "ca.pem")
	writePEM(t, bundle, ca)
	t.Setenv("KEYSTONE_TRUST_BUNDLE", bundle)

	dir := filepath.Join(root, "staged")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, selfupdate.BinaryName), bin, 0o755); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(bin)
	sig, err := ecdsa.SignASN1(rand.Reader, leafKey, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, selfupdate.StagedSig), sig, 0o644); err != nil {
		t.Fatal(err)
	}
	writePEM(t, filepath.Join(dir, selfupdate.StagedCert), leaf)
	return dir
}

// TestVerifyUpdateFollowsTheNoEKUTransition: the gate must apply the same
// transition policy as the agent that proposed the update. If it did not, a
// no-EKU signer the agent accepted would be refused here and every update
// would roll back.
func TestVerifyUpdateFollowsTheNoEKUTransition(t *testing.T) {
	t.Chdir(t.TempDir()) // clock evidence is read relative to the working directory
	t.Cleanup(func() { security.AllowNoEKUSigners(false) })
	serverAuth := []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	codeSigning := []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning}

	cases := []struct {
		name  string
		eku   []x509.ExtKeyUsage
		allow string
		want  int
	}{
		{"codeSigning signs", codeSigning, "", 0},
		{"no EKU is refused", nil, "", 1},
		{"no EKU passes with the opt-in", nil, "true", 0},
		{"serverAuth is refused even with the opt-in", serverAuth, "true", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			security.AllowNoEKUSigners(false)
			t.Setenv("KEYSTONE_ALLOW_NO_EKU_SIGNERS", tc.allow)
			dir := stageProposal(t, tc.eku)
			if got := runVerifyUpdate([]string{dir}); got != tc.want {
				t.Errorf("exit %d, want %d", got, tc.want)
			}
		})
	}
}
