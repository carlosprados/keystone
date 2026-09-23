// Package selfupdate installs a new agent binary beside the running one and
// switches between them atomically.
//
// It does not decide *when* to update, and it does not restart anything. Those
// belong to the supervisor that owns the process — see docs/self-update-design.md
// for why the rollback decision cannot live in the binary being replaced.
package selfupdate

import (
	"debug/elf"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// Layout is the on-disk arrangement of installed versions.
//
//	<root>/versions/v0.12.0/keystone   an installed build
//	<root>/current -> versions/v0.12.0 the one that runs
//	<root>/staging/                    downloads in progress
//
// Versions live in their own directories and the switch is a symlink rename,
// because writing over a running binary returns ETXTBSY while replacing the
// path it is reached through does not. It also means a power cut leaves either
// the old version or the new one and never half of one, which matters on
// hardware whose power supply is already known to be unreliable.
type Layout struct {
	Root string
}

// BinaryName is the file installed inside each version directory.
const BinaryName = "keystone"

func (l Layout) VersionsDir() string        { return filepath.Join(l.Root, "versions") }
func (l Layout) StagingDir() string         { return filepath.Join(l.Root, "staging") }
func (l Layout) CurrentLink() string        { return filepath.Join(l.Root, "current") }
func (l Layout) VersionDir(v string) string { return filepath.Join(l.VersionsDir(), v) }

// BinaryPath is where the binary for a version lives once installed.
func (l Layout) BinaryPath(v string) string {
	return filepath.Join(l.VersionDir(v), BinaryName)
}

// Prepare creates the directories. It does not create the symlink: pointing
// `current` at nothing is worse than its absence, which is at least obviously
// an uninitialised install.
func (l Layout) Prepare() error {
	for _, d := range []string{l.VersionsDir(), l.StagingDir()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", d, err)
		}
	}
	return nil
}

// Current reports the version `current` points at, or "" when there is no
// symlink yet.
func (l Layout) Current() (string, error) {
	target, err := os.Readlink(l.CurrentLink())
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return filepath.Base(target), nil
}

// Installed lists the versions present on disk, sorted.
//
// Sorted lexically on purpose: this is used for reporting and for pruning by
// recency, and inventing a version ordering here would mean agreeing with
// whatever scheme the operator used. Callers that need "newest" use the
// symlink or the install order, not this.
func (l Layout) Installed() ([]string, error) {
	entries, err := os.ReadDir(l.VersionsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// Install places a verified binary as version v.
//
// The caller has already checked the signature: this function moves bytes, it
// does not decide whether to trust them. It refuses to overwrite an existing
// version, because a version number that silently means different things on
// different devices destroys the only handle an operator has on a fleet.
func (l Layout) Install(verifiedBinary, v string) error {
	if err := validVersionName(v); err != nil {
		return err
	}
	if err := l.Prepare(); err != nil {
		return err
	}

	dir := l.VersionDir(v)
	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("version %s is already installed", v)
	}

	if err := CheckArchitecture(verifiedBinary); err != nil {
		return err
	}

	// Build the version directory under a temporary name and rename it into
	// place, so an interrupted install never leaves a half-populated version
	// directory that looks installed.
	staging, err := os.MkdirTemp(l.VersionsDir(), ".incoming-*")
	if err != nil {
		return fmt.Errorf("create staging dir: %w", err)
	}
	defer os.RemoveAll(staging)

	dst := filepath.Join(staging, BinaryName)
	if err := copyExecutable(verifiedBinary, dst); err != nil {
		return err
	}
	if err := os.Rename(staging, dir); err != nil {
		return fmt.Errorf("install version %s: %w", v, err)
	}
	return nil
}

// Activate points `current` at v.
//
// Done with a temporary symlink and a rename because os.Symlink cannot replace
// an existing link: removing and recreating leaves a window where `current`
// does not exist, and a restart landing in that window has nothing to start.
func (l Layout) Activate(v string) error {
	if err := validVersionName(v); err != nil {
		return err
	}
	if _, err := os.Stat(l.BinaryPath(v)); err != nil {
		return fmt.Errorf("cannot activate %s: %w", v, err)
	}

	tmp := l.CurrentLink() + ".tmp"
	_ = os.Remove(tmp)
	if err := os.Symlink(filepath.Join("versions", v), tmp); err != nil {
		return fmt.Errorf("create symlink: %w", err)
	}
	if err := os.Rename(tmp, l.CurrentLink()); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("activate %s: %w", v, err)
	}
	return nil
}

// Prune removes installed versions, keeping the ones named in keep.
//
// The caller names what to keep rather than passing a count: "keep the newest
// two" needs a version ordering this package deliberately does not invent, and
// the two that actually matter are the running one and the one to fall back
// to — which the caller knows and a sort does not.
func (l Layout) Prune(keep ...string) ([]string, error) {
	kept := map[string]bool{}
	for _, k := range keep {
		kept[k] = true
	}

	installed, err := l.Installed()
	if err != nil {
		return nil, err
	}

	var removed []string
	for _, v := range installed {
		if kept[v] {
			continue
		}
		if err := os.RemoveAll(l.VersionDir(v)); err != nil {
			return removed, fmt.Errorf("remove %s: %w", v, err)
		}
		removed = append(removed, v)
	}
	return removed, nil
}

// CheckArchitecture refuses a binary built for a different machine.
//
// Without it, the wrong build for the board is indistinguishable from a corrupt
// one: both fail to execute, and both are discovered only after the swap, by
// burning restarts against the boot counter. This is knowable beforehand, from
// the ELF header, in microseconds.
func CheckArchitecture(path string) error {
	f, err := elf.Open(path)
	if err != nil {
		return fmt.Errorf("not a usable ELF binary: %w", err)
	}
	defer f.Close()

	want, ok := archToELFMachine[runtime.GOARCH]
	if !ok {
		// An architecture this check does not know about: better to install and
		// let the boot counter catch a mistake than to refuse a valid binary
		// because the table is incomplete.
		return nil
	}
	if f.Machine != want {
		return fmt.Errorf("binary is for %s, this device is %s", elfMachineName(f.Machine), runtime.GOARCH)
	}
	return nil
}

var archToELFMachine = map[string]elf.Machine{
	"amd64":   elf.EM_X86_64,
	"arm64":   elf.EM_AARCH64,
	"arm":     elf.EM_ARM,
	"386":     elf.EM_386,
	"riscv64": elf.EM_RISCV,
}

func elfMachineName(m elf.Machine) string {
	for arch, machine := range archToELFMachine {
		if machine == m {
			return arch
		}
	}
	return m.String()
}

// validVersionName keeps a version out of the filesystem's way. It is used as a
// directory name and as a symlink target, so a separator or a traversal in it
// would write outside the layout entirely.
func validVersionName(v string) error {
	if strings.TrimSpace(v) == "" {
		return fmt.Errorf("empty version name")
	}
	if v != filepath.Base(v) || v == "." || v == ".." {
		return fmt.Errorf("invalid version name %q", v)
	}
	if strings.ContainsAny(v, `/\:`) {
		return fmt.Errorf("invalid version name %q", v)
	}
	return nil
}

func copyExecutable(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read %s: %w", src, err)
	}
	if err := os.WriteFile(dst, b, 0o755); err != nil {
		return fmt.Errorf("write %s: %w", dst, err)
	}
	return nil
}
