package agent

import "testing"

// TestMobileImageTag covers the parsing that makes this warning trustworthy.
//
// The registry-with-a-port cases are the point: splitting on the last colon
// without checking it comes after the last slash reads "5000/app" as the tag,
// which both misses a real ":latest" and warns about a reference that is
// correctly pinned. A warning that fires when it should not is learned and
// ignored, and then it is not there when it matters.
func TestMobileImageTag(t *testing.T) {
	cases := []struct {
		ref        string
		wantTag    string
		wantMobile bool
	}{
		{"app:latest", "latest", true},
		{"app", "latest", true},
		{"app:sha-abc1234", "sha-abc1234", false},
		{"docker.io/library/nginx:1.27", "1.27", false},
		{"docker.io/library/nginx", "latest", true},

		// Registry with a port: the colon before the slash is not a separator.
		{"registry.lab.example:5000/rotaflux", "latest", true},
		{"registry.lab.example:5000/rotaflux:sha-abc1234", "sha-abc1234", false},
		{"registry.lab.example:5000/rotaflux:latest", "latest", true},
		{"registry.lab.example:5000/team/rotaflux:v1.2.3", "v1.2.3", false},

		// A digest pins harder than any tag.
		{"app@sha256:0123456789abcdef", "", false},
		{"registry.lab.example:5000/app@sha256:0123456789abcdef", "", false},

		{"app:main", "main", true},
		{"app:stable", "stable", true},
	}

	for _, c := range cases {
		tag, mobile := mobileImageTag(c.ref)
		if tag != c.wantTag || mobile != c.wantMobile {
			t.Errorf("mobileImageTag(%q) = (%q, %v), want (%q, %v)",
				c.ref, tag, mobile, c.wantTag, c.wantMobile)
		}
	}
}
