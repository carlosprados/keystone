//go:build linux

package runner

import (
	"os"
	"os/user"
	"reflect"
	"strconv"
	"testing"
)

func TestCapabilityByName(t *testing.T) {
	for _, in := range []string{"CAP_NET_RAW", "cap_net_raw", "net_raw", "  NET_RAW  ", "Cap_Net_Raw"} {
		got, err := capabilityByName(in)
		if err != nil {
			t.Errorf("capabilityByName(%q) errored: %v", in, err)
			continue
		}
		if name := capabilityName(got); name != "CAP_NET_RAW" {
			t.Errorf("capabilityByName(%q) resolved to %s, want CAP_NET_RAW", in, name)
		}
	}
	for _, bad := range []string{"", "   ", "CAP_MAKE_COFFEE", "SYS_ADMINISTRATOR", "CAP_"} {
		if _, err := capabilityByName(bad); err == nil {
			t.Errorf("capabilityByName(%q) accepted an unknown capability; a typo must not silently widen or narrow confinement", bad)
		}
	}
}

func TestCapMask(t *testing.T) {
	// CAP_CHOWN is 0, CAP_NET_BIND_SERVICE is 10, CAP_BPF is 39 (second word).
	chown, _ := capabilityByName("CAP_CHOWN")
	bind, _ := capabilityByName("CAP_NET_BIND_SERVICE")
	bpf, _ := capabilityByName("CAP_BPF")

	if got := capMask(nil); got != [2]uint32{0, 0} {
		t.Errorf("capMask(nil)=%v, want an empty mask", got)
	}
	if got := capMask([]uintptr{chown}); got != [2]uint32{1, 0} {
		t.Errorf("capMask(CAP_CHOWN)=%v, want {1,0}", got)
	}
	if got := capMask([]uintptr{bind}); got != [2]uint32{1 << 10, 0} {
		t.Errorf("capMask(CAP_NET_BIND_SERVICE)=%v, want {0x400,0}", got)
	}
	if got := capMask([]uintptr{bpf}); got[1] == 0 {
		t.Errorf("capMask(CAP_BPF)=%v, want a bit in the high word (capability %d)", got, bpf)
	}
}

func TestResolveUser(t *testing.T) {
	self, err := user.Current()
	if err != nil {
		t.Fatalf("cannot determine the current user: %v", err)
	}
	selfUID, _ := strconv.Atoi(self.Uid)
	selfGID, _ := strconv.Atoi(self.Gid)

	t.Run("by name", func(t *testing.T) {
		uid, gid, _, err := resolveUser(self.Username)
		if err != nil {
			t.Fatalf("resolveUser(%q): %v", self.Username, err)
		}
		if uid != selfUID || gid != selfGID {
			t.Errorf("got uid=%d gid=%d, want %d/%d (the user's primary group)", uid, gid, selfUID, selfGID)
		}
	})

	t.Run("numeric uid gets the primary group from the database", func(t *testing.T) {
		uid, gid, _, err := resolveUser(self.Uid)
		if err != nil {
			t.Fatalf("resolveUser(%q): %v", self.Uid, err)
		}
		if uid != selfUID || gid != selfGID {
			t.Errorf("got uid=%d gid=%d, want %d/%d", uid, gid, selfUID, selfGID)
		}
	})

	t.Run("explicit uid:gid", func(t *testing.T) {
		uid, gid, _, err := resolveUser("4242:4243")
		if err != nil {
			t.Fatalf("resolveUser(4242:4243): %v", err)
		}
		if uid != 4242 || gid != 4243 {
			t.Errorf("got uid=%d gid=%d, want 4242/4243", uid, gid)
		}
	})

	t.Run("unknown uid without a group is rejected", func(t *testing.T) {
		// A uid absent from the user database has no primary group to infer, and
		// silently defaulting to gid 0 would hand the process root's group.
		if _, _, _, err := resolveUser("4242"); err == nil {
			t.Error("resolveUser(4242) accepted a uid with no database entry and no explicit gid")
		}
	})

	for _, bad := range []string{"", "   ", ":", "definitely-not-a-user-xyz", "-1", "root:definitely-not-a-group-xyz"} {
		if _, _, _, err := resolveUser(bad); err == nil {
			t.Errorf("resolveUser(%q) accepted an invalid spec", bad)
		}
	}
}

