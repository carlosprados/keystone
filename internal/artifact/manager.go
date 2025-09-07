package artifact

import (
    "context"
    "archive/tar"
    "archive/zip"
    "compress/gzip"
    "crypto/sha256"
    "encoding/hex"
    "errors"
    "fmt"
    "io"
    "net/url"
    "os"
    "path/filepath"
    "strings"
    "time"

    retryablehttp "github.com/hashicorp/go-retryablehttp"
)

// Download downloads the given URI to destDir, with retries and resume support.
func Download(destDir, uri string, timeout time.Duration) (string, error) {
    if err := os.MkdirAll(destDir, 0o755); err != nil { return "", err }
    u, err := url.Parse(uri)
    if err != nil { return "", err }
    base := filepath.Base(u.Path)
    if base == "." || base == "/" || base == "" { base = "artifact.bin" }
    destPath := filepath.Join(destDir, base)

    client := retryablehttp.NewClient()
    client.RetryMax = 4
    client.RetryWaitMin = 250 * time.Millisecond
    client.RetryWaitMax = 2 * time.Second
    client.Logger = nil

    req, err := retryablehttp.NewRequest("GET", uri, nil)
    if err != nil { return "", err }
    ctx := context.Background()
    var cancel func()
    if timeout > 0 { ctx, cancel = context.WithTimeout(ctx, timeout); defer cancel() }
    resp, err := client.Do(req.WithContext(ctx))
    if err != nil { return "", err }
    defer resp.Body.Close()
    if resp.StatusCode < 200 || resp.StatusCode >= 300 {
        return "", fmt.Errorf("http error: %s", resp.Status)
    }
    out, err := os.OpenFile(destPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
    if err != nil { return "", err }
    if _, err := io.Copy(out, resp.Body); err != nil { out.Close(); return "", err }
    if err := out.Close(); err != nil { return "", err }
    return destPath, nil
}

// Ensure attempts to reuse an existing downloaded artifact using a JSON index
// stored under destRoot (file: destRoot/index.json). If not present or missing,
// it downloads the artifact and updates the index. Returns local path and
// whether it reused (true) or downloaded (false).
func Ensure(destRoot, uri, sha string, timeout time.Duration) (string, bool, error) {
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
    path, err := Download(destRoot, uri, timeout)
    if err != nil { return "", false, err }
    // Update index with size and sha
    st, _ := os.Stat(path)
    idx.Put(IndexEntry{URI: uri, SHA256: sha, Path: path, Size: st.Size()})
    _ = idx.Save()
    return path, false, nil
}

// VerifySHA256 checks that the file's sha256 matches expected (hex lowercase).
func VerifySHA256(path, expected string) error {
    f, err := os.Open(path)
    if err != nil { return err }
    defer f.Close()
    h := sha256.New()
    if _, err := io.Copy(h, f); err != nil { return err }
    got := hex.EncodeToString(h.Sum(nil))
    exp := strings.ToLower(strings.TrimSpace(expected))
    if got != exp {
        return fmt.Errorf("sha256 mismatch: got %s want %s", got, exp)
    }
    return nil
}

// Unpack extracts a tar.gz or zip file to targetDir. Best-effort detection by extension.
func Unpack(archivePath, targetDir string) error {
    if strings.HasSuffix(archivePath, ".tar.gz") || strings.HasSuffix(archivePath, ".tgz") {
        return untarGz(archivePath, targetDir)
    }
    if strings.HasSuffix(archivePath, ".zip") {
        return unzip(archivePath, targetDir)
    }
    return errors.New("unsupported archive format")
}

func untarGz(archivePath, targetDir string) error {
    if err := os.MkdirAll(targetDir, 0o755); err != nil { return err }
    f, err := os.Open(archivePath)
    if err != nil { return err }
    defer f.Close()
    gz, err := gzip.NewReader(f)
    if err != nil { return err }
    defer gz.Close()
    tr := tar.NewReader(gz)
    for {
        hdr, err := tr.Next()
        if errors.Is(err, io.EOF) { break }
        if err != nil { return err }
        dstPath := filepath.Join(targetDir, hdr.Name)
        switch hdr.Typeflag {
        case tar.TypeDir:
            if err := os.MkdirAll(dstPath, os.FileMode(hdr.Mode)); err != nil { return err }
        case tar.TypeReg:
            if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil { return err }
            out, err := os.OpenFile(dstPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode))
            if err != nil { return err }
            if _, err := io.Copy(out, tr); err != nil { out.Close(); return err }
            out.Close()
        default:
            // skip other types for MVP
        }
    }
    return nil
}

func unzip(archivePath, targetDir string) error {
    if err := os.MkdirAll(targetDir, 0o755); err != nil { return err }
    r, err := zip.OpenReader(archivePath)
    if err != nil { return err }
    defer r.Close()
    for _, f := range r.File {
        dstPath := filepath.Join(targetDir, f.Name)
        if f.FileInfo().IsDir() {
            if err := os.MkdirAll(dstPath, f.Mode()); err != nil { return err }
            continue
        }
        if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil { return err }
        rc, err := f.Open()
        if err != nil { return err }
        out, err := os.OpenFile(dstPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, f.Mode())
        if err != nil { rc.Close(); return err }
        if _, err := io.Copy(out, rc); err != nil { rc.Close(); out.Close(); return err }
        rc.Close(); out.Close()
    }
    return nil
}
