package enrol

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"time"
)

// Renewal pacing. The re-check bound keeps a clock that jumps (NTP arriving
// on a device with no RTC) from leaving a stale timer running for weeks.
const (
	recheckEvery   = time.Hour
	backoffMin     = time.Minute
	backoffMax     = time.Hour
	renewAttempts  = 10
	rejectedPause  = time.Hour
	renewalRefused = "renewal refused"
)

// Renewer keeps the identity in a Store renewed.
type Renewer struct {
	Store Store
	// Now is the agent's clock: the later of the system clock and evidence
	// that time has passed, so renewal is not postponed by a clock set back.
	Now func() time.Time
	// OnRenewed runs after a new identity is current, to put it into use.
	OnRenewed func()

	client func(e *Enrolment, cert *tls.Certificate) (*client, error)
	sleep  func(context.Context, time.Duration) error
}

// Run renews at renewAfter until ctx ends, or until renewing is no longer
// possible: the server refused it, or the certificate has expired. Both need an
// operator, and both are said in the log as such.
func (r *Renewer) Run(ctx context.Context) {
	if r.sleep == nil {
		r.sleep = sleepCtx
	}
	backoff := backoffMin
	for {
		e, err := r.Store.Current()
		if err != nil {
			log.Printf("[enrol] ERROR cannot read the identity to renew it: %v", err)
			return
		}
		now := r.Now()
		if !now.Before(e.NotAfter) {
			log.Printf("[enrol] ERROR the device certificate expired at %s. It cannot renew itself any more: enrol it again with a new bootstrap token (keystone enrol --replace)", e.NotAfter.Format(time.RFC3339))
			return
		}
		if wait := e.RenewAfter.Sub(now); wait > 0 {
			if r.sleep(ctx, min(wait, recheckEvery)) != nil {
				return
			}
			continue
		}

		err = r.renewOnce(ctx, e)
		switch {
		case err == nil:
			backoff = backoffMin
			if r.OnRenewed != nil {
				r.OnRenewed()
			}
			continue
		case ctx.Err() != nil:
			return
		case errors.Is(err, ErrRefused):
			log.Printf("[enrol] ERROR %s: %v. The server will not renew this device (superseded, revoked or unknown). Renewal has stopped; an operator must enrol it again (keystone enrol --replace). The current certificate works until %s", renewalRefused, err, e.NotAfter.Format(time.RFC3339))
			return
		case errors.Is(err, ErrBadRequest), errors.Is(err, ErrUnauthenticated):
			// The right to renew is kept; retrying in a tight loop would not
			// change the answer, so wait long.
			log.Printf("[enrol] WARNING renewal rejected: %v; retrying in %s", err, rejectedPause)
			if r.sleep(ctx, rejectedPause) != nil {
				return
			}
		default:
			log.Printf("[enrol] WARNING renewal failed: %v; retrying in %s", err, backoff)
			if r.sleep(ctx, backoff) != nil {
				return
			}
			backoff = min(backoff*2, backoffMax)
		}
	}
}

// renewOnce asks for a certificate for a NEW key, presenting the current one.
// Until the server answers 200 the old key and certificate stay in force: at no
// point are there two keys in play, and a failure leaves the device as it was.
func (r *Renewer) renewOnce(ctx context.Context, e *Enrolment) error {
	cert, err := tls.LoadX509KeyPair(r.Store.CertPath(), r.Store.KeyPath())
	if err != nil {
		return fmt.Errorf("load current identity: %w", err)
	}
	newC := r.client
	if newC == nil {
		newC = r.defaultClient
	}
	c, err := newC(e, &cert)
	if err != nil {
		return err
	}
	key, csr, err := newKeyAndCSR(e.Tenant, e.Device)
	if err != nil {
		return err
	}
	iss, err := c.post(ctx, "/enrol/v1/renew", map[string]string{"csr": csr}, renewAttempts)
	if err != nil {
		return err
	}
	if _, err := checkIssued(iss, key, e.Tenant, e.Device); err != nil {
		return fmt.Errorf("renewal answer not used: %w", err)
	}
	keyPEM, err := encodeKey(key)
	if err != nil {
		return err
	}
	next := *e
	next.NotAfter, next.RenewAfter = iss.NotAfter, iss.RenewAfter
	if err := r.Store.write(keyPEM, iss, next); err != nil {
		return fmt.Errorf("renewed, but the new identity could not be written: %w", err)
	}
	log.Printf("[enrol] device certificate renewed; valid until %s, next renewal after %s", next.NotAfter.Format(time.RFC3339), next.RenewAfter.Format(time.RFC3339))
	return nil
}

func (r *Renewer) defaultClient(e *Enrolment, cert *tls.Certificate) (*client, error) {
	pin, err := ParsePin(e.CAPin)
	if err != nil {
		return nil, err
	}
	return newClient(e.EnrolURL, pin, r.Now, cert), nil
}
