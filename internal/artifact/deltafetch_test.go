package artifact

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// tarWith builds an uncompressed tar holding one file, which is enough to tell
// "the patched archive unpacks to the new contents" from "it does not".
func tarWith(t *testing.T, name, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{
		Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatalf("tar header: %v", err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatalf("tar body: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	return buf.Bytes()
}

func gzipOf(t *testing.T, b []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	zw := gzip.NewWriter(&out)
	if _, err := zw.Write(b); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return out.Bytes()
}

// seedPreviousVersion writes a cached archive for an earlier version of the
// same component, in the shape the agent leaves behind: the archive plus the
// index entry that records what it was downloaded from.
func seedPreviousVersion(t *testing.T, parent, version string, archive []byte) string {
	t.Helper()
	dir := filepath.Join(parent, version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	p := filepath.Join(dir, "app-"+version+".tar.gz")
	if err := os.WriteFile(p, archive, 0o644); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	idx, err := LoadIndex(dir)
	if err != nil {
		t.Fatalf("load index: %v", err)
	}
	idx.Put(IndexEntry{URI: "https://example.com/app-" + version + ".tar.gz", Path: p, Size: int64(len(archive))})
	if err := idx.Save(); err != nil {
		t.Fatalf("save index: %v", err)
	}
	return p
}

func TestFetchViaDeltaEndToEnd(t *testing.T) {
	root := t.TempDir()
	baseTar := tarWith(t, "bin/app", "version one, the contents that are already installed\n")
	newTar := tarWith(t, "bin/app", "version two, with different contents entirely\n")

	seedPreviousVersion(t, root, "1.0.0", gzipOf(t, baseTar))
	curDir := filepath.Join(root, "1.1.0")
	if err := os.MkdirAll(curDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	patch := makePatch(t, baseTar, newTar)
	wantPath := "/delta/" + SHA256Bytes(baseTar) + "/" + SHA256Bytes(newTar)

	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.URL.Path != wantPath {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(patch)
	}))
	defer srv.Close()

	out, err := FetchViaDelta(context.Background(), curDir, srv.URL,
		SHA256Bytes(newTar), "", DefaultDownloadConfig())
	if err != nil {
		t.Fatalf("FetchViaDelta: %v (server saw %q, wanted %q)", err, gotPath, wantPath)
	}

	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if !bytes.Equal(got, newTar) {
		t.Fatal("patched artifact does not match the target tar")
	}

	// The result must be unpackable by the normal path, which is the whole
	// point of patching the tar rather than a file inside it.
	work := t.TempDir()
	if err := Unpack(out, work); err != nil {
		t.Fatalf("Unpack of the patched artifact: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(work, "bin", "app"))
	if err != nil {
		t.Fatalf("unpacked file missing: %v", err)
	}
	if !strings.Contains(string(body), "version two") {
		t.Errorf("unpacked contents are stale: %q", body)
	}

	// No temp state should survive a successful fetch.
	if _, err := os.Stat(filepath.Join(curDir, "delta-tmp")); !os.IsNotExist(err) {
		t.Error("delta-tmp directory was left behind")
	}
}

// TestFetchViaDeltaWaitsForGeneration pins the behaviour a real server forced.
// The first request for a patch that has never been asked for answers 404 and
// starts computing it in the background. Believing that 404 would mean a lone
// device downloads the whole artifact forever, since only a *second* asker
// would ever find the result cached.
func TestFetchViaDeltaWaitsForGeneration(t *testing.T) {
	oldAttempts, oldInterval := deltaRetryAttempts, deltaRetryInterval
	deltaRetryInterval = 10 * time.Millisecond
	defer func() { deltaRetryAttempts, deltaRetryInterval = oldAttempts, oldInterval }()

	root := t.TempDir()
	baseTar := tarWith(t, "bin/app", "installed contents\n")
	newTar := tarWith(t, "bin/app", "the release being rolled out\n")
	seedPreviousVersion(t, root, "1.0.0", gzipOf(t, baseTar))
	curDir := filepath.Join(root, "1.1.0")
	if err := os.MkdirAll(curDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	patch := makePatch(t, baseTar, newTar)
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 { // "not cached yet; generating"
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(patch)
	}))
	defer srv.Close()

	out, err := FetchViaDelta(context.Background(), curDir, srv.URL,
		SHA256Bytes(newTar), "", DefaultDownloadConfig())
	if err != nil {
		t.Fatalf("FetchViaDelta gave up after %d attempts: %v", calls, err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if !bytes.Equal(got, newTar) {
		t.Error("patched artifact does not match the target")
	}
	if calls != 3 {
		t.Errorf("server saw %d requests, want 3 (two 404s then the patch)", calls)
	}
}

// A failure that is not "not yet" must not be retried: asking a wrong URL
// again wastes an update window on a link that is expensive by definition.
func TestFetchViaDeltaDoesNotRetryOtherErrors(t *testing.T) {
	root := t.TempDir()
	baseTar := tarWith(t, "bin/app", "installed\n")
	seedPreviousVersion(t, root, "1.0.0", gzipOf(t, baseTar))
	curDir := filepath.Join(root, "1.1.0")
	if err := os.MkdirAll(curDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer srv.Close()

	if _, err := FetchViaDelta(context.Background(), curDir, srv.URL,
		SHA256Bytes(tarWith(t, "bin/app", "new\n")), "", DefaultDownloadConfig()); err == nil {
		t.Fatal("a 403 produced no error")
	}
	if calls != 1 {
		t.Errorf("server saw %d requests, want 1: a 403 must not be retried", calls)
	}
}

func TestFetchViaDeltaNoBase(t *testing.T) {
	root := t.TempDir()
	curDir := filepath.Join(root, "1.0.0")
	if err := os.MkdirAll(curDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	_, err := FetchViaDelta(context.Background(), curDir, "https://ota.example.com",
		SHA256Bytes([]byte("anything")), "", DefaultDownloadConfig())
	if !errors.Is(err, ErrNoDeltaBase) {
		t.Fatalf("err = %v, want ErrNoDeltaBase", err)
	}
}

// TestFetchViaDeltaServerHasNoPatch covers the ordinary case of a server that
// holds no patch from the base this device happens to have. It must surface as
// an error the caller can fall back on, not as a partial install.
func TestFetchViaDeltaServerHasNoPatch(t *testing.T) {
	root := t.TempDir()
	baseTar := tarWith(t, "bin/app", "installed contents\n")
	seedPreviousVersion(t, root, "1.0.0", gzipOf(t, baseTar))
	curDir := filepath.Join(root, "1.1.0")
	if err := os.MkdirAll(curDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	cfg := DefaultDownloadConfig()
	cfg.MaxRetries = 0
	if _, err := FetchViaDelta(context.Background(), curDir, srv.URL,
		SHA256Bytes(tarWith(t, "bin/app", "new\n")), "", cfg); err == nil {
		t.Fatal("a 404 from the delta server produced no error")
	}
	if _, err := os.Stat(filepath.Join(curDir, "delta-tmp")); !os.IsNotExist(err) {
		t.Error("delta-tmp directory was left behind after a failure")
	}
}

// TestFetchViaDeltaRejectsTamperedPatch is the security case: a server that
// returns a patch producing bytes other than the digest the signed recipe
// declared must not install anything.
func TestFetchViaDeltaRejectsTamperedPatch(t *testing.T) {
	root := t.TempDir()
	baseTar := tarWith(t, "bin/app", "installed contents\n")
	evilTar := tarWith(t, "bin/app", "attacker-chosen contents\n")
	goodTar := tarWith(t, "bin/app", "the release the operator asked for\n")

	seedPreviousVersion(t, root, "1.0.0", gzipOf(t, baseTar))
	curDir := filepath.Join(root, "1.1.0")
	if err := os.MkdirAll(curDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// The server answers the request for the good digest with a patch that
	// reconstructs something else.
	evilPatch := makePatch(t, baseTar, evilTar)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(evilPatch)
	}))
	defer srv.Close()

	out, err := FetchViaDelta(context.Background(), curDir, srv.URL,
		SHA256Bytes(goodTar), "", DefaultDownloadConfig())
	if !errors.Is(err, ErrDeltaDigestMismatch) {
		t.Fatalf("err = %v, want ErrDeltaDigestMismatch", err)
	}
	if out != "" {
		t.Errorf("a path was returned for a rejected patch: %q", out)
	}
	entries, _ := os.ReadDir(curDir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tar") {
			t.Errorf("attacker bytes were written to disk: %s", e.Name())
		}
	}
}
