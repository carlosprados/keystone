package security

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
	"strings"
	"testing"
	"time"
)

type issuer struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

var serial int64

func mint(t *testing.T, cn string, parent *issuer, isCA bool, eku []x509.ExtKeyUsage) *issuer {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial++
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(serial),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		ExtKeyUsage:           eku,
		BasicConstraintsValid: true,
		IsCA:                  isCA,
		KeyUsage:              x509.KeyUsageDigitalSignature,
	}
	if isCA {
		tmpl.KeyUsage |= x509.KeyUsageCertSign
	}
	signerCert, signerKey := tmpl, key
	if parent != nil {
		signerCert, signerKey = parent.cert, parent.key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, signerCert, &key.PublicKey, signerKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &issuer{cert: cert, key: key}
}

// signed writes a file, its detached signature by leaf, and a certificate file
// holding leaf followed by chain; it returns the three paths.
func signed(t *testing.T, leaf *issuer, chain ...*issuer) (file, sig, cert string) {
	t.Helper()
	dir := t.TempDir()
	file = filepath.Join(dir, "recipe.toml")
	if err := os.WriteFile(file, []byte("[metadata]\nname = \"x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(file)
	digest := sha256.Sum256(b)
	s, err := ecdsa.SignASN1(rand.Reader, leaf.key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	sig = file + ".sig"
	if err := os.WriteFile(sig, s, 0o644); err != nil {
		t.Fatal(err)
	}
	var pemBytes []byte
	for _, c := range append([]*issuer{leaf}, chain...) {
		pemBytes = append(pemBytes, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.cert.Raw})...)
	}
	cert = file + ".crt"
	if err := os.WriteFile(cert, pemBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	return file, sig, cert
}

func pool(roots ...*issuer) *x509.CertPool {
	p := x509.NewCertPool()
	for _, r := range roots {
		p.AddCert(r.cert)
	}
	return p
}

var codeSigning = []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning}

func TestACodeSigningCertificateSigns(t *testing.T) {
	ca := mint(t, "ca", nil, true, nil)
	leaf := mint(t, "signer", ca, false, codeSigning)
	f, s, c := signed(t, leaf)
	if err := VerifyDetached(f, s, c, pool(ca)); err != nil {
		t.Fatalf("a codeSigning certificate was refused: %v", err)
	}
}

// TestOnlyCodeSigningCertificatesSign is the property: the verifier asked for
// no key usage, so x509 checked ServerAuth, and any no-EKU or serverAuth
// certificate from the trust bundle could sign — a broker's TLS certificate, or
// a device's client certificate.
func TestOnlyCodeSigningCertificatesSign(t *testing.T) {
	AllowNoEKUSigners(false)
	ca := mint(t, "ca", nil, true, nil)
	for name, eku := range map[string][]x509.ExtKeyUsage{
		"serverAuth only": {x509.ExtKeyUsageServerAuth},
		"clientAuth only": {x509.ExtKeyUsageClientAuth},
		"no EKU":          nil,
	} {
		t.Run(name, func(t *testing.T) {
			f, s, c := signed(t, mint(t, name, ca, false, eku))
			if err := VerifyDetached(f, s, c, pool(ca)); err == nil {
				t.Fatalf("a %s certificate signed", name)
			}
		})
	}
}

// TestTheTransitionAdmitsNoEKUOnly: the flag exists for certificates issued
// before codeSigning was required. It must not open the door to a certificate
// that says it is for something else.
func TestTheTransitionAdmitsNoEKUOnly(t *testing.T) {
	AllowNoEKUSigners(true)
	t.Cleanup(func() { AllowNoEKUSigners(false) })
	ca := mint(t, "ca", nil, true, nil)

	f, s, c := signed(t, mint(t, "legacy", ca, false, nil))
	if err := VerifyDetached(f, s, c, pool(ca)); err != nil {
		t.Errorf("a no-EKU certificate was refused during the transition: %v", err)
	}
	f, s, c = signed(t, mint(t, "tls", ca, false, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}))
	if err := VerifyDetached(f, s, c, pool(ca)); err == nil {
		t.Error("the transition let a serverAuth certificate sign")
	}
}

