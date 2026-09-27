package selfupdate

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev, flags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev); log.SetFlags(flags) })
	return &buf
}

// TestConfirmedIsLoggedOnlyWhenSomethingIsRecorded: an ordinary start of the
// version already confirmed meets the same conditions and writes nothing.
// Logging "confirmed" for it read like a trial that had just passed, which is
// what an operator saw after a rollback.
func TestConfirmedIsLoggedOnlyWhenSomethingIsRecorded(t *testing.T) {
	buf := captureLog(t)
	c := NewConfirmation(pendingLayout(t, "", "v1", 0), "v1", false)
	c.MarkConverged()
	if !c.Confirmed() {
		t.Fatal("an ordinary start did not count as confirmed")
	}
	if strings.Contains(buf.String(), "confirmed") {
		t.Errorf("an ordinary start logged a confirmation: %q", buf.String())
	}

	buf.Reset()
	c = NewConfirmation(pendingLayout(t, "v2", "v1", 1), "v2", false)
	c.MarkConverged()
	if !strings.Contains(buf.String(), "version v2 confirmed") {
		t.Errorf("a trial that passed was not logged: %q", buf.String())
	}
}
