package agent

import "testing"

func TestShouldResumeLastPlan(t *testing.T) {
	cases := []struct {
		status string
		want   bool
		reason string
	}{
		{"", true, "empty (legacy or first run) resumes"},
		{"running", true, "running resumes"},
		{"failed", true, "failed resumes"},
		{"applying", true, "interrupted apply resumes (the v0.1.0 boot-resume bug)"},
		{"APPLYING", true, "case-insensitive"},
		{"  applying  ", true, "trims whitespace"},
		{"stopped", false, "explicit stop is respected"},
		{"STOPPED", false, "case-insensitive stop"},
		{"dry-run", false, "dry-run never installed anything, do not resume"},
		{"unknown-future-status", true, "unknown values default to resume (safer than silent skip)"},
	}
	for _, c := range cases {
		if got := shouldResumeLastPlan(c.status); got != c.want {
			t.Errorf("shouldResumeLastPlan(%q)=%v, want %v (%s)", c.status, got, c.want, c.reason)
		}
	}
}
