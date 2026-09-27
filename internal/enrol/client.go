package enrol

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// maxBody bounds both what is sent and what is read back: the contract caps
// requests at 64 KiB, and a response is a certificate chain and a CA bundle.
const maxBody = 64 << 10

// Errors that decide what a caller does next.
var (
	// ErrRefused: the server will not do this for these credentials, and asking
	// again will not change that (403). For renewal it means an operator has to
	// enrol the device again.
	ErrRefused = errors.New("refused")
	// ErrBadRequest: the server rejected the request itself (400).
	ErrBadRequest = errors.New("bad request")
	// ErrUnauthenticated: no client certificate reached the server (401).
	ErrUnauthenticated = errors.New("no client certificate presented")
)

// Issued is the body of a successful enrolment or renewal.
type Issued struct {
	Certificate string    `json:"certificate"`
	BrokerCA    string    `json:"brokerCA"`
	NotAfter    time.Time `json:"notAfter"`
	RenewAfter  time.Time `json:"renewAfter"`
}

// client talks to one enrolment server, trusting only the pinned CA.
type client struct {
	base string
	http *http.Client
	// sleep is replaceable so tests do not wait out Retry-After.
	sleep func(context.Context, time.Duration) error
}

// newClient builds a client whose TLS accepts the server only through the
// pinned CA. cert, when set, is presented for mTLS (renewal).
func newClient(base string, pin [32]byte, now func() time.Time, cert *tls.Certificate) *client {
	cfg := pinnedTLS(pin, now)
	if cert != nil {
		cfg.Certificates = []tls.Certificate{*cert}
	}
	return &client{
		base: strings.TrimRight(base, "/"),
		http: &http.Client{
			Timeout:   60 * time.Second,
			Transport: &http.Transport{TLSClientConfig: cfg, ForceAttemptHTTP2: true},
		},
		sleep: sleepCtx,
	}
}

// pinnedTLS verifies the server against the pinned CA and nothing else.
//
// The pin is of the CA the chain ends at, never of the server's own leaf, so the
// server can rotate its certificate without re-issuing every bootstrap. The
// server always sends that CA in its chain; a chain without it is refused, and
// so is one that contains it but does not verify up to it.
//
// The system roots play no part: a certificate from a public CA for the same
// host name must not be enough. Time comes from the agent's clock, so a device
// with no RTC booting at 1970 does not find every certificate not yet valid.
func pinnedTLS(pin [32]byte, now func() time.Time) *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		// Verification is done in VerifyConnection, against the pin.
		InsecureSkipVerify: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			return verifyPinned(cs.PeerCertificates, cs.ServerName, pin, now())
		},
	}
}

func verifyPinned(chain []*x509.Certificate, host string, pin [32]byte, at time.Time) error {
	if len(chain) == 0 {
		return errors.New("enrolment server presented no certificate")
	}
	var anchor *x509.Certificate
	for _, c := range chain[1:] {
		if sha256.Sum256(c.Raw) == pin {
			anchor = c
			break
		}
	}
	// A pin of the server's own leaf finds nothing above: the leaf would be
	// its own root, and x509 accepts a certificate that is in the root pool.
	if anchor == nil || !anchor.IsCA || !anchor.BasicConstraintsValid {
		return errors.New("enrolment server's chain does not contain the pinned CA")
	}
	roots := x509.NewCertPool()
	roots.AddCert(anchor)
	inter := x509.NewCertPool()
	for _, c := range chain[1:] {
		inter.AddCert(c)
	}
	_, err := chain[0].Verify(x509.VerifyOptions{
		DNSName:       host,
		Roots:         roots,
		Intermediates: inter,
		CurrentTime:   at,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	if err != nil {
		return fmt.Errorf("enrolment server certificate does not verify to the pinned CA: %w", err)
	}
	return nil
}

// post sends body to path and decodes a 200 into Issued. A 429 is not a
// refusal: it waits Retry-After plus jitter and tries again, up to attempts.
func (c *client) post(ctx context.Context, path string, body any, attempts int) (*Issued, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	if len(payload) > maxBody {
		return nil, fmt.Errorf("request is %d bytes, over the %d the server accepts", len(payload), maxBody)
	}
	for attempt := 1; ; attempt++ {
		issued, retryAfter, err := c.once(ctx, path, payload)
		if retryAfter <= 0 || attempt >= attempts {
			return issued, err
		}
		if serr := c.sleep(ctx, retryAfter+jitter()); serr != nil {
			return nil, serr
		}
	}
}

// once makes one request. A positive duration means "rate limited, try again
// after this".
func (c *client) once(ctx context.Context, path string, payload []byte) (*Issued, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(payload))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, 0, err
	}
	if len(b) > maxBody {
		return nil, 0, fmt.Errorf("%s: response over %d bytes", path, maxBody)
	}

	switch resp.StatusCode {
	case http.StatusOK:
		var out Issued
		if err := json.Unmarshal(b, &out); err != nil {
			return nil, 0, fmt.Errorf("%s: unreadable response: %w", path, err)
		}
		return &out, 0, nil
	case http.StatusTooManyRequests:
		wait := retryAfter(resp.Header.Get("Retry-After"))
		return nil, wait, fmt.Errorf("%s: rate limited (retry after %s)", path, wait)
	case http.StatusBadRequest:
		return nil, 0, fmt.Errorf("%w: %s", ErrBadRequest, serverError(b))
	case http.StatusUnauthorized:
		return nil, 0, fmt.Errorf("%w: %s", ErrUnauthenticated, serverError(b))
	case http.StatusForbidden:
		return nil, 0, fmt.Errorf("%w: %s", ErrRefused, serverError(b))
	default:
		return nil, 0, fmt.Errorf("%s: server answered %d: %s", path, resp.StatusCode, serverError(b))
	}
}

func serverError(b []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(b, &e) == nil && e.Error != "" {
		return e.Error
	}
	return strings.TrimSpace(string(b))
}

// retryAfter reads a Retry-After in seconds. A missing or unreadable value
// still means "wait": one second, not zero.
func retryAfter(v string) time.Duration {
	if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	return time.Second
}

// jitter spreads retries from a plant behind one NAT, which shares one
// rate limit, so they do not all come back in the same instant.
func jitter() time.Duration { return time.Duration(rand.Int64N(int64(time.Second))) }

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
