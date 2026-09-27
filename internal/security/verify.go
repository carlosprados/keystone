package security

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"os"
	"sync/atomic"
	"time"
)

// allowNoEKUSigners admits signing certificates that carry no extended key
// usage at all. It exists only for the transition: certificates issued before
// codeSigning was required have no EKU, and refusing them at once would stop
// every device that still holds one. Off unless an operator turns it on.
var allowNoEKUSigners atomic.Bool

// AllowNoEKUSigners sets the transition policy for signing certificates with no
// extended key usage. Call it once at startup.
func AllowNoEKUSigners(allow bool) { allowNoEKUSigners.Store(allow) }

// CheckSignerEKU reports whether cert may sign what Keystone verifies: it must
// list codeSigning. A certificate with no EKU at all passes only while the
// transition policy allows it, and says so in the log every time.
//
// Why explicit: x509 treats a certificate with no EKU as good for any purpose,
// and the verifier used to ask for none, so x509 checked ServerAuth. Any no-EKU
// or serverAuth certificate from the trust bundle — a broker's TLS certificate,
// for one — could sign a recipe, while a correctly made codeSigning-only
// certificate was rejected.
func CheckSignerEKU(cert *x509.Certificate) error {
	for _, u := range cert.ExtKeyUsage {
		if u == x509.ExtKeyUsageCodeSigning {
			return nil
		}
	}
	if len(cert.ExtKeyUsage) == 0 && len(cert.UnknownExtKeyUsage) == 0 {
		if allowNoEKUSigners.Load() {
			log.Printf("[security] WARNING accepting signer %q with no extended key usage (--allow-no-eku-signers). Reissue it with codeSigning: this allowance is temporary", cert.Subject.CommonName)
			return nil
		}
		return fmt.Errorf("signing certificate %q has no extended key usage; it must be issued for codeSigning (during a transition, --allow-no-eku-signers admits it)", cert.Subject.CommonName)
	}
	return fmt.Errorf("signing certificate %q is not issued for codeSigning", cert.Subject.CommonName)
}

// LoadTrustBundle loads a PEM bundle of trusted roots into a CertPool.
func LoadTrustBundle(pemPath string) (*x509.CertPool, error) {
	if pemPath == "" {
		return nil, errors.New("empty trust bundle path")
	}
	b, err := os.ReadFile(pemPath)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(b) {
		return nil, errors.New("failed to parse trust bundle")
	}
	return pool, nil
}

// VerifyDetached verifies the signature of a file using a provided leaf certificate and trust bundle.
// The signature is expected to be over the SHA-256 digest of the file content.
// Signature file may be raw bytes or base64-encoded; both are supported.
//
// Certificate validity is judged against the system clock. On a device that may
// have no reliable clock, use VerifyDetachedAt with a time the caller can
// defend — see internal/clock.
func VerifyDetached(filePath, sigPath, leafCertPath string, roots *x509.CertPool) error {
	return VerifyDetachedAt(filePath, sigPath, leafCertPath, roots, time.Time{})
}

