package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestSSHTransportCarriesARequest exercises the whole tunnel: the socketpair,
// the child process wired to it, and an HTTP exchange over the result. The ssh
// binary is replaced by a stand-in that implements -W and nothing else, so the
// test proves our plumbing rather than OpenSSH's.
func TestSSHTransportCarriesARequest(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "ssh")
	if out, err := exec.Command("go", "build", "-o", fake, "./testdata/fakessh").CombinedOutput(); err != nil {
		t.Skipf("cannot build the ssh stand-in: %v\n%s", err, out)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer s3cr3t" {
			t.Errorf("Authorization = %q, want the bearer token to survive the tunnel", got)
		}
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()

	tunnelled := &http.Client{Transport: sshTransport(sshTarget{Host: "edge-001"})}
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/healthz", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer s3cr3t")

	resp, err := tunnelled.Do(req)
	if err != nil {
		t.Fatalf("request over the SSH tunnel: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %s, want 200", resp.Status)
	}
	if strings.TrimSpace(string(body)) != `{"status":"ok"}` {
		t.Errorf("body = %q", body)
	}
}

// A tunnel that cannot be established must fail the request rather than hang or
// report a confusing success.
func TestSSHTransportFailsWhenSSHIsMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	tunnelled := &http.Client{Transport: sshTransport(sshTarget{Host: "edge-001"})}
	if _, err := tunnelled.Get("http://127.0.0.1:1/healthz"); err == nil {
		t.Fatal("expected an error when the ssh binary is not on PATH")
	}
}
