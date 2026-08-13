package recipe

import (
	"strings"
	"testing"
)

const processRecipe = `
[metadata]
name = "app"
version = "1.0.0"
[lifecycle.run.exec]
command = "./app"
args = ["--port", "8080"]
`

const containerRecipe = `
[metadata]
name = "web"
version = "2.1.0"
[lifecycle.run]
type = "container"
restart_policy = "always"
[lifecycle.run.container]
image = "docker.io/library/nginx:alpine"
`

func TestUnmarshalAcceptsValid(t *testing.T) {
	if _, err := Unmarshal([]byte(processRecipe)); err != nil {
		t.Errorf("process recipe rejected: %v", err)
	}
	if _, err := Unmarshal([]byte(containerRecipe)); err != nil {
		t.Errorf("container recipe rejected: %v", err)
	}
}

// TestUnmarshalIgnoresUnknownFields pins the property that lets a new recipe
// be published to a fleet of mixed agent versions: an agent that predates a
// field must ignore it, not reject the recipe.
//
// This is load-bearing and it is currently a consequence of two lax defaults —
// go-toml does not reject unknown keys unless asked, and the validation schema
// sets no "additionalProperties": false. Tightening either one is a natural
// hardening step that would, fleet-wide, make older agents start refusing
// recipes that carry a newer field, with a symptom that points nowhere near
// the change that caused it. If this test fails, that is what happened.
func TestUnmarshalIgnoresUnknownFields(t *testing.T) {
	const fromTheFuture = `
[metadata]
name = "app"
version = "1.0.0"
some_future_key = "whatever"
[lifecycle.run.exec]
command = "./app"
[[artifacts]]
uri = "https://example.com/app.tar.gz"
sha256 = "0000000000000000000000000000000000000000000000000000000000000000"
[artifacts.not_invented_yet]
mode = "turbo"
`
	r, err := Unmarshal([]byte(fromTheFuture))
	if err != nil {
		t.Fatalf("recipe carrying unknown fields was rejected: %v", err)
	}
	if r.Metadata.Name != "app" {
		t.Errorf("metadata.name = %q, want %q", r.Metadata.Name, "app")
	}
	if len(r.Artifacts) != 1 || r.Artifacts[0].URI == "" {
		t.Errorf("artifacts did not survive the unknown block: %+v", r.Artifacts)
	}
}

func TestUnmarshalDelta(t *testing.T) {
	const withDelta = `
[metadata]
name = "app"
version = "1.0.0"
[lifecycle.run.exec]
command = "./app"
[[artifacts]]
uri = "https://example.com/app-1.0.0.tar.gz"
sha256 = "1111111111111111111111111111111111111111111111111111111111111111"
unpack = true
[artifacts.delta]
server = "https://ota.example.com"
sha256 = "2222222222222222222222222222222222222222222222222222222222222222"
`
	r, err := Unmarshal([]byte(withDelta))
	if err != nil {
		t.Fatalf("recipe with a delta block rejected: %v", err)
	}
	d := r.Artifacts[0].Delta
	if d == nil {
		t.Fatal("delta block did not decode")
	}
	if d.Server != "https://ota.example.com" {
		t.Errorf("server = %q", d.Server)
	}
	if d.SHA256 != "2222222222222222222222222222222222222222222222222222222222222222" {
		t.Errorf("sha256 = %q", d.SHA256)
	}
	if d.Format != "" {
		t.Errorf("format = %q, want empty (meaning the default)", d.Format)
	}

	// A delta block without the digest has nothing to verify the patch
	// against, so it must be rejected at parse time rather than silently
	// skipped at apply time.
	const noDigest = `
[metadata]
name = "app"
version = "1.0.0"
[lifecycle.run.exec]
command = "./app"
[[artifacts]]
uri = "https://example.com/app.tar.gz"
[artifacts.delta]
server = "https://ota.example.com"
`
	if _, err := Unmarshal([]byte(noDigest)); err == nil {
		t.Error("delta block without sha256 was accepted, want error")
	}
}

func TestUnmarshalRejectsInvalid(t *testing.T) {
	cases := map[string]string{
		"missing lifecycle": `
[metadata]
name = "x"
version = "1.0.0"
`,
		"exec without command": `
[metadata]
name = "x"
version = "1.0.0"
[lifecycle.run.exec]
args = ["a"]
`,
		"bad restart_policy": `
[metadata]
name = "x"
version = "1.0.0"
[lifecycle.run]
restart_policy = "whenever"
[lifecycle.run.exec]
command = "./x"
`,
		"traversal name": `
[metadata]
name = "../../escape"
version = "1.0.0"
[lifecycle.run.exec]
command = "./x"
`,
	}
	for name, toml := range cases {
		if _, err := Unmarshal([]byte(toml)); err == nil {
			t.Errorf("%s: Unmarshal accepted an invalid recipe, want error", name)
		}
	}
}

// TestUnmarshalReportsUnknownFields is the other half of
// TestUnmarshalIgnoresUnknownFields. Tolerating a field an older agent has
// never heard of is what lets a recipe reach a mixed-version fleet; the price
// is that a typo looks exactly the same from here. So the loader still accepts
// the recipe, but it says what it did not understand — and it says where, since
// a key alone is hard to find in a file that repeats [[artifacts]] four times.
//
// If this test fails while TestUnmarshalIgnoresUnknownFields still passes, the
// tolerance became silent again, which is the bug this pair exists to prevent.
func TestUnmarshalReportsUnknownFields(t *testing.T) {
	const withTypo = `
[metadata]
name = "app"
version = "1.0.0"
[lifecycle.run]
restart_polciy = "never"
[lifecycle.run.exec]
command = "./app"
`
	r, err := Unmarshal([]byte(withTypo))
	if err != nil {
		t.Fatalf("recipe with an unknown field was rejected: %v", err)
	}
	if len(r.UnknownFields) != 1 {
		t.Fatalf("UnknownFields = %v, want exactly one entry", r.UnknownFields)
	}
	got := r.UnknownFields[0]
	if !strings.Contains(got, "lifecycle.run.restart_polciy") {
		t.Errorf("report %q does not name the offending key", got)
	}
	if !strings.Contains(got, "line 6") {
		t.Errorf("report %q does not carry the line number", got)
	}
	// The typo is the whole point: the field it was aiming at stayed unset, and
	// an empty restart policy resolves to "always" further down.
	if r.Lifecycle.Run.RestartPolicy != "" {
		t.Errorf("RestartPolicy = %q, want empty — the typo must not have set it", r.Lifecycle.Run.RestartPolicy)
	}
}

// A recipe whose keys are all understood must report nothing, or the warning
// becomes noise an operator learns to scroll past.
func TestUnmarshalReportsNothingWhenClean(t *testing.T) {
	const clean = `
[metadata]
name = "app"
version = "1.0.0"
[lifecycle.run]
restart_policy = "never"
[lifecycle.run.exec]
command = "./app"
`
	r, err := Unmarshal([]byte(clean))
	if err != nil {
		t.Fatalf("valid recipe rejected: %v", err)
	}
	if len(r.UnknownFields) != 0 {
		t.Errorf("UnknownFields = %v, want none", r.UnknownFields)
	}
}
