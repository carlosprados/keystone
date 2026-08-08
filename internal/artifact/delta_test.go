package artifact

import (
	"errors"
	"math/rand"
	"strings"
	"testing"

	"github.com/gabstv/go-bsdiff/pkg/bsdiff"
	"github.com/klauspost/compress/zstd"
)

// makePatch produces the same encoding a delta server does: bsdiff, then zstd.
// Keystone itself only ever patches, never diffs, so the generating half lives
// in the test.
func makePatch(t *testing.T, base, target []byte) []byte {
	t.Helper()
	raw, err := bsdiff.Bytes(base, target)
	if err != nil {
		t.Fatalf("bsdiff: %v", err)
	}
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatalf("zstd writer: %v", err)
	}
	defer enc.Close()
	return enc.EncodeAll(raw, nil)
}

// mutate builds a plausible "next release": mostly the same bytes, a scattering
// of changes, and some growth at the end.
func mutate(base []byte) []byte {
	out := make([]byte, len(base))
	copy(out, base)
	for i := 0; i < len(out); i += 512 {
		out[i] ^= 0x5A
	}
	return append(out, []byte("new section appended by the next build")...)
}

func TestApplyDeltaRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	base := make([]byte, 256<<10)
	if _, err := rng.Read(base); err != nil {
		t.Fatalf("rng: %v", err)
	}
	target := mutate(base)
	patch := makePatch(t, base, target)

	got, err := ApplyDelta("", base, patch, SHA256Bytes(target))
	if err != nil {
		t.Fatalf("ApplyDelta: %v", err)
	}
	if string(got) != string(target) {
		t.Fatal("reconstructed bytes differ from the target")
	}

	// The explicit format spelling must behave identically to the empty one.
	if _, err := ApplyDelta(DeltaFormatBsdiffZstd, base, patch, SHA256Bytes(target)); err != nil {
		t.Fatalf("ApplyDelta with explicit format: %v", err)
	}
}

// TestApplyDeltaRejectsWrongResult is the test that matters: a patch that
// applies cleanly but yields the wrong bytes must not be returned. This is the
// property that lets the signed recipe's digest be the trust gate for the
// whole delta path.
func TestApplyDeltaRejectsWrongResult(t *testing.T) {
	base := []byte(strings.Repeat("original contents, at length\n", 400))
	target := mutate(base)
	patch := makePatch(t, base, target)

	// Digest of something else entirely.
	wrong := SHA256Bytes([]byte("a different artifact"))
	_, err := ApplyDelta("", base, patch, wrong)
	if !errors.Is(err, ErrDeltaDigestMismatch) {
		t.Fatalf("err = %v, want ErrDeltaDigestMismatch", err)
	}

	// A patch applied to the wrong base: either bspatch fails outright or it
	// produces junk, and both must surface as an error rather than a result.
	otherBase := []byte(strings.Repeat("a completely different base\n", 400))
	if _, err := ApplyDelta("", otherBase, patch, SHA256Bytes(target)); err == nil {
		t.Fatal("patching the wrong base returned no error")
	}
}

func TestApplyDeltaUnsupportedFormat(t *testing.T) {
	_, err := ApplyDelta("xdelta3", []byte("base"), []byte("patch"),
		SHA256Bytes([]byte("whatever")))
	if !errors.Is(err, ErrUnsupportedDeltaFormat) {
		t.Fatalf("err = %v, want ErrUnsupportedDeltaFormat", err)
	}
	if !strings.Contains(err.Error(), "xdelta3") {
		t.Errorf("error should name the format it could not handle: %v", err)
	}
}

func TestApplyDeltaRejectsMalformedDigest(t *testing.T) {
	if _, err := ApplyDelta("", []byte("base"), []byte("patch"), "not-a-digest"); err == nil {
		t.Fatal("a malformed declared digest was accepted")
	}
}

func TestDeltaURL(t *testing.T) {
	const (
		from = "1111111111111111111111111111111111111111111111111111111111111111"
		to   = "2222222222222222222222222222222222222222222222222222222222222222"
	)
	want := "https://ota.example.com/delta/" + from + "/" + to

	for _, server := range []string{
		"https://ota.example.com",
		"https://ota.example.com/", // a trailing slash must not double up
	} {
		got, err := DeltaURL(server, from, to)
		if err != nil {
			t.Fatalf("DeltaURL(%q): %v", server, err)
		}
		if got != want {
			t.Errorf("DeltaURL(%q) = %q, want %q", server, got, want)
		}
	}

	// A server with a base path keeps it.
	got, err := DeltaURL("https://ota.example.com/ota", from, to)
	if err != nil {
		t.Fatalf("DeltaURL with base path: %v", err)
	}
	if got != "https://ota.example.com/ota/delta/"+from+"/"+to {
		t.Errorf("base path not preserved: %q", got)
	}

	bad := map[string][3]string{
		"empty server":      {"", from, to},
		"relative server":   {"ota.example.com", from, to},
		"traversal as from": {"https://ota.example.com", "../../etc/passwd", to},
		"short from":        {"https://ota.example.com", "abc", to},
		// Digests are lowercase hex everywhere else in Keystone; accepting
		// uppercase would make the same artifact hash to two cache keys.
		"uppercase hex to":   {"https://ota.example.com", from, strings.ToUpper(strings.Repeat("ab", 32))},
		"empty to":           {"https://ota.example.com", from, ""},
		"non-hex characters": {"https://ota.example.com", from, strings.Repeat("z", 64)},
	}
	for name, args := range bad {
		if _, err := DeltaURL(args[0], args[1], args[2]); err == nil {
			t.Errorf("%s: DeltaURL accepted invalid input", name)
		}
	}
}
