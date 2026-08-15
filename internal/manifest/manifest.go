// Package manifest defines the signed document that says which version of a
// dataset is current.
//
// It exists because a dataset's digest cannot live in the recipe. A recipe is
// signed, and a vulnerability feed published every night would need a new
// signed recipe every night — which reconcile would see as a changed recipe and
// answer by restarting the component. A manifest moves the part that changes
// daily out of the part that is stable, and carries its own signature.
//
// The document is TOML, like recipes and plans, and is parsed with the same
// strict decoder, so a misspelled key is reported rather than silently ignored.
package manifest

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/carlosprados/keystone/internal/validate"
)

// SchemaVersion is the only schema this build understands.
const SchemaVersion = 1

// Manifest is a dataset publication: what the current version is, where to get
// it, and when it was published.
type Manifest struct {
	Schema    int       `toml:"schema"`
	Name      string    `toml:"name"`
	Version   string    `toml:"version"`
	Published time.Time `toml:"published"`
	Artifact  Artifact  `toml:"artifact"`
	Delta     *Delta    `toml:"delta"`

	// UnknownFields lists keys the file carried that this struct does not
	// declare. Same contract as recipes and plans: reported, not rejected, so a
	// manifest written for a newer agent still works on an older one.
	UnknownFields []string `toml:"-"`
}

// Artifact is the dataset payload itself.
type Artifact struct {
	URI    string `toml:"uri"`
	SHA256 string `toml:"sha256"`
	Size   int64  `toml:"size"`
}

// Delta points at a patch server that can turn the version a device already has
// into this one. Always optional, and always a fallback to the full download.
type Delta struct {
	Server string `toml:"server"`
	From   string `toml:"from"`
	Format string `toml:"format"`
}

// Load reads and validates a manifest from disk.
func Load(path string) (*Manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

// Parse decodes and validates a manifest document.
func Parse(b []byte) (*Manifest, error) {
	var m Manifest
	unknown, err := validate.DecodeTOML(b, &m)
	if err != nil {
		return nil, fmt.Errorf("invalid manifest: %w", err)
	}
	m.UnknownFields = unknown
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Validate rejects a manifest that cannot be acted on.
//
// Every one of these is fatal rather than reported, unlike an unknown key: a
// manifest missing its digest or its timestamp is not a manifest from the
// future, it is a broken one, and acting on it means either an unverifiable
// download or a replay that cannot be detected.
func (m *Manifest) Validate() error {
	if m.Schema != SchemaVersion {
		return fmt.Errorf("manifest schema %d is not supported (this build understands %d)", m.Schema, SchemaVersion)
	}
	if strings.TrimSpace(m.Name) == "" {
		return fmt.Errorf("manifest has no name")
	}
	if strings.TrimSpace(m.Version) == "" {
		return fmt.Errorf("manifest %s has no version", m.Name)
	}
	if m.Published.IsZero() {
		return fmt.Errorf("manifest %s has no published timestamp, so a replay of an older one could not be detected", m.Name)
	}
	if strings.TrimSpace(m.Artifact.URI) == "" {
		return fmt.Errorf("manifest %s has no artifact.uri", m.Name)
	}
	if err := validateDigest(m.Artifact.SHA256); err != nil {
		return fmt.Errorf("manifest %s artifact.sha256: %w", m.Name, err)
	}
	if m.Delta != nil && strings.TrimSpace(m.Delta.Server) == "" {
		return fmt.Errorf("manifest %s declares [delta] without a server", m.Name)
	}
	return nil
}

func validateDigest(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return fmt.Errorf("required: without it the download cannot be verified")
	}
	if len(s) != 64 {
		return fmt.Errorf("%q is %d characters, want 64 hex characters", s, len(s))
	}
	for _, c := range s {
		isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
		if !isHex {
			return fmt.Errorf("%q is not hex", s)
		}
	}
	return nil
}

// IsNewerThan reports whether m supersedes a previously accepted publication
// timestamp. This is the anti-replay rule, and an agent must refuse anything it
// returns false for.
//
// Judged on Published, not Version: a date-shaped version string compares
// correctly by accident, and any other versioning scheme does not compare at
// all. Version stays a label for humans.
//
// Note what this does not consult: the local clock. It compares two signed
// values against each other, so a device whose clock is wrong — no RTC, no NTP
// yet — still enforces the rule exactly. Without it, an attacker who can serve
// the URL replays a perfectly valid, perfectly signed bundle from six months
// ago and the scanner using it reports no vulnerabilities.
func (m *Manifest) IsNewerThan(lastAccepted time.Time) bool {
	if lastAccepted.IsZero() {
		return true
	}
	return m.Published.After(lastAccepted)
}

// SigPath is where the detached signature of a manifest lives: next to it.
func SigPath(manifestPath string) string {
	return manifestPath + ".sig"
}
