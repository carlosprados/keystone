package selfupdate

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// UpdateState is what the agent and the pre-start gate say to each other.
//
// The format is deliberately `KEY=value`, one per line, and not JSON: the gate
// that reads it runs before the agent starts, has to work when the binary it is
// about to start is broken, and therefore cannot depend on the agent, on jq, or
// on anything that might not be installed on a gateway. Two greps and a cut can
// read this.
//
// It is also why the gate never sources the file: it is written by a process
// that could in principle be compromised, and `.` on a file of key=value pairs
// is arbitrary code execution. The gate parses it.
type UpdateState struct {
	// Pending is the version that was installed and is being tried. Empty when
	// there is no update in flight.
	Pending string
	// Boots counts how many times the gate has let a pending version start
	// without it confirming. It is the only thing standing between a version
	// that cannot run and a device that is gone.
	Boots int
	// Confirmed is the last version that started and said so. This is what a
	// failed update is rolled back to, so it is the field that must never be
	// wrong.
	Confirmed string
	// LastFailure records why the previous attempt was reverted, for the
	// telemetry that a device reports when nobody can go and look.
	LastFailure string
}

const stateFileName = "update.env"

// StatePath is where the gate and the agent meet.
func (l Layout) StatePath() string {
	return filepath.Join(l.Root, "state", stateFileName)
}

// LoadState reads the update state. A missing file is not an error: it means no
// update has ever been attempted, which is the normal state of most devices.
func (l Layout) LoadState() (UpdateState, error) {
	var st UpdateState

	f, err := os.Open(l.StatePath())
	if err != nil {
		if os.IsNotExist(err) {
			return st, nil
		}
		return st, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"`)
		switch strings.TrimSpace(key) {
		case "KEYSTONE_UPDATE_PENDING":
			st.Pending = value
		case "KEYSTONE_UPDATE_BOOTS":
			st.Boots, _ = strconv.Atoi(value)
		case "KEYSTONE_UPDATE_CONFIRMED":
			st.Confirmed = value
		case "KEYSTONE_UPDATE_LAST_FAILURE":
			st.LastFailure = value
		}
	}
	return st, sc.Err()
}

// SaveState writes the update state atomically.
//
// Atomically because the gate may read it at any moment — including immediately
// after a power cut mid-write. A truncated state file would read as "no pending
// update", which would leave a half-installed version active with nothing
// counting its restarts.
func (l Layout) SaveState(st UpdateState) error {
	dir := filepath.Dir(l.StatePath())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	var b strings.Builder
	b.WriteString("# Written by keystone. Read by the pre-start gate.\n")
	fmt.Fprintf(&b, "KEYSTONE_UPDATE_PENDING=%s\n", sanitiseValue(st.Pending))
	fmt.Fprintf(&b, "KEYSTONE_UPDATE_BOOTS=%d\n", st.Boots)
	fmt.Fprintf(&b, "KEYSTONE_UPDATE_CONFIRMED=%s\n", sanitiseValue(st.Confirmed))
	fmt.Fprintf(&b, "KEYSTONE_UPDATE_LAST_FAILURE=%s\n", sanitiseValue(st.LastFailure))

	tmp := l.StatePath() + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, l.StatePath()); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// MarkPending records that a version is installed and about to be tried.
//
// Boots is reset here, not incremented: this is the start of a trial, and the
// count belongs to the gate. Confirmed is left alone — it is what the trial
// falls back to, and overwriting it with the version being tried would remove
// the only thing a rollback can return to.
func (l Layout) MarkPending(version string) error {
	if err := validVersionName(version); err != nil {
		return err
	}
	st, err := l.LoadState()
	if err != nil {
		return err
	}
	if st.Confirmed == "" {
		// First update on a device that has never confirmed anything. Whatever
		// is running now is, by definition, working — record it so there is
		// somewhere to go back to.
		if current, cerr := l.Current(); cerr == nil && current != "" && current != version {
			st.Confirmed = current
		}
	}
	st.Pending = version
	st.Boots = 0
	return l.SaveState(st)
}

// sanitiseValue keeps a value on one line and out of the parser's way. A
// newline in a version string or a failure message would otherwise inject a
// second key, which is how a "reason" field turns into a way to set the
// confirmed version.
func sanitiseValue(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return strings.TrimSpace(s)
}
