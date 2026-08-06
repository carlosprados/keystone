//go:build linux

package runner

import (
	"fmt"
	"log"
	"os"
	"os/user"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// PrivdropFlag is the hidden argv[1] that turns the keystone binary into the
// privilege-dropping shim. See RunPrivdropShim.
const PrivdropFlag = "--privdrop-exec"

// Security describes the privilege restrictions to apply to a process
// component before it is executed. The zero value means "inherit everything
// from the agent", which is what happens without a [lifecycle.run.security]
// block in the recipe.
type Security struct {
	// User is "user", "uid", "user:group" or "uid:gid". An empty value keeps
	// the agent's own uid/gid.
	User string
	// NoNewPrivileges sets PR_SET_NO_NEW_PRIVS, so the process and its children
	// can never gain privileges through execve (setuid binaries, file
	// capabilities).
	NoNewPrivileges bool
	// Capabilities is the allow-list of capability names ("CAP_NET_BIND_SERVICE").
	// When non-nil, every capability outside the list is dropped from the
	// bounding set, so the process can never acquire it. A non-nil empty slice
	// therefore means "no capabilities at all".
	Capabilities []string
}

// IsZero reports whether nothing was requested.
func (s Security) IsZero() bool {
	return s.User == "" && !s.NoNewPrivileges && s.Capabilities == nil
}

// describe renders the restrictions for the log line that records them, so an
// operator can see in the journal what a component was actually confined to.
func (s Security) describe() string {
	parts := make([]string, 0, 3)
	if s.User != "" {
		parts = append(parts, "user="+s.User)
	}
	if s.NoNewPrivileges {
		parts = append(parts, "no_new_privileges=true")
	}
	if s.Capabilities != nil {
		if len(s.Capabilities) == 0 {
			parts = append(parts, "capabilities=none")
		} else {
			parts = append(parts, "capabilities="+strings.Join(s.Capabilities, "+"))
		}
	}
	return strings.Join(parts, ",")
}

// resolvedSecurity is Security with names turned into numbers, ready to apply.
type resolvedSecurity struct {
	uid, gid        int
	changeUser      bool
	groups          []int
	noNewPrivileges bool
	caps            []uintptr
	capsSet         bool
}

// Validate resolves and checks everything that can be checked without applying
// it, so a bad recipe fails when the plan is applied rather than at exec time.
func (s Security) Validate() error {
	_, err := s.resolve()
	return err
}

func (s Security) resolve() (resolvedSecurity, error) {
	var out resolvedSecurity
	out.noNewPrivileges = s.NoNewPrivileges

	if s.User != "" {
		uid, gid, groups, err := resolveUser(s.User)
		if err != nil {
			return out, err
		}
		out.uid, out.gid, out.groups, out.changeUser = uid, gid, groups, true
	}

	if s.Capabilities != nil {
		out.capsSet = true
		out.caps = make([]uintptr, 0, len(s.Capabilities))
		seen := map[uintptr]bool{}
		for _, name := range s.Capabilities {
			c, err := capabilityByName(name)
			if err != nil {
				return out, err
			}
			if seen[c] {
				continue
			}
			seen[c] = true
			out.caps = append(out.caps, c)
		}
	}
	return out, nil
}

// resolveUser turns "user", "uid", "user:group" or "uid:gid" into ids. When only
// a user is given, its primary group and supplementary groups are used, which is
// what an operator expects from `User=` in a systemd unit.
func resolveUser(spec string) (uid, gid int, groups []int, err error) {
	userPart, groupPart, hasGroup := strings.Cut(spec, ":")
	userPart = strings.TrimSpace(userPart)
	groupPart = strings.TrimSpace(groupPart)
	if userPart == "" {
		return 0, 0, nil, fmt.Errorf("security.user %q: empty user", spec)
	}

	var u *user.User
	if n, convErr := strconv.Atoi(userPart); convErr == nil {
		if n < 0 {
			return 0, 0, nil, fmt.Errorf("security.user %q: negative uid", spec)
		}
		uid = n
		// Look the uid up for its groups; not being in /etc/passwd is allowed.
		u, _ = user.LookupId(userPart)
	} else {
		u, err = user.Lookup(userPart)
		if err != nil {
			return 0, 0, nil, fmt.Errorf("security.user %q: unknown user %q: %w", spec, userPart, err)
		}
		if uid, err = strconv.Atoi(u.Uid); err != nil {
			return 0, 0, nil, fmt.Errorf("security.user %q: unusable uid %q: %w", spec, u.Uid, err)
		}
	}

	switch {
	case hasGroup && groupPart != "":
		if n, convErr := strconv.Atoi(groupPart); convErr == nil {
			if n < 0 {
				return 0, 0, nil, fmt.Errorf("security.user %q: negative gid", spec)
			}
			gid = n
		} else {
			g, lookupErr := user.LookupGroup(groupPart)
			if lookupErr != nil {
				return 0, 0, nil, fmt.Errorf("security.user %q: unknown group %q: %w", spec, groupPart, lookupErr)
			}
			if gid, err = strconv.Atoi(g.Gid); err != nil {
				return 0, 0, nil, fmt.Errorf("security.user %q: unusable gid %q: %w", spec, g.Gid, err)
			}
		}
	case u != nil:
		if gid, err = strconv.Atoi(u.Gid); err != nil {
			return 0, 0, nil, fmt.Errorf("security.user %q: unusable primary gid %q: %w", spec, u.Gid, err)
		}
	default:
		return 0, 0, nil, fmt.Errorf("security.user %q: uid %d is not in the user database, so it has no primary group: give it explicitly as \"uid:gid\"", spec, uid)
	}

	// Supplementary groups, best effort: only available for a known user.
	if u != nil {
		if ids, gerr := u.GroupIds(); gerr == nil {
			for _, s := range ids {
				if n, convErr := strconv.Atoi(s); convErr == nil && n != gid {
					groups = append(groups, n)
				}
			}
		}
	}
	return uid, gid, groups, nil
}

// shimArgs renders the resolved restrictions as the argv of the shim, followed
// by the command to execute.
func (s Security) shimArgs(command string, args []string) ([]string, error) {
	res, err := s.resolve()
	if err != nil {
		return nil, err
	}
	out := []string{PrivdropFlag}
	if res.changeUser {
		out = append(out, "--uid="+strconv.Itoa(res.uid), "--gid="+strconv.Itoa(res.gid))
		if len(res.groups) > 0 {
			gs := make([]string, 0, len(res.groups))
			for _, g := range res.groups {
				gs = append(gs, strconv.Itoa(g))
			}
			out = append(out, "--groups="+strings.Join(gs, ","))
		}
	}
	if res.noNewPrivileges {
		out = append(out, "--no-new-privs")
	}
	if res.capsSet {
		names := make([]string, 0, len(res.caps))
		for _, c := range res.caps {
			names = append(names, capabilityName(c))
		}
		// Always present when capabilities are restricted, even if empty: that
		// is how the shim tells "drop everything" from "not requested".
		out = append(out, "--caps="+strings.Join(names, ","))
	}
	out = append(out, "--", command)
	return append(out, args...), nil
}

// RunPrivdropShim is the entry point of the privilege-dropping shim: the agent
// re-executes its own binary with PrivdropFlag, the shim reduces its own
// privileges, verifies the result, and only then execs the real command. It
// never returns on success — the process image is replaced, so the PID the agent
// supervises stays the same.
//
// Doing this in a shim rather than in the parent is not a detour: dropping the
// capability bounding set and setting PR_SET_NO_NEW_PRIVS are per-process
// operations that would otherwise apply to the agent itself and be inherited by
// everything it starts, and they are irreversible.
//
// Order matters and follows what systemd does: keep capabilities across the uid
// change, switch group then user, restrict capabilities, forbid regaining
// privileges, exec.
func RunPrivdropShim(argv []string) error {
	req, err := parseShimArgs(argv)
	if err != nil {
		return err
	}

	var caps []uintptr
	for _, n := range req.capNames {
		c, err := capabilityByName(n)
		if err != nil {
			return fmt.Errorf("privdrop: %w", err)
		}
		caps = append(caps, c)
	}

	if err := applyPrivdrop(req.uid, req.gid, req.groups, caps, req.capsRequested, req.noNewPrivs); err != nil {
		return err
	}

	return unix.Exec(req.command, append([]string{req.command}, req.commandArgs...), os.Environ())
}

// shimRequest is what the shim was asked to do, as parsed from its argv.
type shimRequest struct {
	uid, gid      int
	groups        []int
	noNewPrivs    bool
	capsRequested bool
	capNames      []string
	command       string
	commandArgs   []string
}

// parseShimArgs reads the argv the runner rendered. Anything unrecognised is an
// error rather than something to skip: this argv decides how confined a process
// runs, so a typo must stop the component, not weaken it.
func parseShimArgs(argv []string) (shimRequest, error) {
	req := shimRequest{uid: -1, gid: -1}
	sawSentinel := false

	for _, arg := range argv {
		if sawSentinel {
			if req.command == "" {
				req.command = arg
			} else {
				req.commandArgs = append(req.commandArgs, arg)
			}
			continue
		}
		switch {
		case arg == "--":
			sawSentinel = true
		case arg == "--no-new-privs":
			req.noNewPrivs = true
		case strings.HasPrefix(arg, "--uid="):
			n, err := strconv.Atoi(strings.TrimPrefix(arg, "--uid="))
			if err != nil {
				return req, fmt.Errorf("privdrop: bad --uid: %w", err)
			}
			req.uid = n
		case strings.HasPrefix(arg, "--gid="):
			n, err := strconv.Atoi(strings.TrimPrefix(arg, "--gid="))
			if err != nil {
				return req, fmt.Errorf("privdrop: bad --gid: %w", err)
			}
			req.gid = n
		case strings.HasPrefix(arg, "--groups="):
			for _, s := range strings.Split(strings.TrimPrefix(arg, "--groups="), ",") {
				if s == "" {
					continue
				}
				n, err := strconv.Atoi(s)
				if err != nil {
					return req, fmt.Errorf("privdrop: bad --groups entry %q: %w", s, err)
				}
				req.groups = append(req.groups, n)
			}
		case strings.HasPrefix(arg, "--caps="):
			req.capsRequested = true
			for _, s := range strings.Split(strings.TrimPrefix(arg, "--caps="), ",") {
				if s = strings.TrimSpace(s); s != "" {
					req.capNames = append(req.capNames, s)
				}
			}
		default:
			return req, fmt.Errorf("privdrop: unknown argument %q", arg)
		}
	}
	if req.command == "" {
		return req, fmt.Errorf("privdrop: no command to execute")
	}
	return req, nil
}

func applyPrivdrop(uid, gid int, groups []int, caps []uintptr, capsRequested, noNewPrivs bool) error {
	boundingSetNarrowed := false
	if capsRequested {
		// Keep the permitted set across the uid change; without this, switching
		// away from uid 0 clears all capabilities and the allow-list could not
		// be honoured.
		if len(caps) > 0 {
			if err := unix.Prctl(unix.PR_SET_KEEPCAPS, 1, 0, 0, 0); err != nil {
				return fmt.Errorf("privdrop: PR_SET_KEEPCAPS: %w", err)
			}
		}
		// The bounding set must be closed *before* the identity switch: doing so
		// needs CAP_SETPCAP in the effective set, and setuid clears the
		// effective set.
		var err error
		if boundingSetNarrowed, err = dropBoundingSet(caps, noNewPrivs); err != nil {
			return err
		}
	}

	// Switching identity is skipped when the target is the identity we already
	// have: a recipe that says user = "svc" must work both under a root agent
	// (which switches) and under an agent already running as svc (which has
	// nothing to switch and no privilege to call setgroups with).
	changingUser := (uid >= 0 && uid != unix.Getuid()) || (gid >= 0 && gid != unix.Getgid())
	if changingUser {
		// Always set the supplementary groups, including to the empty set:
		// inheriting the agent's groups (root's, typically) would leave the
		// component with access the recipe did not ask for.
		if err := unix.Setgroups(groups); err != nil {
			return fmt.Errorf("privdrop: setgroups %v: %w", groups, err)
		}
		if gid >= 0 {
			if err := unix.Setgid(gid); err != nil {
				return fmt.Errorf("privdrop: setgid %d: %w", gid, err)
			}
		}
		if uid >= 0 {
			if err := unix.Setuid(uid); err != nil {
				return fmt.Errorf("privdrop: setuid %d: %w", uid, err)
			}
		}
	}

	if capsRequested {
		if err := setCapabilities(caps); err != nil {
			return err
		}
	}

	if noNewPrivs {
		if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
			return fmt.Errorf("privdrop: PR_SET_NO_NEW_PRIVS: %w", err)
		}
	}

	return verifyPrivdrop(uid, gid, caps, capsRequested, boundingSetNarrowed, noNewPrivs)
}

