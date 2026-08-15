package version

import "time"

// These variables can be overridden via -ldflags during build/publish.
var (
	Version = "0.1.0-dev"
	Commit  = "unknown"
	// Date is the build timestamp in RFC3339, injected at build time. It is not
	// decoration: a binary cannot run before it was built, so this is a lower
	// bound on the real time that holds even on a device with no RTC, whose
	// clock reads 1970 on its very first boot. See internal/clock.
	Date = ""
)

// BuildTime returns the build timestamp, or the zero time when the binary was
// built without one (a plain `go build`). Callers must treat the zero value as
// "unknown", never as "the epoch".
func BuildTime() time.Time {
	if Date == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, Date)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}
