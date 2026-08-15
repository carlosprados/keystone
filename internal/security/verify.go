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
	"math/big"
	"os"
)

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
func VerifyDetached(filePath, sigPath, leafCertPath string, roots *x509.CertPool) error {
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

	// Verify chain
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots}); err != nil {
		return fmt.Errorf("certificate verify failed: %w", err)
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
