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
	"strconv"
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
// defaultMaxExtractBytes caps the total uncompressed size written by a single
// archive extraction to defeat decompression bombs (a tiny archive that
// expands to fill the disk). Override with KEYSTONE_MAX_EXTRACT_BYTES.
const defaultMaxExtractBytes int64 = 2 << 30 // 2 GiB

func maxExtractBytes() int64 {
	if v := os.Getenv("KEYSTONE_MAX_EXTRACT_BYTES"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return defaultMaxExtractBytes
}

// safeJoin joins targetDir and an archive-supplied entry name, then verifies
// the cleaned result stays within targetDir. This defeats "zip slip" path
// traversal: an entry named "../../etc/cron.d/x" (or an absolute path) is
// rejected instead of escaping the extraction directory.
func safeJoin(targetDir, name string) (string, error) {
	dst := filepath.Join(targetDir, name)
	cleanDir := filepath.Clean(targetDir)
	rel, err := filepath.Rel(cleanDir, dst)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("illegal archive entry %q: escapes extraction directory", name)
	}
	return dst, nil
}

// safeMode masks archive-supplied permission bits down to a safe subset,
// stripping setuid/setgid/sticky and any group/other write bit so a malicious
// archive cannot drop a setuid binary or world-writable file.
func safeMode(m os.FileMode) os.FileMode { return m & 0o755 }

// copyWithBudget copies src into dst while decrementing *budget by the number
// of bytes written. It errors if the copy would exceed the remaining budget,
// bounding total extracted size against decompression bombs.
func copyWithBudget(dst io.Writer, src io.Reader, budget *int64) error {
	if *budget < 0 {
		return errors.New("archive exceeds maximum extraction size")
	}
	// Read at most budget+1 bytes; if we get budget+1, the limit was exceeded.
	n, err := io.Copy(dst, io.LimitReader(src, *budget+1))
	*budget -= n
	if err != nil {
		return err
	}
	if *budget < 0 {
		return errors.New("archive exceeds maximum extraction size")
	}
	return nil
}

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
	budget := maxExtractBytes()
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		dstPath, err := safeJoin(targetDir, hdr.Name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dstPath, safeMode(os.FileMode(hdr.Mode))); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(dstPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, safeMode(os.FileMode(hdr.Mode)))
			if err != nil {
				return err
			}
			if err := copyWithBudget(out, tr, &budget); err != nil {
				out.Close()
				return err
			}
			out.Close()
		default:
			// skip other types (symlinks, devices, etc.) for safety
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
	budget := maxExtractBytes()
	for _, f := range r.File {
		dstPath, err := safeJoin(targetDir, f.Name)
		if err != nil {
			return err
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(dstPath, safeMode(f.Mode())); err != nil {
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
		out, err := os.OpenFile(dstPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, safeMode(f.Mode()))
		if err != nil {
			rc.Close()
			return err
		}
		if err := copyWithBudget(out, rc, &budget); err != nil {
			rc.Close()
			out.Close()
			return err
		}
		rc.Close()
		out.Close()
	}
	return nil
}
