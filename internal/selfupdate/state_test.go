package selfupdate

import (
	"os"
	"strings"
	"testing"
)

func TestUpdateStateRoundTrip(t *testing.T) {
	l := Layout{Root: t.TempDir()}

	want := UpdateState{Pending: "v2", Boots: 2, Confirmed: "v1", LastFailure: "v0 never confirmed"}
	if err := l.SaveState(want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := l.LoadState()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// TestLoadStateWithNoFile: no file means no update has ever been attempted,
// which is the normal state of most devices and not an error.
func TestLoadStateWithNoFile(t *testing.T) {
	l := Layout{Root: t.TempDir()}

	st, err := l.LoadState()
	if err != nil {
		t.Fatalf("load on a fresh install: %v", err)
	}
	if st.Pending != "" || st.Boots != 0 {
		t.Errorf("got %+v, want zero", st)
	}
}

// TestSaveStateKeepsValuesOnOneLine: a newline in a failure message would open
// a second key, which is how a free-text "reason" field becomes a way to set
// the confirmed version — the one field a rollback depends on.
func TestSaveStateKeepsValuesOnOneLine(t *testing.T) {
	l := Layout{Root: t.TempDir()}

	evil := "boom\nKEYSTONE_UPDATE_CONFIRMED=attacker-version"
	if err := l.SaveState(UpdateState{Confirmed: "v1", LastFailure: evil}); err != nil {
		t.Fatalf("save: %v", err)
	}

	st, err := l.LoadState()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if st.Confirmed != "v1" {
		t.Fatalf("confirmed = %q: a newline in another field overwrote it", st.Confirmed)
	}

	// Counted at the start of a line, which is what both parsers look at: the
	// Go one cuts each line at the first "=", and the shell gate anchors its
	// sed with ^. The injected text survives inside the message, harmlessly,
	// because it is no longer at the start of anything.
	b, _ := os.ReadFile(l.StatePath())
	var keys int
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "KEYSTONE_UPDATE_CONFIRMED=") {
			keys++
		}
	}
	if keys != 1 {
		t.Errorf("the file has %d confirmed keys:\n%s", keys, b)
	}
}

// TestSaveStateIsAtomic: the gate can read this at any moment, including right
// after a power cut mid-write. A truncated file reads as "no pending update",
// which would leave a half-installed version running with nothing counting its
// restarts.
func TestSaveStateIsAtomic(t *testing.T) {
	l := Layout{Root: t.TempDir()}

	if err := l.SaveState(UpdateState{Pending: "v2", Confirmed: "v1"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := os.Stat(l.StatePath() + ".tmp"); err == nil {
		t.Error("the temporary file was left behind")
	}
}
