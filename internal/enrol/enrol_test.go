package enrol

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBootstrapParsing(t *testing.T) {
	valid := Bootstrap{
		Version: 1, EnrolURL: "https://enrol.example:8443", Token: testToken,
		Tenant: "acme", Device: "pi-1", CAPin: "sha256:" + strings.Repeat("ab", 32),
		ExpiresAt: time.Now().Add(time.Hour),
	}
	encode := func(b Bootstrap) []byte { out, _ := json.Marshal(b); return out }

	if _, err := ParseBootstrap(encode(valid)); err != nil {
		t.Fatalf("valid bootstrap refused: %v", err)
	}
	for name, mutate := range map[string]func(*Bootstrap){
		"unknown version": func(b *Bootstrap) { b.Version = 2 },
		"plain http":      func(b *Bootstrap) { b.EnrolURL = "http://enrol.example" },
		"short token":     func(b *Bootstrap) { b.Token = "kst1_abc_def" },
		"wrong prefix":    func(b *Bootstrap) { b.Token = strings.Replace(testToken, "kst1", "kst2", 1) },
		"pin of wrong length": func(b *Bootstrap) {
			b.CAPin = "sha256:abcd"
		},
		"tenant with a slash": func(b *Bootstrap) { b.Tenant = "a/b" },
		"device with a colon": func(b *Bootstrap) { b.Device = "pi:1" },
		"no expiry":           func(b *Bootstrap) { b.ExpiresAt = time.Time{} },
	} {
		b := valid
		mutate(&b)
		if _, err := ParseBootstrap(encode(b)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	withExtra := strings.Replace(string(encode(valid)), `{`, `{"extra":1,`, 1)
	if _, err := ParseBootstrap([]byte(withExtra)); err == nil {
		t.Error("an unknown field was accepted")
	}
}

// TestTokenSecretMaySpellUnderscores: the secret is base64url, so only the
// first two underscores separate fields. testToken has one in its secret.
func TestTokenSecretMaySpellUnderscores(t *testing.T) {
	if !strings.Contains(strings.SplitN(testToken, "_", 3)[2], "_") {
		t.Fatal("test token does not exercise the case")
	}
	if err := ValidateToken(testToken); err != nil {
		t.Fatal(err)
	}
}

func TestPinnedTLS(t *testing.T) {
	f := newFakeServer(t)
	caPin, _ := ParsePin(f.pin())
	leafPin := sha256.Sum256(f.tlsLeaf.Raw)
	var other [32]byte

	get := func(pin [32]byte) error {
		c := newClient(f.srv.URL, pin, time.Now, nil)
		resp, err := c.http.Get(f.srv.URL + "/")
		if err == nil {
			resp.Body.Close()
		}
		return err
	}
	if err := get(caPin); err != nil {
		t.Errorf("pinned CA refused: %v", err)
	}
	// The pin is of the CA, never the leaf: pinning the leaf would make the
	// leaf its own root, and must not verify.
	if err := get(leafPin); err == nil {
		t.Error("a pin of the server's leaf was accepted")
	}
	if err := get(other); err == nil {
		t.Error("a chain without the pinned CA was accepted")
	}
	// The pinned CA is present and correct, but time says the chain is not
	// valid: the clock the agent passes is what decides.
	c := newClient(f.srv.URL, caPin, func() time.Time { return time.Now().Add(2 * 365 * 24 * time.Hour) }, nil)
	if _, err := c.http.Get(f.srv.URL + "/"); err == nil {
		t.Error("an expired server certificate was accepted")
	}
}

func TestEnrolWritesTheIdentity(t *testing.T) {
	f := newFakeServer(t)
	bs := f.bootstrapFile(t)
	dir := filepath.Join(t.TempDir(), "enrol")

	e, err := Enrol(context.Background(), Options{BootstrapPath: bs, Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if e.Tenant != "acme" || e.Device != "pi-1" {
		t.Errorf("enrolment names %s/%s", e.Tenant, e.Device)
	}
	store := Store{Dir: dir}
	if _, err := tls.LoadX509KeyPair(store.CertPath(), store.KeyPath()); err != nil {
		t.Fatalf("written key and certificate are not a pair: %v", err)
	}
	fi, err := os.Stat(store.KeyPath())
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("key mode %v, want 0600", fi.Mode().Perm())
	}
	b, _ := os.ReadFile(store.BrokerCAPath())
	if string(b) != f.brokerCA {
		t.Error("broker CA not written as received")
	}
	if _, err := os.Stat(bs); !os.IsNotExist(err) {
		t.Error("the bootstrap file, whose token is spent, was left behind")
	}
	if _, err := Enrol(context.Background(), Options{BootstrapPath: f.bootstrapFile(t), Dir: dir}); err == nil {
		t.Error("enrolled over an existing identity without --replace")
	}
}

func TestARefusedTokenWritesNothing(t *testing.T) {
	f := newFakeServer(t)
	f.status = []int{403}
	bs := f.bootstrapFile(t)
	dir := filepath.Join(t.TempDir(), "enrol")

	_, err := Enrol(context.Background(), Options{BootstrapPath: bs, Dir: dir})
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("got %v, want a refusal", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("a refused enrolment left a directory behind")
	}
	if _, err := os.Stat(bs); err != nil {
		t.Error("the bootstrap file was removed although enrolment failed")
	}
}

func TestRateLimitingIsWaitedOut(t *testing.T) {
	f := newFakeServer(t)
	f.status = []int{429}
	start := time.Now()
	if _, err := Enrol(context.Background(), Options{BootstrapPath: f.bootstrapFile(t), Dir: filepath.Join(t.TempDir(), "e")}); err != nil {
		t.Fatalf("a 429 was treated as a refusal: %v", err)
	}
	if f.enrols.Load() != 2 {
		t.Errorf("%d requests, want 2", f.enrols.Load())
	}
	if time.Since(start) < time.Second {
		t.Error("Retry-After was not waited out")
	}
}

func TestACertificateForAnotherKeyIsNotWritten(t *testing.T) {
	f := newFakeServer(t)
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	f.issueFor = &other.PublicKey
	dir := filepath.Join(t.TempDir(), "enrol")

	if _, err := Enrol(context.Background(), Options{BootstrapPath: f.bootstrapFile(t), Dir: dir}); err == nil {
		t.Fatal("a certificate for another key was accepted")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("the answer was written although it was refused")
	}
}

func TestAnExpiredBootstrapIsNotSent(t *testing.T) {
	f := newFakeServer(t)
	later := func() time.Time { return time.Now().Add(2 * time.Hour) }
	if _, err := Enrol(context.Background(), Options{BootstrapPath: f.bootstrapFile(t), Dir: t.TempDir(), Now: later}); err == nil {
		t.Fatal("an expired token was used")
	}
	if f.enrols.Load() != 0 {
		t.Error("an expired token was sent to the server")
	}
}

func enrolled(t *testing.T) (*fakeServer, Store) {
	t.Helper()
	f := newFakeServer(t)
	dir := filepath.Join(t.TempDir(), "enrol")
	if _, err := Enrol(context.Background(), Options{BootstrapPath: f.bootstrapFile(t), Dir: dir}); err != nil {
		t.Fatal(err)
	}
	return f, Store{Dir: dir}
}

func currentKey(t *testing.T, s Store) *ecdsa.PublicKey {
	t.Helper()
	pair, err := tls.LoadX509KeyPair(s.CertPath(), s.KeyPath())
	if err != nil {
		t.Fatal(err)
	}
	return pair.PrivateKey.(*ecdsa.PrivateKey).Public().(*ecdsa.PublicKey)
}

func TestRenewalReplacesKeyAndCertificateTogether(t *testing.T) {
	f, store := enrolled(t)
	before := currentKey(t, store)
	e, _ := store.Current()

	r := &Renewer{Store: store, Now: time.Now}
	if err := r.renewOnce(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	after := currentKey(t, store)
	if after.Equal(before) {
		t.Error("renewal kept the old key")
	}
	if !f.lastKey.Equal(after) {
		t.Error("the key in force is not the one the server certified")
	}
	gens, _ := filepath.Glob(filepath.Join(store.Dir, genPrefix+"*"))
	if len(gens) != 2 {
		t.Errorf("%d generations kept, want the current one and the one before", len(gens))
	}
}

// TestARefusedRenewalStopsAndKeepsTheIdentity: a 403 means an operator must
// act. Retrying would not change it, and the old certificate still works.
func TestARefusedRenewalStopsAndKeepsTheIdentity(t *testing.T) {
	f, store := enrolled(t)
	before := currentKey(t, store)
	f.status = []int{403}

	r := &Renewer{Store: store, Now: func() time.Time { return time.Now().Add(25 * 24 * time.Hour) }}
	done := make(chan struct{})
	go func() { r.Run(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("renewal kept going after a 403")
	}
	if f.renewals.Load() != 1 {
		t.Errorf("%d renewal requests after a refusal, want 1", f.renewals.Load())
	}
	if !currentKey(t, store).Equal(before) {
		t.Error("a refused renewal replaced the key")
	}
}

func TestRenewalWaitsForRenewAfter(t *testing.T) {
	f, store := enrolled(t)
	ctx, cancel := context.WithCancel(context.Background())
	var slept time.Duration
	r := &Renewer{Store: store, Now: time.Now, sleep: func(_ context.Context, d time.Duration) error {
		slept = d
		cancel()
		return context.Canceled
	}}
	r.Run(ctx)
	if f.renewals.Load() != 0 {
		t.Error("renewed before renewAfter")
	}
	if slept != recheckEvery {
		t.Errorf("slept %s, want the %s re-check bound", slept, recheckEvery)
	}
}
