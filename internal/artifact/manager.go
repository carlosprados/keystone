package artifact

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// HTTPOptions allows callers to attach headers and optional GitHub token.
type HTTPOptions struct {
	Headers     map[string]string
	GithubToken string
}

// Download downloads the given URI to destDir, with retries and resume support.
// This is the main entry point for artifact downloads. It uses robust retry logic
// with exponential backoff, resume capability via HTTP Range headers, and proper
// timeout handling for unreliable network conditions.
func Download(destDir, uri string, timeout time.Duration, httpOpts HTTPOptions) (string, error) {
	cfg := DefaultDownloadConfig()
	cfg.HTTPOptions = httpOpts

	// Override timeout if specified
	if timeout > 0 {
		cfg.OverallTimeout = timeout
	}

	ctx := context.Background()
	result, err := DownloadWithResume(ctx, destDir, uri, cfg)
	if err != nil {
		return "", err
	}

	return result.Path, nil
}

// DownloadWithConfig downloads with a custom configuration.
// Use this for fine-grained control over timeouts, retries, and resume behavior.
func DownloadWithConfig(ctx context.Context, destDir, uri string, cfg DownloadConfig) (*DownloadResult, error) {
	return DownloadWithResume(ctx, destDir, uri, cfg)
}

// attachRequestHeaders adds headers from HTTPOptions only.
func attachRequestHeaders(r *http.Request, opts HTTPOptions) {
	// Always set a User-Agent
	if r.Header.Get("User-Agent") == "" {
		r.Header.Set("User-Agent", "keystone-agent")
	}
	// Headers from recipe options
	for k, v := range opts.Headers {
		if strings.TrimSpace(k) == "" || strings.TrimSpace(v) == "" {
			continue
		}
		r.Header.Set(k, v)
	}
	// GitHub token from recipe options (if no Authorization already set)
	host := r.URL.Hostname()
	if (host == "github.com" || host == "api.github.com") && r.Header.Get("Authorization") == "" {
		if opts.GithubToken != "" {
			r.Header.Set("Authorization", "Bearer "+opts.GithubToken)
		}
	}
}

// splitHeader/splitMulti removed: configuration now only via recipe TOML.

// Ensure attempts to reuse an existing downloaded artifact using a JSON index
// stored under destRoot (file: destRoot/index.json). If not present or missing,
// it downloads the artifact and updates the index. Returns local path and
// whether it reused (true) or downloaded (false).
func Ensure(destRoot, uri, sha string, timeout time.Duration, httpOpts HTTPOptions) (string, bool, error) {
	idx, _ := LoadIndex(destRoot)
	if e, ok := idx.Get(uri); ok {
		if _, err := os.Stat(e.Path); err == nil {
			// If SHA provided, verify before reuse
			if sha == "" || VerifySHA256(e.Path, sha) == nil {
				log.Printf("[artifact] using cached artifact: %s", filepath.Base(e.Path))
				return e.Path, true, nil
			}
			log.Printf("[artifact] cached artifact SHA mismatch, re-downloading")
		}
	}

	// Download fresh with robust retry and resume
	cfg := DefaultDownloadConfig()
	cfg.HTTPOptions = httpOpts
	if timeout > 0 {
		cfg.OverallTimeout = timeout
	}

	ctx := context.Background()
	result, err := DownloadWithResume(ctx, destRoot, uri, cfg)
	if err != nil {
		return "", false, fmt.Errorf("download %s: %w", uri, err)
	}

	// Verify SHA if provided
	if sha != "" {
		if err := VerifySHA256(result.Path, sha); err != nil {
			// Remove corrupted file
			_ = os.Remove(result.Path)
			return "", false, fmt.Errorf("SHA256 verification failed for %s: %w", uri, err)
		}
		log.Printf("[artifact] SHA256 verified: %s", filepath.Base(result.Path))
	}

	// Update index
	idx.Put(IndexEntry{URI: uri, SHA256: sha, Path: result.Path, Size: result.Size})
	if err := idx.Save(); err != nil {
		log.Printf("[artifact] warning: failed to save index: %v", err)
	}

	return result.Path, false, nil
}

// EnsureWithConfig is like Ensure but with custom download configuration.
func EnsureWithConfig(ctx context.Context, destRoot, uri, sha string, cfg DownloadConfig) (string, bool, error) {
	idx, _ := LoadIndex(destRoot)
	if e, ok := idx.Get(uri); ok {
		if _, err := os.Stat(e.Path); err == nil {
			if sha == "" || VerifySHA256(e.Path, sha) == nil {
				log.Printf("[artifact] using cached artifact: %s", filepath.Base(e.Path))
				return e.Path, true, nil
			}
		}
	}

	result, err := DownloadWithResume(ctx, destRoot, uri, cfg)
	if err != nil {
		return "", false, fmt.Errorf("download %s: %w", uri, err)
	}

	if sha != "" {
		if err := VerifySHA256(result.Path, sha); err != nil {
			_ = os.Remove(result.Path)
			return "", false, fmt.Errorf("SHA256 verification failed: %w", err)
		}
	}

	idx.Put(IndexEntry{URI: uri, SHA256: sha, Path: result.Path, Size: result.Size})
	_ = idx.Save()

	return result.Path, false, nil
}

// VerifySHA256 checks that the file's sha256 matches expected (hex lowercase).
func VerifySHA256(path, expected string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	exp := strings.ToLower(strings.TrimSpace(expected))
	if got != exp {
		return fmt.Errorf("sha256 mismatch: got %s want %s", got, exp)
	}
	return nil
}

// Unpack extracts a tar.gz or zip file to targetDir. Best-effort detection by extension.
func Unpack(archivePath, targetDir string) error {
	// Fast-path by extension
	if strings.HasSuffix(archivePath, ".tar.gz") || strings.HasSuffix(archivePath, ".tgz") {
		return untarGz(archivePath, targetDir)
	}
	if strings.HasSuffix(archivePath, ".zip") {
		return unzip(archivePath, targetDir)
	}
	// Fallback: detect by magic header (GitHub artifacts may download as '.../zip')
	if isZipFile(archivePath) {
		return unzip(archivePath, targetDir)
	}
	if isGzipFile(archivePath) {
		return untarGz(archivePath, targetDir)
	}
	return errors.New("unsupported archive format")
}

func isZipFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(f, hdr); err != nil {
		return false
	}
	// ZIP local file header signature: 0x50 0x4B 0x03 0x04
	return hdr[0] == 0x50 && hdr[1] == 0x4B && hdr[2] == 0x03 && hdr[3] == 0x04
}

func isGzipFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(f, hdr); err != nil {
		return false
	}
	// GZIP ID1 ID2: 0x1F 0x8B
	return hdr[0] == 0x1F && hdr[1] == 0x8B
}

func untarGz(archivePath, targetDir string) error {
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return err
	}
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		dstPath := filepath.Join(targetDir, hdr.Name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dstPath, os.FileMode(hdr.Mode)); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(dstPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			out.Close()
		default:
			// skip other types for MVP
		}
	}
	return nil
}

func unzip(archivePath, targetDir string) error {
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return err
	}
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range r.File {
		dstPath := filepath.Join(targetDir, f.Name)
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(dstPath, f.Mode()); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(dstPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, f.Mode())
		if err != nil {
			rc.Close()
			return err
		}
		if _, err := io.Copy(out, rc); err != nil {
			rc.Close()
			out.Close()
			return err
		}
		rc.Close()
		out.Close()
	}
	return nil
}
