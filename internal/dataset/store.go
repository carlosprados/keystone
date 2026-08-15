// Package dataset owns the on-disk life of a refreshable body of data: where
// each version lives, which one is current, and how many are kept.
//
// The layout is one directory per version plus a symlink:
//
//	runtime/datasets/<name>/2026-08-14/     ← kept: rollback target, delta base
//	runtime/datasets/<name>/2026-08-15/     ← the new one
//	runtime/datasets/<name>/current -> 2026-08-15
//
// The component reads through `current` and never learns a version number.
// Switching versions is a rename(2) over a temporary symlink in the same
// directory, which is atomic: no reader ever sees a half-written state.
//
// Two consequences of that, both real on an edge device. The tree must be on a
// single filesystem, because a cross-device rename fails — and a separate /var
// or an overlay is common. And a process holding the old file open keeps
// reading the old inode until it reopens, which is exactly why a reload hook
// exists.
package dataset

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// CurrentLink is the name of the symlink pointing at the active version.
const CurrentLink = "current"

// Store manages the dataset tree under a root directory.
type Store struct {
	root string
}

// NewStore returns a store rooted at dir (usually runtime/datasets).
//
// The root is made absolute here, once. Every path this store hands out ends up
// somewhere a relative path does not survive: a component's environment, and it
// runs with its own working directory, not the agent's. Getting that wrong
// gives the component a path that resolves to nothing, which reads exactly like
// an empty dataset.
func NewStore(dir string) *Store {
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	return &Store{root: dir}
}

// Root returns the tree root.
func (s *Store) Root() string { return s.root }

// Dir is where a dataset's versions live.
func (s *Store) Dir(name string) string { return filepath.Join(s.root, name) }

// VersionDir is where one version's files live.
func (s *Store) VersionDir(name, version string) string {
	return filepath.Join(s.root, name, version)
}

// Path is what the component reads: the stable symlink.
func (s *Store) Path(name string) string {
	return filepath.Join(s.root, name, CurrentLink)
}

// Active returns the version `current` points at, or "" when there is none.
func (s *Store) Active(name string) string {
	target, err := os.Readlink(s.Path(name))
	if err != nil {
		return ""
	}
	return filepath.Base(target)
}

// Prepare creates and returns an empty directory for a version, removing any
// half-finished attempt at the same one.
//
// Removing rather than reusing matters: a previous run may have been killed
// mid-extraction, and extracting over those files would produce a directory
// that is neither version.
func (s *Store) Prepare(name, version string) (string, error) {
	dir := s.VersionDir(name, version)
	if err := os.RemoveAll(dir); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// Activate points `current` at a version, atomically.
//
// The symlink is relative, so the tree can be moved or bind-mounted without
// every dataset breaking.
func (s *Store) Activate(name, version string) error {
	dir := s.VersionDir(name, version)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("cannot activate %s/%s: %w", name, version, err)
	}
	link := s.Path(name)
	tmp := link + ".tmp"

	_ = os.Remove(tmp)
	if err := os.Symlink(version, tmp); err != nil {
		return fmt.Errorf("cannot stage the symlink for %s: %w", name, err)
	}
	// rename(2) over an existing symlink is atomic within a directory: a reader
	// sees either the old target or the new one, never neither.
	if err := os.Rename(tmp, link); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("cannot activate %s/%s: %w", name, version, err)
	}
	return nil
}

// Versions lists the versions on disk, newest last by name order.
func (s *Store) Versions(name string) []string {
	entries, err := os.ReadDir(s.Dir(name))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() || e.Name() == CurrentLink {
			continue
		}
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

// Prune keeps the newest `keep` versions plus everything named in protect, and
// deletes the rest.
//
// protect exists because the active version is not always the newest one: after
// a rollback the newest directory is the version that failed, and deleting the
// one actually in use would leave the component reading a dangling symlink.
func (s *Store) Prune(name string, keep int, protect ...string) error {
	if keep < 1 {
		keep = 1
	}
	versions := s.Versions(name)
	if len(versions) <= keep {
		return nil
	}

	protected := map[string]bool{}
	for _, p := range protect {
		if p != "" {
			protected[p] = true
		}
	}
	if active := s.Active(name); active != "" {
		protected[active] = true
	}

	// Delete oldest first, stopping once few enough remain.
	remaining := len(versions)
	var firstErr error
	for _, v := range versions {
		if remaining <= keep {
			break
		}
		if protected[v] {
			continue
		}
		if err := os.RemoveAll(s.VersionDir(name, v)); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		remaining--
	}
	return firstErr
}

// EnvName is the environment variable a component reads to find a dataset:
// KEYSTONE_DATASET_<NAME>, upper-cased, with anything that is not a letter or
// digit turned into an underscore.
func EnvName(name string) string {
	var b strings.Builder
	b.WriteString("KEYSTONE_DATASET_")
	for _, r := range strings.ToUpper(name) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}
