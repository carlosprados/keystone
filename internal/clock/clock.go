// Package clock decides what time the agent believes it is when it checks a
// certificate's validity.
//
// The problem is specific and it bites in the field: a gateway without an RTC
// boots with its clock at 1970, so every valid certificate is "not yet valid"
// and the device rejects the recipes, artifacts and manifests it needs — often
// before it has any chance to reach NTP, because the component that gives it a
// network is in the plan it just refused to apply.
//
// Ignoring certificate validity is not the answer. Expiry is the reason X.509
// was chosen over a pinned key: it is what makes a compromised signer stop
// working on its own. And it hands an attacker a way to switch that off — put
// the clock back, and a certificate you revoked six months ago is live again.
//
// So the agent keeps its own lower bound on the real time, built from evidence
// rather than trust:
//
//   - the build timestamp of this binary, since it cannot be running before it
//     was built;
//
//   - a high-water mark persisted across restarts, so time never goes backwards
//     between boots.
//
//   - the publication timestamp of an accepted dataset manifest, via Advance.
//
// That third source is why a fleet which never reaches NTP still tracks time:
// each nightly manifest proves a later date than the last. Note the candidate
// that does NOT work, so it is not tried again — a verified certificate's
// NotBefore looks like evidence but cannot raise the mark, because verification
// uses max(clock, mark, build) as its reference and a certificate that
// validates therefore always has a NotBefore at or below it. A manifest's
// timestamp is independent of the validity window of the certificate that
// signed it, so one 90-day signing certificate vouches for manifests proving
// later and later dates.
//
// Verification uses max(system clock, high-water mark, build time). Putting the
// clock back therefore achieves nothing, which closes the attack that matters.
// Putting it forward only expires certificates early: noisy, and not a bypass.
package clock

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/carlosprados/keystone/internal/version"
)

// Policy says what to do when the system clock cannot be trusted.
type Policy string

const (
	// PolicyHighWater verifies against the best lower bound available. A device
	// with no RTC still works, and a replayed certificate still fails. Default.
	PolicyHighWater Policy = "high-water"
	// PolicyStrict refuses to verify anything at all until the system clock is
	// at least as late as the evidence. For deployments that would rather have a
	// device that does nothing than one working from an approximate time.
	PolicyStrict Policy = "strict"
)

// ErrUntrusted is returned under PolicyStrict while the clock is behind the
// evidence. There is deliberately no policy that ignores certificate validity:
// --insecure-skip-verify already exists for development, and a permanent option
// to disregard expiry is a door somebody eventually leaves open.
var ErrUntrusted = fmt.Errorf("system clock is behind known-good time")

// markFile is where the high-water mark lives. Not the state snapshot: that is
// rewritten twice a second and deduplicated by fingerprint, and a timestamp
// changes every time — writing it there would wear out an SD card in months.
const markFile = "clock"

// persistThreshold keeps flash writes rare. The mark only needs to be roughly
// right: its job is to stop time going backwards across a reboot, not to keep
// time.
const persistThreshold = 15 * time.Minute

// Source is the agent's opinion of the current time.
type Source struct {
	policy Policy
	path   string
	floor  time.Time // build timestamp; zero when unknown

	mu        sync.Mutex
	mark      time.Time // high-water mark, persisted
	persisted time.Time // what is actually on disk
}

// New builds a Source, loading any persisted mark from stateDir.
func New(policy Policy, stateDir string) *Source {
	if policy == "" {
		policy = PolicyHighWater
	}
	s := &Source{
		policy: policy,
		path:   filepath.Join(stateDir, markFile),
		floor:  version.BuildTime(),
	}
	s.mark = s.loadMark()
	s.persisted = s.mark
	return s
}

// ParsePolicy validates a policy name.
func ParsePolicy(s string) (Policy, error) {
	switch Policy(strings.ToLower(strings.TrimSpace(s))) {
	case "", PolicyHighWater:
		return PolicyHighWater, nil
	case PolicyStrict:
		return PolicyStrict, nil
	default:
		return "", fmt.Errorf("unknown clock policy %q (supported: %s, %s)", s, PolicyHighWater, PolicyStrict)
	}
}

func (s *Source) loadMark() time.Time {
	b, err := os.ReadFile(s.path)
	if err != nil {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(string(b)))
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// evidence is the latest time we can prove has already passed.
func (s *Source) evidence() time.Time {
	s.mu.Lock()
	mark := s.mark
	s.mu.Unlock()
	if s.floor.After(mark) {
		return s.floor
	}
	return mark
}

// Now returns the agent's best estimate: never earlier than the evidence.
func (s *Source) Now() time.Time {
	now := time.Now().UTC()
	if e := s.evidence(); e.After(now) {
		return e
	}
	return now
}

// Trusted reports whether the system clock is at least as late as the evidence.
// False means it has been set back, or was never set — the device booted at the
// epoch, or NTP has not run yet.
func (s *Source) Trusted() bool {
	e := s.evidence()
	if e.IsZero() {
		// Nothing to compare against: no persisted mark and a binary built
		// without a timestamp. Not evidence of a good clock, but not evidence of
		// a bad one either, and refusing everything on no evidence would strand
		// a device over a `go build`.
		return true
	}
	return !time.Now().UTC().Before(e)
}

// Origin names where the answer came from, for the operator staring at a device
// that will not accept an update.
func (s *Source) Origin() string {
	if s.Trusted() {
		return "system"
	}
	s.mu.Lock()
	mark := s.mark
	s.mu.Unlock()
	if mark.After(s.floor) {
		return "high-water"
	}
	return "build"
}

// VerificationTime returns the time to check certificate validity against, or
// an error under PolicyStrict when the clock cannot be trusted.
func (s *Source) VerificationTime() (time.Time, error) {
	if s.policy == PolicyStrict && !s.Trusted() {
		return time.Time{}, fmt.Errorf("%w: system clock reads %s, known-good time is %s (clock policy is %s)",
			ErrUntrusted,
			time.Now().UTC().Format(time.RFC3339),
			s.evidence().Format(time.RFC3339),
			s.policy)
	}
	return s.Now(), nil
}

// Advance raises the high-water mark with a time proven to have passed: the
// signed publication timestamp of a manifest that has already been verified and
// has already passed the anti-replay check.
//
// Only ever moves forward, and only for authenticated input. Calling it with an
// unverified timestamp would let an attacker push the mark into the future and
// expire every certificate on the device — which is why the two callers do it
// after the signature and the replay rule, never before.
func (s *Source) Advance(t time.Time) {
	if t.IsZero() {
		return
	}
	t = t.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.After(s.mark) {
		s.mark = t
	}
}

// Tick records the passage of time and persists the mark when it has moved on
// enough to be worth a write. Safe to call often.
func (s *Source) Tick() {
	now := time.Now().UTC()
	s.mu.Lock()
	if now.After(s.mark) {
		s.mark = now
	}
	due := s.mark.Sub(s.persisted) >= persistThreshold
	s.mu.Unlock()
	if due {
		_ = s.Persist()
	}
}

// Persist writes the high-water mark. Called on the tick above and at shutdown,
// so a clean stop always leaves the latest mark behind.
func (s *Source) Persist() error {
	s.mu.Lock()
	mark := s.mark
	s.mu.Unlock()
	if mark.IsZero() {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, []byte(mark.Format(time.RFC3339)+"\n"), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	s.mu.Lock()
	s.persisted = mark
	s.mu.Unlock()
	return nil
}
