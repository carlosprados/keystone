//go:build linux

package runner

import (
	"fmt"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

// capabilityNumbers maps the capability names an operator writes in a recipe to
// the kernel's numbers. The names are the same ones systemd and `capsh` use, so
// an existing unit's AmbientCapabilities can be copied across verbatim.
var capabilityNumbers = map[string]uintptr{
	"CAP_CHOWN":              unix.CAP_CHOWN,
	"CAP_DAC_OVERRIDE":       unix.CAP_DAC_OVERRIDE,
	"CAP_DAC_READ_SEARCH":    unix.CAP_DAC_READ_SEARCH,
	"CAP_FOWNER":             unix.CAP_FOWNER,
	"CAP_FSETID":             unix.CAP_FSETID,
	"CAP_KILL":               unix.CAP_KILL,
	"CAP_SETGID":             unix.CAP_SETGID,
	"CAP_SETUID":             unix.CAP_SETUID,
	"CAP_SETPCAP":            unix.CAP_SETPCAP,
	"CAP_LINUX_IMMUTABLE":    unix.CAP_LINUX_IMMUTABLE,
	"CAP_NET_BIND_SERVICE":   unix.CAP_NET_BIND_SERVICE,
	"CAP_NET_BROADCAST":      unix.CAP_NET_BROADCAST,
	"CAP_NET_ADMIN":          unix.CAP_NET_ADMIN,
	"CAP_NET_RAW":            unix.CAP_NET_RAW,
	"CAP_IPC_LOCK":           unix.CAP_IPC_LOCK,
	"CAP_IPC_OWNER":          unix.CAP_IPC_OWNER,
	"CAP_SYS_MODULE":         unix.CAP_SYS_MODULE,
	"CAP_SYS_RAWIO":          unix.CAP_SYS_RAWIO,
	"CAP_SYS_CHROOT":         unix.CAP_SYS_CHROOT,
	"CAP_SYS_PTRACE":         unix.CAP_SYS_PTRACE,
	"CAP_SYS_PACCT":          unix.CAP_SYS_PACCT,
	"CAP_SYS_ADMIN":          unix.CAP_SYS_ADMIN,
	"CAP_SYS_BOOT":           unix.CAP_SYS_BOOT,
	"CAP_SYS_NICE":           unix.CAP_SYS_NICE,
	"CAP_SYS_RESOURCE":       unix.CAP_SYS_RESOURCE,
	"CAP_SYS_TIME":           unix.CAP_SYS_TIME,
	"CAP_SYS_TTY_CONFIG":     unix.CAP_SYS_TTY_CONFIG,
	"CAP_MKNOD":              unix.CAP_MKNOD,
	"CAP_LEASE":              unix.CAP_LEASE,
	"CAP_AUDIT_WRITE":        unix.CAP_AUDIT_WRITE,
	"CAP_AUDIT_CONTROL":      unix.CAP_AUDIT_CONTROL,
	"CAP_SETFCAP":            unix.CAP_SETFCAP,
	"CAP_MAC_OVERRIDE":       unix.CAP_MAC_OVERRIDE,
	"CAP_MAC_ADMIN":          unix.CAP_MAC_ADMIN,
	"CAP_SYSLOG":             unix.CAP_SYSLOG,
	"CAP_WAKE_ALARM":         unix.CAP_WAKE_ALARM,
	"CAP_BLOCK_SUSPEND":      unix.CAP_BLOCK_SUSPEND,
	"CAP_AUDIT_READ":         unix.CAP_AUDIT_READ,
	"CAP_PERFMON":            unix.CAP_PERFMON,
	"CAP_BPF":                unix.CAP_BPF,
	"CAP_CHECKPOINT_RESTORE": unix.CAP_CHECKPOINT_RESTORE,
}

// lastKnownCapability is the highest capability number this build knows about.
// Iterating past it is harmless (the kernel answers EINVAL) but pointless.
const lastKnownCapability = uintptr(unix.CAP_CHECKPOINT_RESTORE)

// capabilityByName resolves a recipe-provided capability name. Both "CAP_NET_RAW"
// and "net_raw" are accepted, case-insensitively.
func capabilityByName(name string) (uintptr, error) {
	key := strings.ToUpper(strings.TrimSpace(name))
	if key == "" {
		return 0, fmt.Errorf("empty capability name")
	}
	if !strings.HasPrefix(key, "CAP_") {
		key = "CAP_" + key
	}
	c, ok := capabilityNumbers[key]
	if !ok {
		return 0, fmt.Errorf("unknown capability %q (see `man 7 capabilities` for the list this build knows)", name)
	}
	return c, nil
}

// capabilityName is the inverse of capabilityByName, for log and error messages.
func capabilityName(c uintptr) string {
	for name, n := range capabilityNumbers {
		if n == c {
			return name
		}
	}
	return fmt.Sprintf("CAP_%d", c)
}

// capabilityNames renders a set of capability numbers in a stable order.
func capabilityNames(caps []uintptr) []string {
	out := make([]string, 0, len(caps))
	for _, c := range caps {
		out = append(out, capabilityName(c))
	}
	sort.Strings(out)
	return out
}
