package clock

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParsePolicy(t *testing.T) {
	for _, in := range []string{"", "high-water", "HIGH-WATER", "  high-water  "} {
		got, err := ParsePolicy(in)
		if err != nil || got != PolicyHighWater {
			t.Errorf("ParsePolicy(%q) = %q, %v", in, got, err)
		}
	}
	if got, err := ParsePolicy("strict"); err != nil || got != PolicyStrict {
		t.Errorf("ParsePolicy(strict) = %q, %v", got, err)
	}
	// Deliberately absent: anything that would ignore certificate expiry.
	for _, bad := range []string{"permissive", "ignore", "off", "none"} {
		if _, err := ParsePolicy(bad); err == nil {
			t.Errorf("ParsePolicy(%q) was accepted", bad)
		}
	}
}

// The case this package exists for: a gateway with no RTC boots at 1970. Every
// certificate would be "not yet valid", so verification has to use the evidence
// instead — and the device must say out loud that it is doing so.
func TestClockBehindTheMarkUsesTheMark(t *testing.T) {
	dir := t.TempDir()
	// Truncated to the second because that is RFC3339's resolution, and the mark
	// is stored in that format. Sub-second precision is irrelevant to a mark
	// whose job is to stop time going backwards across a reboot.
	knownGood := time.Now().UTC().Add(400 * 24 * time.Hour).Truncate(time.Second)
	writeMark(t, dir, knownGood)

	s := New(PolicyHighWater, dir)

	if s.Trusted() {
		t.Error("a system clock behind the persisted mark must not be reported as trusted")
	}
	if got := s.Now(); got.Before(knownGood) {
		t.Errorf("Now()=%s, want at least the mark %s", got, knownGood)
	}
	if got := s.Origin(); got != "high-water" {
		t.Errorf("Origin()=%q, want high-water", got)
	}

	// high-water still verifies, using the later time.
	at, err := s.VerificationTime()
	if err != nil {
		t.Fatalf("high-water must not refuse to verify: %v", err)
	}
	if at.Before(knownGood) {
		t.Errorf("verification time %s is before the mark %s", at, knownGood)
	}
}

// strict is for deployments that would rather have a device that does nothing
// than one working from an approximate time.
func TestStrictRefusesWhileTheClockIsBehind(t *testing.T) {
	dir := t.TempDir()
	writeMark(t, dir, time.Now().UTC().Add(400*24*time.Hour))

	s := New(PolicyStrict, dir)
	_, err := s.VerificationTime()
	if err == nil {
		t.Fatal("strict accepted a clock behind the mark")
	}
	if !errors.Is(err, ErrUntrusted) {
		t.Errorf("error %v is not ErrUntrusted, so callers cannot distinguish it", err)
	}
}

func TestTrustedWhenTheClockIsAhead(t *testing.T) {
	dir := t.TempDir()
	writeMark(t, dir, time.Now().UTC().Add(-24*time.Hour))

	s := New(PolicyHighWater, dir)
	if !s.Trusted() {
		t.Error("a clock later than the mark is trusted")
	}
	if got := s.Origin(); got != "system" {
		t.Errorf("Origin()=%q, want system", got)
	}
	if _, err := New(PolicyStrict, dir).VerificationTime(); err != nil {
		t.Errorf("strict refused a good clock: %v", err)
	}
}

// No mark and no build stamp is no evidence either way. Refusing everything on
// no evidence would strand a device built with a plain `go build`.
func TestNoEvidenceIsNotDistrust(t *testing.T) {
	s := New(PolicyHighWater, t.TempDir())
	s.floor = time.Time{}
	if !s.Trusted() {
		t.Error("with no mark and no build stamp the clock must not be called untrusted")
	}
	if _, err := New(PolicyStrict, t.TempDir()).VerificationTime(); err != nil {
		// only valid while the test binary has no build stamp
		if !s.floor.IsZero() {
			t.Errorf("strict refused with no evidence: %v", err)
		}
	}
}

// The mark only ever moves forward: that is what makes putting a clock back
// useless to an attacker, and it must hold for manifest timestamps too.
func TestAdvanceOnlyMovesForward(t *testing.T) {
	s := New(PolicyHighWater, t.TempDir())
	base := time.Date(2026, 8, 15, 3, 0, 0, 0, time.UTC)

	s.Advance(base)
	if !s.mark.Equal(base) {
		t.Fatalf("mark=%s, want %s", s.mark, base)
	}
	s.Advance(base.Add(-180 * 24 * time.Hour))
	if !s.mark.Equal(base) {
		t.Errorf("a replayed older manifest moved the mark back to %s", s.mark)
	}
	s.Advance(base.Add(24 * time.Hour))
	if !s.mark.Equal(base.Add(24 * time.Hour)) {
		t.Errorf("mark=%s, want the newer manifest to have advanced it", s.mark)
	}
	s.Advance(time.Time{})
	if !s.mark.Equal(base.Add(24 * time.Hour)) {
		t.Errorf("the zero time moved the mark to %s", s.mark)
	}
}

func TestPersistAndReload(t *testing.T) {
	dir := t.TempDir()
	s := New(PolicyHighWater, dir)
	mark := time.Date(2026, 8, 15, 3, 0, 0, 0, time.UTC)
	s.mu.Lock()
	s.mark = mark
	s.mu.Unlock()
	if err := s.Persist(); err != nil {
		t.Fatalf("Persist: %v", err)
	}

	reloaded := New(PolicyHighWater, dir)
	if !reloaded.mark.Equal(mark) {
		t.Errorf("reloaded mark=%s, want %s", reloaded.mark, mark)
	}
}

// Writes have to be rare: the poller calls Tick twice a second, and flash on an
// edge device does not survive being written at that rate. One write to
// establish the mark, then nothing until the threshold has passed.
func TestTickWritesOnceThenRarely(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, markFile)
	s := New(PolicyHighWater, dir)

	// The first tick lays down a mark, so an immediate power cut still leaves
	// this boot's evidence behind.
	s.Tick()
	first, err := os.Stat(path)
	if err != nil {
		t.Fatalf("the first tick did not establish a mark: %v", err)
	}

	// Every tick after it, for the next quarter of an hour, must not touch disk.
	for range 20 {
		s.Tick()
	}
	again, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !again.ModTime().Equal(first.ModTime()) {
		t.Error("the mark was rewritten on a later tick; the poller runs twice a second")
	}

	// Once the threshold has passed, it is written again.
	s.mu.Lock()
	s.persisted = time.Now().UTC().Add(-2 * persistThreshold)
	s.mu.Unlock()
	s.Tick()
	third, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if third.ModTime().Equal(first.ModTime()) && third.Size() == first.Size() {
		t.Errorf("no write after %s had passed", persistThreshold)
	}
}

// An unreadable or corrupt mark is not evidence, and must not be fatal: the
// device still has to boot.
func TestCorruptMarkIsIgnored(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, markFile), []byte("not a timestamp"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := New(PolicyHighWater, dir)
	if !s.mark.IsZero() {
		t.Errorf("mark=%s, want zero from a corrupt file", s.mark)
	}
}

func writeMark(t *testing.T, dir string, at time.Time) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, markFile), []byte(at.Format(time.RFC3339)), 0o644); err != nil {
		t.Fatal(err)
	}
}
