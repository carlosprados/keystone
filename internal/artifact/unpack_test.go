package artifact

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func writeTarGz(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for name, body := range entries {
		hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeZip(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, body := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestUntarGzRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	arc := filepath.Join(dir, "evil.tar.gz")
	writeTarGz(t, arc, map[string]string{"../escape.txt": "pwned"})

	target := filepath.Join(dir, "out")
	if err := untarGz(arc, target); err == nil {
		t.Fatal("untarGz extracted a traversal entry, want error")
	}
	if _, err := os.Stat(filepath.Join(dir, "escape.txt")); err == nil {
		t.Fatal("traversal entry escaped the target directory")
	}
}

func TestUnzipRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	arc := filepath.Join(dir, "evil.zip")
	writeZip(t, arc, map[string]string{"../escape.txt": "pwned"})

	target := filepath.Join(dir, "out")
	if err := unzip(arc, target); err == nil {
		t.Fatal("unzip extracted a traversal entry, want error")
	}
	if _, err := os.Stat(filepath.Join(dir, "escape.txt")); err == nil {
		t.Fatal("traversal entry escaped the target directory")
	}
}

func TestUntarGzHonoursExtractBudget(t *testing.T) {
	t.Setenv("KEYSTONE_MAX_EXTRACT_BYTES", "8")
	dir := t.TempDir()
	arc := filepath.Join(dir, "big.tar.gz")
	writeTarGz(t, arc, map[string]string{"file.txt": "way more than eight bytes"})

	target := filepath.Join(dir, "out")
	if err := untarGz(arc, target); err == nil {
		t.Fatal("untarGz exceeded the extraction budget without error")
	}
}

func TestUnpackHappyPath(t *testing.T) {
	dir := t.TempDir()
	arc := filepath.Join(dir, "ok.tar.gz")
	writeTarGz(t, arc, map[string]string{"sub/hello.txt": "hi"})

	target := filepath.Join(dir, "out")
	if err := untarGz(arc, target); err != nil {
		t.Fatalf("untarGz failed on a benign archive: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(target, "sub", "hello.txt"))
	if err != nil {
		t.Fatalf("expected extracted file: %v", err)
	}
	if string(got) != "hi" {
		t.Fatalf("content = %q, want %q", got, "hi")
	}
}