// capMask renders capability numbers as the two 32-bit words the capset ABI and
// /proc/<pid>/status use.
func capMask(caps []uintptr) [2]uint32 {
	var mask [2]uint32
	for _, c := range caps {
		if c < 32 {
			mask[0] |= 1 << c
		} else {
			mask[1] |= 1 << (c - 32)
		}
	}
	return mask
}

// dropBoundingSet strips every capability outside the allow-list from the
// bounding set, so nothing outside it can ever be acquired by this process or its
// children. It must run before the identity switch: PR_CAPBSET_DROP needs
// CAP_SETPCAP in the *effective* set, and setuid clears the effective set.
//
// It reports whether the bounding set was actually narrowed. Narrowing it
// requires CAP_SETPCAP, which an agent that is not root does not have. That is
// not fatal *if* no_new_privileges is also being set: the bounding set only
// matters when execve would otherwise grant capabilities (a setuid binary, or one
// carrying file capabilities), and PR_SET_NO_NEW_PRIVS forbids exactly that.
// Without no_new_privileges the hole is real, so this fails instead.
func dropBoundingSet(caps []uintptr, noNewPrivs bool) (narrowed bool, err error) {
	allowed := make(map[uintptr]bool, len(caps))
	for _, c := range caps {
		allowed[c] = true
	}
	for c := uintptr(0); c <= lastKnownCapability; c++ {
		if allowed[c] {
			continue
		}
		switch err := unix.Prctl(unix.PR_CAPBSET_DROP, c, 0, 0, 0); {
		case err == nil, err == unix.EINVAL: // EINVAL: unknown to this kernel
			continue
		case err == unix.EPERM && noNewPrivs:
			log.Printf("[runner] privdrop: cannot narrow the capability bounding set without CAP_SETPCAP; no_new_privileges=true already prevents gaining capabilities through execve, continuing")
			return false, nil
		case err == unix.EPERM:
			return false, fmt.Errorf("privdrop: cannot narrow the capability bounding set (%s): CAP_SETPCAP is required. Either run the agent as root or with CAP_SETPCAP, or add no_new_privileges = true, which closes the same hole without it", capabilityName(c))
		default:
			return false, fmt.Errorf("privdrop: PR_CAPBSET_DROP %s: %w", capabilityName(c), err)
		}
	}
	return true, nil
}