// TestAClientAuthCANeverAuthorisesCode: a CA that issues device identities,
// restricted to clientAuth, must not be able to vouch for a signer, even one
// that claims codeSigning itself.
func TestAClientAuthCANeverAuthorisesCode(t *testing.T) {
	root := mint(t, "root", nil, true, nil)
	devices := mint(t, "device-ca", root, true, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})
	f, s, c := signed(t, mint(t, "claims-code", devices, false, codeSigning), devices)
	if err := VerifyDetached(f, s, c, pool(root)); err == nil {
		t.Fatal("a signer under a clientAuth-only CA verified")
	}
}

// TestIntermediatesInTheCertificateFileAreUsed: certificates after the leaf were
// ignored, so a chain through an intermediate verified only if the intermediate
// had been added to the trust bundle.
func TestIntermediatesInTheCertificateFileAreUsed(t *testing.T) {
	root := mint(t, "root", nil, true, nil)
	signing := mint(t, "signing-ca", root, true, codeSigning)
	f, s, c := signed(t, mint(t, "signer", signing, false, codeSigning), signing)
	if err := VerifyDetached(f, s, c, pool(root)); err != nil {
		t.Fatalf("a chain through an intermediate in the certificate file failed: %v", err)
	}
}

func TestTheRefusalSaysWhatToDo(t *testing.T) {
	AllowNoEKUSigners(false)
	ca := mint(t, "ca", nil, true, nil)
	f, s, c := signed(t, mint(t, "legacy", ca, false, nil))
	err := VerifyDetached(f, s, c, pool(ca))
	if err == nil || !strings.Contains(err.Error(), "codeSigning") {
		t.Fatalf("got %v, want a refusal naming codeSigning", err)
	}
}

func pemFile(t *testing.T, name string, certs ...*issuer) string {
	t.Helper()
	var b []byte
	for _, c := range certs {
		b = append(b, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.cert.Raw})...)
	}
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// reissue signs a new certificate for an existing key: another serial, the same
// authority.
func reissue(t *testing.T, i *issuer) *issuer {
	t.Helper()
	serial++
	tmpl := *i.cert
	tmpl.SerialNumber = big.NewInt(serial)
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &i.key.PublicKey, i.key)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	return &issuer{cert: c, key: i.key}
}

// TestTransportAndCodeMustNotShareACA: a CA that issues broker or device
// certificates and is also trusted for code lets anyone who can get a
// connection certificate get code accepted.
func TestTransportAndCodeMustNotShareACA(t *testing.T) {
	code := mint(t, "code-ca", nil, true, nil)
	transport := mint(t, "transport-ca", nil, true, nil)
	device := mint(t, "acme/pi-1", transport, false, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})
	bundle := pemFile(t, "trust.pem", code)

	if err := CheckTransportSeparation(bundle, pemFile(t, "broker-ca.pem", transport), pemFile(t, "client.pem", device, transport)); err != nil {
		t.Fatalf("separate CAs were refused: %v", err)
	}

	for name, file := range map[string]string{
		"the same CA as the broker CA":        pemFile(t, "broker-ca.pem", code),
		"the same key, reissued":              pemFile(t, "broker-ca.pem", reissue(t, code)),
		"inside the client certificate chain": pemFile(t, "client.pem", device, code),
	} {
		t.Run(name, func(t *testing.T) {
			if err := CheckTransportSeparation(bundle, file); err == nil {
				t.Fatal("a transport file sharing a key with the trust bundle was accepted")
			}
		})
	}

	if err := CheckTransportSeparation("", pemFile(t, "broker-ca.pem", code)); err != nil {
		t.Errorf("with no trust bundle there is nothing to separate: %v", err)
	}
}
