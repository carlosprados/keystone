// Package enrol gives a device its own transport identity: a key generated on
// the device, a certificate for it from an enrolment server, and the CA to
// trust the broker with. It also keeps that certificate renewed.
//
// The wire contract (version 1):
//
//   - A bootstrap file, delivered out of band, names the server, pins its CA and
//     carries a one-time token.
//   - POST {enrolUrl}/enrol/v1 {"token","csr"} returns the certificate chain,
//     the broker CA bundle, notAfter and renewAfter.
//   - POST {enrolUrl}/enrol/v1/renew {"csr"}, authenticated by the current
//     certificate over mTLS, returns the same body for a new key.
//
// What this package never does: put the broker CA anywhere near the trust
// bundle that decides what code runs, or keep two keys in play. The new key
// replaces the old one only after the server has issued a certificate for it.
package enrol

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	mqttadapter "github.com/carlosprados/keystone/internal/adapter/mqtt"
)

// BootstrapVersion is the only bootstrap format this agent understands.
const BootstrapVersion = 1

// Bootstrap is the file an operator puts on a device to enrol it.
type Bootstrap struct {
	Version   int       `json:"version"`
	EnrolURL  string    `json:"enrolUrl"`
	Token     string    `json:"token"`
	Tenant    string    `json:"tenant"`
	Device    string    `json:"device"`
	CAPin     string    `json:"caPin"`
	ExpiresAt time.Time `json:"expiresAt"`
}

var (
	tokenID     = regexp.MustCompile(`^[0-9a-f]{32}$`)
	tokenSecret = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
	pinHex      = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// ParseBootstrap reads and validates a bootstrap file. Anything it does not
// recognise is refused rather than guessed at: a version it does not know may
// mean something it would get wrong.
func ParseBootstrap(b []byte) (*Bootstrap, error) {
	var bs Bootstrap
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&bs); err != nil {
		return nil, fmt.Errorf("bootstrap: %w", err)
	}
	if bs.Version != BootstrapVersion {
		return nil, fmt.Errorf("bootstrap: version %d is not supported (this agent understands %d)", bs.Version, BootstrapVersion)
	}
	if err := validateEnrolURL(bs.EnrolURL); err != nil {
		return nil, fmt.Errorf("bootstrap: %w", err)
	}
	if err := ValidateToken(bs.Token); err != nil {
		return nil, fmt.Errorf("bootstrap: %w", err)
	}
	if err := mqttadapter.ValidateTenant(bs.Tenant); err != nil {
		return nil, fmt.Errorf("bootstrap: %w", err)
	}
	if err := validateDevice(bs.Device); err != nil {
		return nil, fmt.Errorf("bootstrap: %w", err)
	}
	if _, err := ParsePin(bs.CAPin); err != nil {
		return nil, fmt.Errorf("bootstrap: %w", err)
	}
	if bs.ExpiresAt.IsZero() {
		return nil, fmt.Errorf("bootstrap: expiresAt is required")
	}
	return &bs, nil
}

// ValidateToken checks the shape kst1_<32 hex>_<43 base64url>. The secret is
// base64url, whose alphabet includes "_", so only the first two underscores
// separate fields.
func ValidateToken(t string) error {
	parts := strings.SplitN(t, "_", 3)
	if len(parts) != 3 || parts[0] != "kst1" || !tokenID.MatchString(parts[1]) || !tokenSecret.MatchString(parts[2]) {
		return fmt.Errorf("token is not of the form kst1_<32 hex>_<43 base64url>")
	}
	return nil
}

// ParsePin decodes "sha256:<64 hex>", the SHA-256 of the DER of the CA the
// server's TLS chain ends at.
func ParsePin(p string) ([32]byte, error) {
	var out [32]byte
	h, ok := strings.CutPrefix(p, "sha256:")
	if !ok || !pinHex.MatchString(h) {
		return out, fmt.Errorf("caPin %q is not of the form sha256:<64 lowercase hex>", p)
	}
	b, _ := hex.DecodeString(h)
	copy(out[:], b)
	return out, nil
}

func validateEnrolURL(s string) error {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return fmt.Errorf("enrolUrl %q is not an absolute URL", s)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("enrolUrl %q must be https", s)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("enrolUrl %q must not carry a query or fragment", s)
	}
	return nil
}

// validateDevice applies the MQTT device ID rules, which exclude "/", ":" and
// spaces: the name is also part of CN=<tenant>/<device> and the URI SAN
// keystone:device:<tenant>:<device>, where they would make it ambiguous.
func validateDevice(d string) error { return mqttadapter.ValidateDeviceID(d) }

// SubjectCN is the certificate common name for a device.
func SubjectCN(tenant, device string) string { return tenant + "/" + device }

// SubjectURI is the URI SAN for a device.
func SubjectURI(tenant, device string) string { return "keystone:device:" + tenant + ":" + device }
