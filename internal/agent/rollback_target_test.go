package agent

import "testing"

// A rollback exists to return to a known-good plan. When the plan that failed is
// also the plan it would return to, there is no such thing — and getting there
// costs an outage, because the rollback stops every component first.
func TestCanRollBackTo(t *testing.T) {
	const applied = "runtime/plans/applied.toml"

	cases := []struct {
		name          string
		oldPath       string
		newPath       string
		allowRollback bool
		want          bool
		reason        string
	}{
		{
			name:          "a genuinely different previous plan",
			oldPath:       applied,
			newPath:       applied + ".staging",
			allowRollback: true,
			want:          true,
			reason:        "this is the normal apply: the staged plan failed, the applied one is known good",
		},
		{
			name:          "re-applying the plan already in effect",
			oldPath:       applied,
			newPath:       applied,
			allowRollback: true,
			want:          false,
			reason:        "rolling back would re-read the same file and stop every healthy component to do it",
		},
		{
			name:          "same file written differently",
			oldPath:       "./runtime/plans/applied.toml",
			newPath:       "runtime/plans/applied.toml",
			allowRollback: true,
			want:          false,
			reason:        "cleaning the paths must see through ./",
		},
		{
			name:          "no previous plan",
			oldPath:       "",
			newPath:       applied,
			allowRollback: true,
			want:          false,
			reason:        "the first apply has nothing behind it",
		},
		{
			name:          "whitespace is not a previous plan",
			oldPath:       "   ",
			newPath:       applied,
			allowRollback: true,
			want:          false,
			reason:        "a blank path is an absent one",
		},
		{
			name:          "caller disabled rollback",
			oldPath:       applied,
			newPath:       applied + ".staging",
			allowRollback: false,
			want:          false,
			reason:        "the reconcile path and the rollback's own re-apply pass false",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := canRollBackTo(c.oldPath, c.newPath, c.allowRollback); got != c.want {
				t.Errorf("canRollBackTo(%q, %q, %v)=%v, want %v — %s",
					c.oldPath, c.newPath, c.allowRollback, got, c.want, c.reason)
			}
		})
	}
}

func TestSamePlanFile(t *testing.T) {
	same := [][2]string{
		{"a.toml", "a.toml"},
		{"./a.toml", "a.toml"},
		{"dir/../a.toml", "a.toml"},
		{"  a.toml  ", "a.toml"},
	}
	for _, p := range same {
		if !samePlanFile(p[0], p[1]) {
			t.Errorf("samePlanFile(%q, %q)=false, want true", p[0], p[1])
		}
	}

	different := [][2]string{
		{"a.toml", "b.toml"},
		{"a.toml", "a.toml.staging"},
		{"", "a.toml"},
		{"a.toml", ""},
		{"", ""},
	}
	for _, p := range different {
		if samePlanFile(p[0], p[1]) {
			t.Errorf("samePlanFile(%q, %q)=true, want false", p[0], p[1])
		}
	}
}
