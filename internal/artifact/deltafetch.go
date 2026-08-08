package artifact

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// FetchViaDelta reconstructs an artifact by patching the copy of a previous
// version that is already on this device, and returns the path of the
// resulting uncompressed tar. The caller unpacks it exactly as it would unpack
// a downloaded archive.
//
// artDir is the current version's artifact directory
// (runtime/artifacts/<name>/<version>). The base is looked for in its sibling
// directories — the other versions of the same component.
//
// Every failure is a normal outcome, not a fault: no base on disk (first
// install, or retention removed it), the server has no patch for that base
// (404), a patch that does not apply, or a result that does not match the
// digest. The caller falls back to downloading the whole artifact, which is
// why none of these return anything special.
//
// The patch transforms the *uncompressed* tar. Gzip's output is not
// reproducible across compressors, so a patched .tar.gz could never be
// verified against a published digest; the uncompressed tar, by contrast, is
// recovered byte for byte by decompressing the cached archive.
func FetchViaDelta(ctx context.Context, artDir, server, targetSHA, format string, cfg DownloadConfig) (string, error) {
	if format != "" && format != DeltaFormatBsdiffZstd {
		return "", fmt.Errorf("%w: %q", ErrUnsupportedDeltaFormat, format)
	}

	tmpDir := filepath.Join(artDir, "delta-tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return "", fmt.Errorf("create delta temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// The base is decompressed to disk and mapped, never read onto the heap:
	// bsdiff needs it addressable as one slice, and on a small device the
	// difference between "the kernel's page cache" and "the process's heap"
	// is the difference between an update that fits and one that does not.
	baseTar, releaseBase, basePath, err := findDeltaBase(artDir, tmpDir)
	if err != nil {
		return "", err
	}
	defer releaseBase()

	if limit := maxDeltaBaseBytes(); limit > 0 && int64(len(baseTar)) > limit {
		return "", fmt.Errorf("base is %d bytes, over the %d-byte delta limit (KEYSTONE_DELTA_MAX_BASE_BYTES)",
			len(baseTar), limit)
	}
	baseSHA := SHA256Bytes(baseTar)

	patchURL, err := DeltaURL(server, baseSHA, targetSHA)
	if err != nil {
		return "", err
	}

	// Patches are fetched with the same downloader as everything else, so a
	// flaky link resumes rather than restarting — the property that matters
	// most on the links a delta is worth having on.
	res, err := fetchPatch(ctx, tmpDir, patchURL, cfg)
	if err != nil {
		return "", fmt.Errorf("download patch: %w", err)
	}
	pf, err := os.Open(res.Path)
	if err != nil {
		return "", fmt.Errorf("open patch: %w", err)
	}
	raw, err := decodeZstd(pf)
	_ = pf.Close()
	if err != nil {
		return "", fmt.Errorf("decompress patch: %w", err)
	}
	patchSize := len(raw)

	out, err := applyRawPatch(baseTar, raw, targetSHA)
	if err != nil {
		return "", err
	}

	dst := filepath.Join(artDir, targetSHA+".tar")
	if err := os.WriteFile(dst, out, 0o644); err != nil {
		return "", fmt.Errorf("write patched artifact: %w", err)
	}
	log.Printf("[artifact] delta applied: base %s (%s) + %d B patch -> %d B",
		filepath.Base(basePath), baseSHA[:12], patchSize, len(out))
	return dst, nil
}

// How long to keep asking for a patch that is not there yet, and how often.
// Computing one is expensive — measured at ~10 s for a 34 MB artifact — so the
// window has to outlast a generation without stalling an apply for long.
// A var rather than a const so tests can exercise the retry without waiting
// out a real generation.
var (
	deltaRetryAttempts = 4
	deltaRetryInterval = 15 * time.Second
)

// fetchPatch downloads the patch, tolerating a "not there yet" answer.
//
// This exists because of how delta servers behave in practice: the first
// request for a (from, to) pair that has never been asked for finds nothing
// cached, answers 404, and *dispatches the generation in the background*.
// Taking that 404 at face value would mean a lone device never receives a
// patch at all — it would fall back to the full download every time, and only
// a second device asking later would benefit from the work the first one
// triggered. Verified against ota-updater: 404, then the same URL serves
// 1,034,559 bytes about ten seconds later.
//
// Any other failure is returned immediately: a wrong URL or an unreachable
// server does not become likelier by asking again.
func fetchPatch(ctx context.Context, dir, url string, cfg DownloadConfig) (*DownloadResult, error) {
	var lastErr error
	for attempt := 0; attempt < deltaRetryAttempts; attempt++ {
		if attempt > 0 {
			log.Printf("[artifact] patch not ready yet; retrying in %s (%d/%d)",
				deltaRetryInterval, attempt+1, deltaRetryAttempts)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(deltaRetryInterval):
			}
		}
		res, err := DownloadWithResume(ctx, dir, url, cfg)
		if err == nil {
			return res, nil
		}
		lastErr = err
		if !isNotFound(err) {
			return nil, err
		}
	}
	return nil, lastErr
}

