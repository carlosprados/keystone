// Package signing produces the detached signatures Keystone verifies.
//
// It is the counterpart of internal/security, and the split is deliberate:
// **the agent verifies, it never signs**. A gateway in a customer's plant is
// the most exposed thing in the system, and one that carries signing machinery
// hands whoever takes it a head start on forging updates for the whole fleet.
// Only internal/cli imports this package; TestAgentDoesNotLinkSigning enforces
// that, because an invariant that is only written down stops being true.
//
// The signature scheme is the one VerifyDetached expects, for every algorithm:
// sign the 32-byte SHA-256 digest of the file. For Ed25519 that means the
// digest is the message and the scheme is NOT Ed25519ph — see the comment in
// internal/security/verify.go.
package signing

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
)

// Backend resolves the key material a signature needs.
//
// It is expressed in terms of crypto.Signer rather than key bytes so the same
// commands can later sign against PKCS#11 or a cloud KMS, both of which
// implement that interface. A production signing key should not live in a file;
// discovering that after threading []byte through the tool means rewriting it.
type Backend interface {
	// Signer returns the private key handle.
	Signer() (crypto.Signer, error)
	// Certificate returns the leaf certificate that will verify the signature,
	// so a caller can check the key and the certificate agree before publishing
	// something no agent will accept.
	Certificate() (*x509.Certificate, error)
}

// FileBackend signs with a PEM private key and PEM certificate on disk.
type FileBackend struct {
	KeyPath  string
	CertPath string
}

// Signer loads the private key. PKCS#8, PKCS#1 ("RSA PRIVATE KEY") and SEC1
// ("EC PRIVATE KEY") are all accepted, because openssl emits different ones
// depending on how it was asked and the person signing should not have to care.
func (f FileBackend) Signer() (crypto.Signer, error) {
	if f.KeyPath == "" {
		return nil, errors.New("no signing key given (--key)")
	}
	raw, err := os.ReadFile(f.KeyPath)
	if err != nil {
		return nil, fmt.Errorf("read signing key: %w", err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("%s is not PEM: expected a private key block", f.KeyPath)
	}

	var key any
	switch block.Type {
	case "PRIVATE KEY":
		key, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	case "RSA PRIVATE KEY":
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	case "EC PRIVATE KEY":
		key, err = x509.ParseECPrivateKey(block.Bytes)
	default:
		return nil, fmt.Errorf("unsupported PEM block %q in %s", block.Type, f.KeyPath)
	}
	if err != nil {
		return nil, fmt.Errorf("parse signing key: %w", err)
	}

	signer, ok := key.(crypto.Signer)
	if !ok {
		return nil, fmt.Errorf("key type %T cannot sign", key)
	}
	switch signer.(type) {
	case *rsa.PrivateKey, *ecdsa.PrivateKey, ed25519.PrivateKey:
		return signer, nil
	default:
		return nil, fmt.Errorf("unsupported key type %T (supported: RSA, ECDSA, Ed25519)", key)
	}
}

// Certificate loads the leaf certificate, when one was given.
func (f FileBackend) Certificate() (*x509.Certificate, error) {
	if f.CertPath == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(f.CertPath)
	if err != nil {
		return nil, fmt.Errorf("read certificate: %w", err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("%s is not PEM: expected a certificate block", f.CertPath)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse certificate: %w", err)
	}
	return cert, nil
}

// DigestFile returns the SHA-256 of a file: the message every signature here is
// made over.
func DigestFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}

// SignDigest signs an already-computed SHA-256 digest.
//
// Ed25519 takes the digest as its message and must be handed crypto.Hash(0);
// passing crypto.SHA256 would select Ed25519ph, which produces a signature the
// agent rejects. RSA and ECDSA take the digest with crypto.SHA256, which is
// what rsa.VerifyPKCS1v15 and ecdsa.Verify check on the other side.
func SignDigest(signer crypto.Signer, digest []byte) ([]byte, error) {
	if len(digest) != sha256.Size {
		return nil, fmt.Errorf("digest is %d bytes, want %d", len(digest), sha256.Size)
	}
	opts := crypto.SignerOpts(crypto.SHA256)
	if _, isEd := signer.Public().(ed25519.PublicKey); isEd {
		opts = crypto.Hash(0)
	}
	return signer.Sign(rand.Reader, digest, opts)
}

// SignFile signs the file at path and returns the raw signature bytes.
//
// When the backend also carries a certificate, the signature is verified
// against it before returning: a mismatched key and certificate produce a
// signature that every agent rejects, and finding that out at publication time
// is much cheaper than finding it out on a fleet.
func SignFile(b Backend, path string) ([]byte, error) {
	signer, err := b.Signer()
	if err != nil {
		return nil, err
	}
	digest, err := DigestFile(path)
	if err != nil {
		return nil, err
	}
	sig, err := SignDigest(signer, digest)
	if err != nil {
		return nil, fmt.Errorf("sign %s: %w", path, err)
	}

	cert, err := b.Certificate()
	if err != nil {
		return nil, err
	}
	if cert != nil {
		if err := VerifyDigest(cert, digest, sig); err != nil {
			return nil, fmt.Errorf("the signature does not verify against %T in the certificate: %w", cert.PublicKey, err)
		}
	}
	return sig, nil
}

// VerifyDigest checks a signature against a certificate's public key, with no
// chain building. It exists so signing can self-check; verifying for real —
// chain, trust bundle and all — is internal/security's job.
func VerifyDigest(cert *x509.Certificate, digest, sig []byte) error {
	switch pub := cert.PublicKey.(type) {
	case *rsa.PublicKey:
		return rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest, sig)
	case *ecdsa.PublicKey:
		if !ecdsa.VerifyASN1(pub, digest, sig) {
			return errors.New("ecdsa signature does not verify")
		}
		return nil
	case ed25519.PublicKey:
		if !ed25519.Verify(pub, digest, sig) {
			return errors.New("ed25519 signature does not verify")
		}
		return nil
	default:
		return fmt.Errorf("unsupported public key type %T", pub)
	}
}

// WriteSignature writes sig to path with 0644: a detached signature is public,
// and it is published next to the file it signs.
func WriteSignature(path string, sig []byte) error {
	return os.WriteFile(path, sig, 0o644)
}
