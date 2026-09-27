package enrol

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeServer implements the server side of the v1 contract, closely enough to
// exercise every answer the agent has to handle.
type fakeServer struct {
	t   *testing.T
	srv *httptest.Server

	tlsCA     *x509.Certificate // the CA a bootstrap pins
	tlsLeaf   *x509.Certificate
	deviceCA  *x509.Certificate
	deviceKey *ecdsa.PrivateKey
	brokerCA  string

	token    string
	mu       sync.Mutex
	spent    bool
	status   []int // forced status codes, consumed in order
	issueFor *ecdsa.PublicKey
	enrols   atomic.Int32
	renewals atomic.Int32
	lastKey  *ecdsa.PublicKey
}

const testToken = "kst1_0123456789abcdef0123456789abcdef_abc_defghijklmnopqrstuvwxyzABCDEFGHIJKLMNOP"

var serial atomic.Int64

func mintCA(t *testing.T, cn string, eku []x509.ExtKeyUsage) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(serial.Add(1)),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           eku,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	return c, key
}

func pemOf(certs ...*x509.Certificate) string {
	var out []byte
	for _, c := range certs {
		out = append(out, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})...)
	}
	return string(out)
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	f := &fakeServer{t: t, token: testToken}

	var tlsCAKey *ecdsa.PrivateKey
	f.tlsCA, tlsCAKey = mintCA(t, "enrol-ca", nil)
	leafKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial.Add(1)),
		Subject:      pkix.Name{CommonName: "enrol.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, leafTmpl, f.tlsCA, &leafKey.PublicKey, tlsCAKey)
	if err != nil {
		t.Fatal(err)
	}
	f.tlsLeaf, _ = x509.ParseCertificate(der)

	f.deviceCA, f.deviceKey = mintCA(t, "device-ca", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})
	brokerCA, _ := mintCA(t, "broker-ca", nil)
	f.brokerCA = pemOf(brokerCA)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /enrol/v1", f.handleEnrol)
	mux.HandleFunc("POST /enrol/v1/renew", f.handleRenew)
	f.srv = httptest.NewUnstartedServer(mux)
	f.srv.TLS = &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{f.tlsLeaf.Raw, f.tlsCA.Raw}, PrivateKey: leafKey}},
		ClientAuth:   tls.RequestClientCert,
	}
	f.srv.StartTLS()
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeServer) pin() string {
	sum := sha256.Sum256(f.tlsCA.Raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (f *fakeServer) forced() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.status) == 0 {
		return 0
	}
	s := f.status[0]
	f.status = f.status[1:]
	return s
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	if code == http.StatusTooManyRequests {
		w.Header().Set("Retry-After", "1")
	}
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func (f *fakeServer) handleEnrol(w http.ResponseWriter, r *http.Request) {
	f.enrols.Add(1)
	if s := f.forced(); s != 0 {
		writeErr(w, s, "forced")
		return
	}
	var req struct{ Token, CSR string }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad json")
		return
	}
	f.mu.Lock()
	ok := req.Token == f.token && !f.spent
	f.spent = f.spent || ok
	f.mu.Unlock()
	if !ok {
		writeErr(w, 403, "token refused")
		return
	}
	f.issue(w, req.CSR)
}

func (f *fakeServer) handleRenew(w http.ResponseWriter, r *http.Request) {
	f.renewals.Add(1)
	if len(r.TLS.PeerCertificates) == 0 {
		writeErr(w, 401, "client certificate required")
		return
	}
	pool := x509.NewCertPool()
	pool.AddCert(f.deviceCA)
	if _, err := r.TLS.PeerCertificates[0].Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		writeErr(w, 403, "renewal refused")
		return
	}
	if s := f.forced(); s != 0 {
		writeErr(w, s, "renewal refused")
		return
	}
	var req struct{ CSR string }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad json")
		return
	}
	f.issue(w, req.CSR)
}

func (f *fakeServer) issue(w http.ResponseWriter, csrPEM string) {
	block, _ := pem.Decode([]byte(csrPEM))
	if block == nil {
		writeErr(w, 400, "no csr")
		return
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil || csr.CheckSignature() != nil {
		writeErr(w, 400, "bad csr")
		return
	}
	pub := csr.PublicKey.(*ecdsa.PublicKey)
	f.mu.Lock()
	f.lastKey = pub
	if f.issueFor != nil {
		pub = f.issueFor
	}
	f.mu.Unlock()
	notAfter := time.Now().Add(30 * 24 * time.Hour).Truncate(time.Second)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial.Add(1)),
		Subject:      csr.Subject,
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, f.deviceCA, pub, f.deviceKey)
	if err != nil {
		writeErr(w, 500, "internal error")
		return
	}
	leaf, _ := x509.ParseCertificate(der)
	_ = json.NewEncoder(w).Encode(Issued{
		Certificate: pemOf(leaf, f.deviceCA),
		BrokerCA:    f.brokerCA,
		NotAfter:    notAfter,
		RenewAfter:  time.Now().Add(20 * 24 * time.Hour).Truncate(time.Second),
	})
}

// bootstrapFile writes a bootstrap for this server and returns its path.
func (f *fakeServer) bootstrapFile(t *testing.T) string {
	t.Helper()
	b, _ := json.Marshal(Bootstrap{
		Version:   1,
		EnrolURL:  f.srv.URL,
		Token:     f.token,
		Tenant:    "acme",
		Device:    "pi-1",
		CAPin:     f.pin(),
		ExpiresAt: time.Now().Add(time.Hour),
	})
	p := filepath.Join(t.TempDir(), "bootstrap.json")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}