// setCapabilities narrows the permitted, effective and inheritable sets to the
// allow-list and raises those capabilities into the ambient set. The ambient set
// is what makes them survive the execve of an ordinary binary — one without file
// capabilities, which is the normal case for a component — so skipping it would
// leave the process with nothing at all.
//
// It must run after the identity switch: setuid clears the effective set (and,
// without PR_SET_KEEPCAPS, the permitted set too).
func setCapabilities(caps []uintptr) error {
	mask := capMask(caps)
	hdr := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3, Pid: 0}
	data := [2]unix.CapUserData{
		{Effective: mask[0], Permitted: mask[0], Inheritable: mask[0]},
		{Effective: mask[1], Permitted: mask[1], Inheritable: mask[1]},
	}
	if err := unix.Capset(&hdr, &data[0]); err != nil {
		if err == unix.EPERM {
			// A process can only ever narrow its own capabilities, so this means
			// the agent does not hold what the recipe asks to grant.
			return fmt.Errorf("privdrop: cannot grant %s: the keystone agent does not hold it in its own permitted set (run the agent with that capability, or remove it from [lifecycle.run.security])",
				strings.Join(capabilityNames(caps), "+"))
		}
		return fmt.Errorf("privdrop: capset to %v: %w", capabilityNames(caps), err)
	}

	for _, c := range caps {
		if err := unix.Prctl(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_RAISE, c, 0, 0); err != nil {
			return fmt.Errorf("privdrop: PR_CAP_AMBIENT_RAISE %s (without it the capability would not survive exec): %w", capabilityName(c), err)
		}
	}
	return nil
}

