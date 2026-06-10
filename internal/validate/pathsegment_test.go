package validate

import "testing"

func TestValidatePathSegment(t *testing.T) {
	valid := []string{
		"nginx",
		"my-app",
		"my_app",
		"1.0.0",
		"1.2.3-rc1",
		"1.2.3+build.5",
		"a.b.c",
		"A1",
	}
	for _, s := range valid {
		if err := ValidatePathSegment("seg", s); err != nil {
			t.Errorf("ValidatePathSegment(%q) = %v, want nil", s, err)
		}
	}

	invalid := []string{
		"",            // empty
		".",           // current dir
		"..",          // parent dir
		"../etc",      // traversal
		"a/../b",      // embedded traversal
		"foo/bar",     // path separator
		"foo\\bar",    // windows separator
		"/etc/passwd", // absolute
		"a b",         // space
		"name\x00",    // NUL
		"tab\tname",   // control char
		"x..y",        // contains ".."
		"héllo",       // non-ASCII outside allowlist
	}
	for _, s := range invalid {
		if err := ValidatePathSegment("seg", s); err == nil {
			t.Errorf("ValidatePathSegment(%q) = nil, want error", s)
		}
	}
}

func TestValidatePathSegmentTooLong(t *testing.T) {
	long := make([]byte, maxSegmentLen+1)
	for i := range long {
		long[i] = 'a'
	}
	if err := ValidatePathSegment("seg", string(long)); err == nil {
		t.Errorf("ValidatePathSegment(<%d chars>) = nil, want error", maxSegmentLen+1)
	}
}
