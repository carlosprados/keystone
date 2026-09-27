package enrol

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
)

// newKeyAndCSR generates a P-256 key that never leaves the device and a CSR
// for CN=<tenant>/<device>. The server ignores CSR extensions and issues its
// own profile; the URI SAN is included so the request reads as what it is.
func newKeyAndCSR(tenant, device string) (*ecdsa.PrivateKey, string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, "", err
	}
	uri, err := url.Parse(SubjectURI(tenant, device))
	if err != nil {
		return nil, "", err
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: SubjectCN(tenant, device)},
		URIs:    []*url.URL{uri},
	}, key)
	if err != nil {
		return nil, "", err
	}
	return key, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})), nil
}

func encodeKey(key *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), nil
}

// checkIssued refuses a response that is not a certificate for this key and
// this device, usable as a client identity, with a broker CA to trust.
//
// The server is trusted to issue, not to be right. Writing a certificate for
// another key would leave the device with a pair that cannot authenticate;
// writing one for another name would put it under someone else's topics.
func checkIssued(iss *Issued, key *ecdsa.PrivateKey, tenant, device string) (*x509.Certificate, error) {
	chain, err := parseCerts(iss.Certificate)
	if err != nil {
		return nil, fmt.Errorf("certificate: %w", err)
	}
	leaf := chain[0]
	pub, ok := leaf.PublicKey.(*ecdsa.PublicKey)
	if !ok || !pub.Equal(&key.PublicKey) {
		return nil, errors.New("the issued certificate is not for the key this device generated")
	}
	if want := SubjectCN(tenant, device); leaf.Subject.CommonName != want {
		return nil, fmt.Errorf("the issued certificate names %q, not %q", leaf.Subject.CommonName, want)
	}
	clientAuth := false
	for _, u := range leaf.ExtKeyUsage {
		if u == x509.ExtKeyUsageClientAuth {
			clientAuth = true
		}
	}
	if !clientAuth {
		return nil, errors.New("the issued certificate is not for clientAuth")
	}
	if len(chain) > 1 {
		if err := leaf.CheckSignatureFrom(chain[1]); err != nil {
			return nil, fmt.Errorf("the issued certificate is not signed by the CA sent with it: %w", err)
		}
	}
	if _, err := parseCerts(iss.BrokerCA); err != nil {
		return nil, fmt.Errorf("brokerCA: %w", err)
	}
	if iss.NotAfter.IsZero() || iss.RenewAfter.IsZero() || !iss.RenewAfter.Before(iss.NotAfter) {
		return nil, fmt.Errorf("notAfter %s and renewAfter %s are not a usable schedule", iss.NotAfter, iss.RenewAfter)
	}
	return leaf, nil
}

func parseCerts(p string) ([]*x509.Certificate, error) {
	var out []*x509.Certificate
	rest := []byte(p)
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil, errors.New("no PEM certificate found")
	}
	return out, nil
}