// verifyPrivdrop refuses to exec if the restrictions did not actually take
// effect. Fail-closed: a component whose confinement silently did not apply
// must not run, which is the whole complaint behind this feature.
func verifyPrivdrop(uid, gid int, caps []uintptr, capsRequested, boundingSetNarrowed, noNewPrivs bool) error {
	if uid >= 0 {
		if got := unix.Getuid(); got != uid {
			return fmt.Errorf("privdrop: uid is %d after setuid(%d)", got, uid)
		}
		if got := unix.Geteuid(); got != uid {
			return fmt.Errorf("privdrop: euid is %d after setuid(%d)", got, uid)
		}
	}
	if gid >= 0 {
		if got := unix.Getgid(); got != gid {
			return fmt.Errorf("privdrop: gid is %d after setgid(%d)", got, gid)
		}
	}
	if noNewPrivs {
		got, err := unix.PrctlRetInt(unix.PR_GET_NO_NEW_PRIVS, 0, 0, 0, 0)
		if err != nil {
			return fmt.Errorf("privdrop: PR_GET_NO_NEW_PRIVS: %w", err)
		}
		if got != 1 {
			return fmt.Errorf("privdrop: no_new_privileges was requested but PR_GET_NO_NEW_PRIVS is %d", got)
		}
	}
	if capsRequested {
		allowed := make(map[uintptr]bool, len(caps))
		var want [2]uint32
		for _, c := range caps {
			allowed[c] = true
			if c < 32 {
				want[0] |= 1 << c
			} else {
				want[1] |= 1 << (c - 32)
			}
		}

		// What the process actually holds now.
		hdr := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3, Pid: 0}
		var got [2]unix.CapUserData
		if err := unix.Capget(&hdr, &got[0]); err != nil {
			return fmt.Errorf("privdrop: capget to verify the result: %w", err)
		}
		for i := range got {
			if got[i].Effective != want[i] || got[i].Permitted != want[i] {
				return fmt.Errorf("privdrop: capabilities are %s after restricting them to %s",
					describeCapMask(got[0].Effective, got[1].Effective), strings.Join(capabilityNames(caps), "+"))
			}
		}

		// The ambient set is the one that matters after this: exec keeps ambient
		// capabilities and drops everything else. Checking only permitted and
		// effective would pass while the component ends up with none.
		for _, c := range caps {
			set, err := unix.PrctlRetInt(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_IS_SET, c, 0, 0)
			if err != nil {
				return fmt.Errorf("privdrop: PR_CAP_AMBIENT_IS_SET %s: %w", capabilityName(c), err)
			}
			if set != 1 {
				return fmt.Errorf("privdrop: %s is not in the ambient set, so it would not survive exec", capabilityName(c))
			}
		}

		// The bounding set is only asserted when it could be narrowed; when it
		// could not, no_new_privileges is what closes the hole, and that was
		// verified above.
		if boundingSetNarrowed {
			for c := uintptr(0); c <= lastKnownCapability; c++ {
				if allowed[c] {
					continue
				}
				bit, err := unix.PrctlRetInt(unix.PR_CAPBSET_READ, c, 0, 0, 0)
				if err != nil {
					// Unknown capability on this kernel.
					continue
				}
				if bit == 1 {
					return fmt.Errorf("privdrop: %s is still in the bounding set after dropping it", capabilityName(c))
				}
			}
		} else if !noNewPrivs {
			return fmt.Errorf("privdrop: the capability bounding set was not narrowed and no_new_privileges is off, so capabilities could still be gained through execve")
		}
	}
	return nil
}

// describeCapMask renders a capability bitmask for error messages.
func describeCapMask(low, high uint32) string {
	var caps []uintptr
	for c := uintptr(0); c <= lastKnownCapability; c++ {
		set := false
		if c < 32 {
			set = low&(1<<c) != 0
		} else {
			set = high&(1<<(c-32)) != 0
		}
		if set {
			caps = append(caps, c)
		}
	}
	if len(caps) == 0 {
		return "none"
	}
	return strings.Join(capabilityNames(caps), "+")
}