func TestSecurityValidate(t *testing.T) {
	self, _ := user.Current()

	valid := []Security{
		{},
		{NoNewPrivileges: true},
		{Capabilities: []string{}},
		{Capabilities: []string{"CAP_NET_BIND_SERVICE", "cap_net_raw"}},
		{User: self.Username, NoNewPrivileges: true, Capabilities: []string{"CAP_CHOWN"}},
		{User: "4242:4243"},
	}
	for _, s := range valid {
		if err := s.Validate(); err != nil {
			t.Errorf("Validate(%+v) errored: %v", s, err)
		}
	}

	invalid := []Security{
		{User: "definitely-not-a-user-xyz"},
		{Capabilities: []string{"CAP_NOT_A_THING"}},
		{User: "4242"}, // no group to infer
	}
	for _, s := range invalid {
		if err := s.Validate(); err == nil {
			t.Errorf("Validate(%+v) accepted an unusable restriction; it must fail at apply time, not at exec time", s)
		}
	}
}

func TestSecurityIsZeroAndDescribe(t *testing.T) {
	if !(Security{}).IsZero() {
		t.Error("the zero Security must be zero")
	}
	// An empty capability list is a real restriction ("none"), not an absent one.
	if (Security{Capabilities: []string{}}).IsZero() {
		t.Error("capabilities = [] is a restriction and must not read as zero")
	}
	if got := (Security{Capabilities: []string{}}).describe(); got != "capabilities=none" {
		t.Errorf("describe()=%q, want capabilities=none", got)
	}
	if got := (Security{User: "svc", NoNewPrivileges: true}).describe(); got != "user=svc,no_new_privileges=true" {
		t.Errorf("describe()=%q", got)
	}
}

// TestShimArgsRoundTrip is the contract between the two halves of the feature:
// whatever the runner renders, the shim must parse back to the same request.
func TestShimArgsRoundTrip(t *testing.T) {
	self, _ := user.Current()
	cases := []struct {
		name string
		sec  Security
	}{
		{"caps only", Security{Capabilities: []string{"CAP_NET_BIND_SERVICE"}}},
		{"drop every capability", Security{Capabilities: []string{}}},
		{"no new privileges only", Security{NoNewPrivileges: true}},
		{"everything", Security{User: self.Username, NoNewPrivileges: true, Capabilities: []string{"CAP_CHOWN", "CAP_BPF"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			argv, err := c.sec.shimArgs("/bin/true", []string{"--flag", "value with spaces"})
			if err != nil {
				t.Fatalf("shimArgs: %v", err)
			}
			if argv[0] != PrivdropFlag {
				t.Fatalf("argv[0]=%q, want %q", argv[0], PrivdropFlag)
			}
			got, err := parseShimArgs(argv[1:])
			if err != nil {
				t.Fatalf("the shim rejected its own arguments %v: %v", argv, err)
			}
			if got.command != "/bin/true" {
				t.Errorf("command=%q, want /bin/true", got.command)
			}
			if !reflect.DeepEqual(got.commandArgs, []string{"--flag", "value with spaces"}) {
				t.Errorf("commandArgs=%q, arguments must survive verbatim", got.commandArgs)
			}
			if got.noNewPrivs != c.sec.NoNewPrivileges {
				t.Errorf("noNewPrivs=%v, want %v", got.noNewPrivs, c.sec.NoNewPrivileges)
			}
			if got.capsRequested != (c.sec.Capabilities != nil) {
				t.Errorf("capsRequested=%v, want %v: an empty list must stay distinguishable from an absent one", got.capsRequested, c.sec.Capabilities != nil)
			}
			if len(got.capNames) != len(c.sec.Capabilities) {
				t.Errorf("caps=%v, want %d entries", got.capNames, len(c.sec.Capabilities))
			}
			if c.sec.User != "" && got.uid != os.Getuid() {
				t.Errorf("uid=%d, want %d", got.uid, os.Getuid())
			}
			if c.sec.User == "" && got.uid != -1 {
				t.Errorf("uid=%d with no user requested, want -1 (leave it alone)", got.uid)
			}
		})
	}
}

func TestParseShimArgsRejectsGarbage(t *testing.T) {
	for _, argv := range [][]string{
		{},                             // no command
		{"--no-new-privs"},             // still no command
		{"--uid=abc", "--", "/bin/sh"}, // unparseable uid
		{"--nonsense", "--", "/bin/sh"},
		{"--groups=1,x", "--", "/bin/sh"},
	} {
		if _, err := parseShimArgs(argv); err == nil {
			t.Errorf("parseShimArgs(%q) accepted malformed input", argv)
		}
	}
}
