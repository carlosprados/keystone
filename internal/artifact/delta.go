package artifact

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/gabstv/go-bsdiff/pkg/bspatch"
	"github.com/klauspost/compress/zstd"
)

// DeltaFormatBsdiffZstd is the default patch encoding: a bsdiff patch
// compressed with zstd. It is what github.com/carlosprados/ota-updater
// produces, and an empty format field in a recipe means this.
const DeltaFormatBsdiffZstd = "bsdiff+zstd"

// ErrUnsupportedDeltaFormat is returned for a patch encoding this build does
// not implement. It is deliberately a normal error and not a fatal one: the
// caller falls back to downloading the whole artifact.
//
// This exists so that a delta server which changes encoding — bsdiff is
// unmaintained upstream, and xdelta3 is the named successor — degrades to a
// slow-but-correct download with a log line that says exactly why, instead of
// feeding bytes to the wrong decoder and failing later as a digest mismatch
// that names nothing.
var ErrUnsupportedDeltaFormat = errors.New("unsupported delta format")

// ErrDeltaDigestMismatch is returned when a patch applies cleanly but produces
// something other than the digest the recipe declared. It means the patch, the
// base, or the declared digest is wrong; all three are the caller's cue to
// fetch the whole artifact instead.
var ErrDeltaDigestMismatch = errors.New("patched artifact does not match the declared digest")

// DeltaURL builds the patch location from the two digests the agent already
// knows. There is no manifest exchange and no handshake: the base is on disk,
// its digest is computed locally, and the target digest comes from the signed
// recipe, so the URL is fully determined.
//
//	{server}/delta/{fromSHA}/{toSHA}
func DeltaURL(server, fromSHA, toSHA string) (string, error) {
	if server == "" {
		return "", errors.New("delta server is empty")
	}
	if !isSHA256Hex(fromSHA) {
		return "", fmt.Errorf("base digest %q is not a sha256 hex digest", fromSHA)
	}
	if !isSHA256Hex(toSHA) {
		return "", fmt.Errorf("target digest %q is not a sha256 hex digest", toSHA)
	}
	u, err := url.Parse(server)
	if err != nil {
		return "", fmt.Errorf("parse delta server %q: %w", server, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("delta server %q must be an absolute URL", server)
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "/delta/" + fromSHA + "/" + toSHA
	return u.String(), nil
}

// ApplyDelta reconstructs an artifact by applying patch to base and verifies
// the result against wantSHA, which the caller must have taken from a trusted
// source — in Keystone that is the signed recipe.
//
// Verification is not optional and not the caller's job to remember: an
// unverified patch is arbitrary attacker-chosen bytes, so this function
// refuses to return a result it has not checked.
func ApplyDelta(format string, base, patch []byte, wantSHA string) ([]byte, error) {
	if format != "" && format != DeltaFormatBsdiffZstd {
		return nil, fmt.Errorf("%w: %q (this build implements %q)",
			ErrUnsupportedDeltaFormat, format, DeltaFormatBsdiffZstd)
	}
	if !isSHA256Hex(wantSHA) {
		return nil, fmt.Errorf("declared digest %q is not a sha256 hex digest", wantSHA)
	}

	raw, err := decodeZstd(bytes.NewReader(patch))
	if err != nil {
		return nil, fmt.Errorf("decompress patch: %w", err)
	}
	return applyRawPatch(base, raw, wantSHA)
}

// applyRawPatch is the half that both the self-contained ApplyDelta and the
// streaming fetch path share, so the digest check cannot be skipped by taking
// a different route to it.
func applyRawPatch(base, raw []byte, wantSHA string) ([]byte, error) {
	out, err := bspatch.Bytes(base, raw)
	if err != nil {
		return nil, fmt.Errorf("apply patch: %w", err)
	}
	sum := sha256.Sum256(out)
	if got := hex.EncodeToString(sum[:]); got != wantSHA {
		return nil, fmt.Errorf("%w: got %s, want %s", ErrDeltaDigestMismatch, got, wantSHA)
	}
	return out, nil
}

// decodeZstd decompresses a zstd stream, bounded by the same extraction budget
// that governs archives so a patch cannot be a decompression bomb either.
//
// Reading from an io.Reader rather than a []byte is deliberate: the compressed
// patch never has to be resident alongside its expansion.
func decodeZstd(r io.Reader) ([]byte, error) {
	zr, err := zstd.NewReader(r, zstd.WithDecoderConcurrency(1))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	limit := maxExtractBytes()
	out, err := io.ReadAll(io.LimitReader(zr, limit))
	if err != nil {
		return nil, err
	}
	if int64(len(out)) >= limit {
		return nil, fmt.Errorf("patch expands beyond %d bytes (KEYSTONE_MAX_EXTRACT_BYTES)", limit)
	}
	return out, nil
}

// SHA256Bytes returns the hex digest of b, in the same lowercase-hex shape
// recipes and delta servers use.
func SHA256Bytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// isSHA256Hex reports whether s is exactly 64 lowercase hex characters. Both
// digests reach a URL path, so they are validated at the boundary rather than
// escaped at each use.
func isSHA256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		default:
			return false
		}
	}
	return true
}
