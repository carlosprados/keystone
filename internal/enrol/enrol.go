package enrol

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"
)

// enrolAttempts bounds retries on 429 during enrolment. With the server's
// limit of 2/s and a burst of 20, this outlasts a whole plant enrolling at once.
const enrolAttempts = 30

// Options for Enrol.
type Options struct {
	BootstrapPath string
	Dir           string
	// Replace allows enrolling over an existing identity: for a device whose
	// renewal was refused, or whose certificate expired, with a new token.
	Replace bool
	// Now is the time certificates and the token's expiry are judged against.
	Now func() time.Time
}

// Enrol turns a bootstrap file into an identity in Dir.
//
// The token is spent by the server on success, so the bootstrap file is
// deleted afterwards: it no longer opens anything, and a copy of a secret is
// one more place for it to leak from.
func Enrol(ctx context.Context, o Options) (*Enrolment, error) {
	if o.Now == nil {
		o.Now = time.Now
	}
	b, err := os.ReadFile(o.BootstrapPath)
	if err != nil {
		return nil, err
	}
	if fi, err := os.Stat(o.BootstrapPath); err == nil && fi.Mode().Perm()&0o077 != 0 {
		log.Printf("[enrol] WARNING %s is readable by others (%v); it holds a secret and should be 0600", o.BootstrapPath, fi.Mode().Perm())
	}
	bs, err := ParseBootstrap(b)
	if err != nil {
		return nil, err
	}
	if now := o.Now(); !now.Before(bs.ExpiresAt) {
		return nil, fmt.Errorf("bootstrap token expired at %s (now %s); ask for a new one", bs.ExpiresAt.Format(time.RFC3339), now.UTC().Format(time.RFC3339))
	}

	store := Store{Dir: o.Dir}
	if _, err := store.Current(); err == nil && !o.Replace {
		return nil, fmt.Errorf("%s already holds an identity; it renews itself. To enrol again with a new token, pass --replace", o.Dir)
	} else if err != nil && !errors.Is(err, ErrNotEnrolled) {
		return nil, err
	}

	pin, _ := ParsePin(bs.CAPin)
	key, csr, err := newKeyAndCSR(bs.Tenant, bs.Device)
	if err != nil {
		return nil, err
	}
	c := newClient(bs.EnrolURL, pin, o.Now, nil)
	iss, err := c.post(ctx, "/enrol/v1", map[string]string{"token": bs.Token, "csr": csr}, enrolAttempts)
	if err != nil {
		if errors.Is(err, ErrRefused) {
			return nil, fmt.Errorf("the enrolment server refused the token (unknown, used or expired): %w", err)
		}
		return nil, fmt.Errorf("enrolment: %w", err)
	}
	if _, err := checkIssued(iss, key, bs.Tenant, bs.Device); err != nil {
		return nil, fmt.Errorf("enrolment: the server's answer was not written: %w", err)
	}

	keyPEM, err := encodeKey(key)
	if err != nil {
		return nil, err
	}
	meta := Enrolment{
		Version:    BootstrapVersion,
		EnrolURL:   bs.EnrolURL,
		CAPin:      bs.CAPin,
		Tenant:     bs.Tenant,
		Device:     bs.Device,
		NotAfter:   iss.NotAfter,
		RenewAfter: iss.RenewAfter,
	}
	if err := store.write(keyPEM, iss, meta); err != nil {
		return nil, fmt.Errorf("enrolment succeeded but the identity could not be written to %s: %w", o.Dir, err)
	}
	if err := os.Remove(o.BootstrapPath); err != nil {
		log.Printf("[enrol] WARNING the token is spent, but %s could not be removed: %v", o.BootstrapPath, err)
	}
	return &meta, nil
}
