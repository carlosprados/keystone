package validate

import (
	"fmt"
	"strings"
)

// maxSegmentLen bounds a path-segment length to avoid pathological filenames.
const maxSegmentLen = 128

// ValidatePathSegment rejects any string that is unsafe to use as a single
// filesystem path component. A valid segment is non-empty, within the length
// bound, restricted to the allowlist [A-Za-z0-9._+-], and is neither "." nor
// ".." nor contains a ".." traversal sequence.
//
// This is the guard that stops attacker-controlled recipe metadata (name,
// version) from escaping the runtime directories: because path separators are
// not in the allowlist, a value can never form "../" and therefore cannot
// traverse out of the directory it is joined into. The explicit ".." checks
// are defence in depth.
//
// kind is used only to produce a readable error (e.g. "name", "version").
func ValidatePathSegment(kind, s string) error {
	if s == "" {
		return fmt.Errorf("%s must not be empty", kind)
	}
	if len(s) > maxSegmentLen {
		return fmt.Errorf("%s too long (%d > %d characters)", kind, len(s), maxSegmentLen)
	}
	if s == "." || s == ".." {
		return fmt.Errorf("%s must not be %q", kind, s)
	}
	if strings.Contains(s, "..") {
		return fmt.Errorf("%s must not contain %q", kind, "..")
	}
	for _, r := range s {
		if !isAllowedSegmentRune(r) {
			return fmt.Errorf("%s contains illegal character %q (allowed: A-Z a-z 0-9 . _ + -)", kind, r)
		}
	}
	return nil
}

func isAllowedSegmentRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z':
		return true
	case r >= 'A' && r <= 'Z':
		return true
	case r >= '0' && r <= '9':
		return true
	case r == '.' || r == '_' || r == '+' || r == '-':
		return true
	default:
		return false
	}
}
