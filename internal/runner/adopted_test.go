package runner

import (
	"os"
	"testing"
)

func TestAdoptedHandlesSayWhatTheyAre(t *testing.T) {
	h, err := NewProcessRunner().Adopt(os.Getpid(), "self")
	if err != nil {
		t.Fatal(err)
	}
	if !h.Adopted() {
		t.Error("an adopted handle does not report it")
	}
	if (&ProcessHandle{pid: os.Getpid()}).Adopted() {
		t.Error("a started handle reports being adopted")
	}
}