// isNotFound recognises the downloader's 404. It matches on the status text
// because that is what the download path surfaces; a typed error would be
// better and is worth having if anything else ever needs to tell 404 from the
// other client errors.
func isNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "404")
}

// maxDeltaBaseBytes bounds the artifact size the delta path will attempt.
//
// Patching costs memory proportional to the artifact — the base is mapped
// rather than heap-allocated, but bsdiff still materialises the whole result —
// so on a small device there is a size past which downloading the artifact is
// cheaper than the RAM spike of reconstructing it. 0 disables the limit.
func maxDeltaBaseBytes() int64 {
	if v := os.Getenv("KEYSTONE_DELTA_MAX_BASE_BYTES"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			return n
		}
	}
	return defaultMaxDeltaBaseBytes
}

const defaultMaxDeltaBaseBytes = 256 << 20 // 256 MiB

// ErrNoDeltaBase means no previous version of this artifact is on disk, so
// there is nothing to patch. It is the expected outcome of a first install.
var ErrNoDeltaBase = errors.New("no local base version to patch from")

// findDeltaBase decompresses the newest cached archive belonging to another
// version of the same component and returns its uncompressed tar bytes.
//
// Only one candidate is tried. Trying every version on disk would multiply
// decompressions and server round trips to salvage a case — an operator
// jumping backwards several releases — that the full download already covers
// at a known cost.
// The returned bytes are only valid until release is called.
func findDeltaBase(artDir, tmpDir string) (tarBytes []byte, release func(), path string, err error) {
	parent := filepath.Dir(artDir)
	entries, err := os.ReadDir(parent)
	if err != nil {
		return nil, nil, "", ErrNoDeltaBase
	}
	current := filepath.Base(artDir)

	type candidate struct {
		path string
		mod  int64
	}
	var cands []candidate
	for _, e := range entries {
		if !e.IsDir() || e.Name() == current {
			continue
		}
		// The index records what was downloaded for that version, which
		// distinguishes the archive from its signature and certificate.
		idx, err := LoadIndex(filepath.Join(parent, e.Name()))
		if err != nil {
			continue
		}
		for _, ent := range idx.M {
			if !isGzippedTarName(ent.Path) {
				continue
			}
			fi, err := os.Stat(ent.Path)
			if err != nil {
				continue
			}
			cands = append(cands, candidate{path: ent.Path, mod: fi.ModTime().UnixNano()})
		}
	}
	if len(cands) == 0 {
		return nil, nil, "", ErrNoDeltaBase
	}
	// Newest first: the most recently installed version is the likeliest one
	// the delta server still holds a patch from.
	sort.Slice(cands, func(i, j int) bool { return cands[i].mod > cands[j].mod })

	f, err := os.Open(cands[0].path)
	if err != nil {
		return nil, nil, "", ErrNoDeltaBase
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, nil, "", fmt.Errorf("base %s is not gzip: %w", filepath.Base(cands[0].path), err)
	}
	defer gz.Close()

	// Straight from the compressed archive to a file, so the uncompressed base
	// is never held on the heap on its way to being mapped.
	basePath := filepath.Join(tmpDir, "base.tar")
	out, err := os.Create(basePath)
	if err != nil {
		return nil, nil, "", fmt.Errorf("create base file: %w", err)
	}
	limit := maxExtractBytes()
	n, err := io.Copy(out, io.LimitReader(gz, limit))
	closeErr := out.Close()
	if err != nil {
		return nil, nil, "", fmt.Errorf("decompress base: %w", err)
	}
	if closeErr != nil {
		return nil, nil, "", fmt.Errorf("write base: %w", closeErr)
	}
	if n >= limit {
		return nil, nil, "", fmt.Errorf("base expands beyond %d bytes (KEYSTONE_MAX_EXTRACT_BYTES)", limit)
	}

	data, release, err := mapFile(basePath)
	if err != nil {
		return nil, nil, "", err
	}
	return data, release, cands[0].path, nil
}

func isGzippedTarName(p string) bool {
	return strings.HasSuffix(p, ".tar.gz") || strings.HasSuffix(p, ".tgz")
}
