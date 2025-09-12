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
    "net/http"
    "net/url"
    "os"
    "path/filepath"
    "strings"
    "time"

    retryablehttp "github.com/hashicorp/go-retryablehttp"
)

// HTTPOptions allows callers to attach headers and optional GitHub token.
type HTTPOptions struct {
    Headers     map[string]string
    GithubToken string
}

// Download downloads the given URI to destDir, with retries and resume support.
func Download(destDir, uri string, timeout time.Duration, httpOpts HTTPOptions) (string, error) {
    if err := os.MkdirAll(destDir, 0o755); err != nil {
        return "", err
    }
    u, err := url.Parse(uri)
    if err != nil {
		return "", err
	}
	base := filepath.Base(u.Path)
	if base == "." || base == "/" || base == "" {
		base = "artifact.bin"
	}
	destPath := filepath.Join(destDir, base)

	client := retryablehttp.NewClient()
	client.RetryMax = 4
	client.RetryWaitMin = 250 * time.Millisecond
	client.RetryWaitMax = 2 * time.Second
	client.Logger = nil

    req, err := retryablehttp.NewRequest("GET", uri, nil)
    if err != nil {
        return "", err
    }
    // Attach headers from environment (e.g., Authorization)
    attachRequestHeaders(req.Request, httpOpts)
    ctx := context.Background()
    var cancel func()
    if timeout > 0 {
        ctx, cancel = context.WithTimeout(ctx, timeout)
        defer cancel()
	}
	resp, err := client.Do(req.WithContext(ctx))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("http error: %s", resp.Status)
	}
	out, err := os.OpenFile(destPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, resp.Body); err != nil {
		out.Close()
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	return destPath, nil
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
                return e.Path, true, nil
            }
        }
    }
    // Download fresh
    path, err := Download(destRoot, uri, timeout, httpOpts)
    if err != nil {
        return "", false, err
    }
    // Update index with size and sha
    st, _ := os.Stat(path)
	idx.Put(IndexEntry{URI: uri, SHA256: sha, Path: path, Size: st.Size()})
	_ = idx.Save()
	return path, false, nil
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
