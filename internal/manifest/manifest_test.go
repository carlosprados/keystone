package manifest

import (
	"strings"
	"testing"
	"time"
)

const goodManifest = `
schema    = 1
name      = "com.example.cve-bundle"
version   = "2026-08-15"
published = 2026-08-15T03:00:00Z

[artifact]
uri    = "https://hub.plant.local/datasets/cve-2026-08-15.tar"
sha256 = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
size   = 184320000
`

func TestParseGoodManifest(t *testing.T) {
	m, err := Parse([]byte(goodManifest))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if m.Name != "com.example.cve-bundle" {
		t.Errorf("name=%q", m.Name)
	}
	if !m.Published.Equal(time.Date(2026, 8, 15, 3, 0, 0, 0, time.UTC)) {
		t.Errorf("published=%s", m.Published)
	}
	if m.Artifact.Size != 184320000 {
		t.Errorf("size=%d", m.Artifact.Size)
	}
	if m.Delta != nil {
		t.Error("no [delta] was declared but one was parsed")
	}
	if len(m.UnknownFields) != 0 {
		t.Errorf("unexpected unknown fields: %v", m.UnknownFields)
	}
}

// Every one of these makes the manifest unusable, so each is fatal rather than
// reported: a manifest with no digest cannot be verified, and one with no
// timestamp cannot be told apart from a replay.
func TestValidateRejects(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(string) string
		want   string
	}{
		{"unsupported schema", func(s string) string {
			return strings.Replace(s, "schema    = 1", "schema    = 2", 1)
		}, "schema 2 is not supported"},
		{"no name", func(s string) string {
			return strings.Replace(s, `name      = "com.example.cve-bundle"`, `name      = ""`, 1)
		}, "no name"},
		{"no version", func(s string) string {
			return strings.Replace(s, `version   = "2026-08-15"`, `version   = ""`, 1)
		}, "no version"},
		{"no uri", func(s string) string {
			return strings.Replace(s, `uri    = "https://hub.plant.local/datasets/cve-2026-08-15.tar"`, `uri    = ""`, 1)
		}, "no artifact.uri"},
		{"no digest", func(s string) string {
			return strings.Replace(s, `sha256 = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"`, `sha256 = ""`, 1)
		}, "cannot be verified"},
		{"short digest", func(s string) string {
			return strings.Replace(s, "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08", "9f86d0", 1)
		}, "want 64 hex characters"},
		{"non-hex digest", func(s string) string {
			return strings.Replace(s, "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
				"zzzzd081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08", 1)
		}, "is not hex"},
		{"delta without a server", func(s string) string {
			return s + "\n[delta]\nfrom = \"2026-08-14\"\n"
		}, "without a server"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse([]byte(c.mutate(goodManifest)))
			if err == nil {
				t.Fatal("accepted a manifest that cannot be acted on")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not mention %q", err, c.want)
			}
		})
	}
}

// A missing timestamp has to be fatal specifically because of the replay rule:
// without it every publication compares equal and an old bundle is accepted
// forever.
func TestValidateRejectsMissingPublished(t *testing.T) {
	withoutPublished := strings.Replace(goodManifest, "published = 2026-08-15T03:00:00Z", "", 1)
	_, err := Parse([]byte(withoutPublished))
	if err == nil {
		t.Fatal("accepted a manifest with no published timestamp")
	}
	if !strings.Contains(err.Error(), "replay") {
		t.Errorf("error %q does not explain why the timestamp matters", err)
	}
}

// An unknown key is reported, not rejected: the same tolerance recipes and plans
// have, so a manifest written for a newer agent still works on an older one.
func TestUnknownFieldsAreReportedNotRejected(t *testing.T) {
	m, err := Parse([]byte(goodManifest + "\nfuture_field = \"whatever\"\n"))
	if err != nil {
		t.Fatalf("an unknown key must not be fatal: %v", err)
	}
	if len(m.UnknownFields) != 1 || !strings.Contains(m.UnknownFields[0], "future_field") {
		t.Errorf("unknown fields=%v, want one mentioning future_field", m.UnknownFields)
	}
}

// The anti-replay rule. It compares two signed values, never the local clock,
// so a device with no RTC enforces it exactly like any other.
func TestIsNewerThan(t *testing.T) {
	published := time.Date(2026, 8, 15, 3, 0, 0, 0, time.UTC)
	m := &Manifest{Published: published}

	cases := []struct {
		name string
		last time.Time
		want bool
	}{
		{"nothing accepted yet", time.Time{}, true},
		{"yesterday's publication is in the field", published.Add(-24 * time.Hour), true},
		{"the same publication replayed", published, false},
		{"a newer one has already been accepted", published.Add(time.Second), false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := m.IsNewerThan(c.last); got != c.want {
				t.Errorf("IsNewerThan(%s)=%v, want %v", c.last, got, c.want)
			}
		})
	}
}

// The attack the rule exists for: someone who can serve the URL hands back a
// perfectly valid, perfectly signed bundle from six months ago, and the scanner
// that consumes it reports no vulnerabilities.
func TestIsNewerThanRefusesASixMonthOldReplay(t *testing.T) {
	inTheField := time.Date(2026, 8, 15, 3, 0, 0, 0, time.UTC)
	replayed := &Manifest{
		Name:      "com.example.cve-bundle",
		Published: inTheField.Add(-180 * 24 * time.Hour),
	}
	if replayed.IsNewerThan(inTheField) {
		t.Error("a six-month-old signed bundle was accepted over the current one")
	}
}

func TestSigPath(t *testing.T) {
	if got := SigPath("cve.manifest.toml"); got != "cve.manifest.toml.sig" {
		t.Errorf("SigPath=%q", got)
	}
}