// VerifyDetachedAt is VerifyDetached with an explicit time for the certificate
// validity check. The zero time means "use the system clock", which is what
// x509 does with an unset CurrentTime.
//
// This exists because a gateway with no RTC boots at 1970 and would reject every
// valid certificate as not yet valid. Passing the time in — rather than reading
// the clock here — also means the decision about what time to believe lives in
// one place instead of being made implicitly at each call site.
func VerifyDetachedAt(filePath, sigPath, leafCertPath string, roots *x509.CertPool, now time.Time) error {
	if roots == nil {
		return errors.New("nil trust roots")
	}

	// Read file and compute digest
	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	digest := h.Sum(nil)

	// Load signature
	sig, err := os.ReadFile(sigPath)
	if err != nil {
		return err
	}
	// Try base64 decode if looks like base64
	if isLikelyBase64(sig) {
		if dec, err := base64.StdEncoding.DecodeString(string(sig)); err == nil {
			sig = dec
		}
	}

	// Load certificate
	certBytes, err := os.ReadFile(leafCertPath)
	if err != nil {
		return err
	}
	certs, err := x509.ParseCertificates(certBytes)
	if err != nil {
		// maybe PEM
		certs2, err2 := parsePEMCerts(certBytes)
		if err2 != nil {
			return err
		}
		certs = certs2
	}
	if len(certs) == 0 {
		return errors.New("no certificate found")
	}
	leaf := certs[0]

	// Certificates after the leaf are its intermediates. They were ignored, so
	// a chain through an intermediate CA verified only if that intermediate had
	// been put in the trust bundle itself.
	intermediates := x509.NewCertPool()
	for _, c := range certs[1:] {
		intermediates.AddCert(c)
	}

	// Verify the chain for code signing, which also holds every CA in the chain
	// to it: an intermediate restricted to clientAuth cannot authorise code.
	// CurrentTime zero means x509 uses the system clock.
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:         roots,
		Intermediates: intermediates,
		CurrentTime:   now,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
	}); err != nil {
		return fmt.Errorf("certificate verify failed: %w", err)
	}
	if err := CheckSignerEKU(leaf); err != nil {
		return err
	}

	// Verify signature according to key type
	switch pub := leaf.PublicKey.(type) {
	case *rsa.PublicKey:
		if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest, sig); err != nil {
			return fmt.Errorf("rsa verify failed: %w", err)
		}
	case *ecdsa.PublicKey:
		var esig struct{ R, S *big.Int }
		if _, err := asn1.Unmarshal(sig, &esig); err != nil {
			return fmt.Errorf("ecdsa sig parse: %w", err)
		}
		if esig.R == nil || esig.S == nil {
			return errors.New("invalid ecdsa signature")
		}
		if !ecdsa.Verify(pub, digest, esig.R, esig.S) {
			return errors.New("ecdsa verify failed")
		}
	case ed25519.PublicKey:
		// The signed message is the 32-byte SHA-256 digest, NOT the file, and
		// this is deliberately *not* Ed25519ph: Ed25519 hashes whatever it is
		// given with SHA-512 internally, so it hashes the digest. Signing the
		// digest keeps one rule for every algorithm here — "sign the SHA-256 of
		// the file" — and lets a signer stream a large artifact once.
		//
		// Signer and verifier have to agree on this exactly or nothing
		// validates, which is why it is stated here and in docs/security.md
		// rather than left to be inferred from the code.
		if !ed25519.Verify(pub, digest, sig) {
			return errors.New("ed25519 verify failed")
		}
	default:
		return fmt.Errorf("unsupported public key type %T (supported: RSA, ECDSA, Ed25519)", pub)
	}
	return nil
}

// Helpers

func isLikelyBase64(b []byte) bool {
	for _, c := range b {
		if c == '\n' || c == '\r' || c == '=' || c == '+' || c == '/' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			continue
		}
		return false
	}
	return true
}

func parsePEMCerts(b []byte) ([]*x509.Certificate, error) {
	var out []*x509.Certificate
	for {
		var block *pem.Block
		block, b = pem.Decode(b)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err == nil {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no certs in PEM")
	}
	return out, nil
}

// CheckTransportSeparation refuses a trust bundle that shares a key with the
// certificates used for transport: the MQTT or NATS CA file, or a client
// certificate chain.
//
// The trust bundle decides what code a device runs. A CA that issues transport
// identities — broker certificates, device client certificates — is operated
// for a different purpose and usually by different people, and if it is also
// trusted for code, whoever can get a connection certificate issued can get a
// recipe accepted. Requiring the codeSigning EKU narrows that; separation closes it.
// Compared by public key, so a CA reissued under another serial still counts.
//
// Empty paths are skipped. A trust bundle path that is empty means signing is
// not configured, and there is nothing to keep apart.
func CheckTransportSeparation(trustBundlePath string, transportFiles ...string) error {
	if trustBundlePath == "" {
		return nil
	}
	codeKeys, err := keysIn(trustBundlePath)
	if err != nil {
		return fmt.Errorf("read trust bundle %s: %w", trustBundlePath, err)
	}
	for _, path := range transportFiles {
		if path == "" {
			continue
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		certs, err := parsePEMCerts(b)
		if err != nil {
			// A key file or anything else without certificates: nothing to compare.
			continue
		}
		for _, c := range certs {
			if subject, shared := codeKeys[spkiHash(c)]; shared {
				return fmt.Errorf("%s contains %q, whose key is also in the trust bundle %s as %q: "+
					"a certificate authority used for transport must never be trusted to authorise code; use separate CAs",
					path, c.Subject.String(), trustBundlePath, subject)
			}
		}
	}
	return nil
}

func keysIn(path string) (map[[32]byte]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	certs, err := parsePEMCerts(b)
	if err != nil {
		return nil, err
	}
	keys := make(map[[32]byte]string, len(certs))
	for _, c := range certs {
		keys[spkiHash(c)] = c.Subject.String()
	}
	return keys, nil
}

func spkiHash(c *x509.Certificate) [32]byte { return sha256.Sum256(c.RawSubjectPublicKeyInfo) }
