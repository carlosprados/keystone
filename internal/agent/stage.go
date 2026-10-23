package agent

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/carlosprados/keystone/internal/artifact"
)

// linkArtifact is os.Link, behind a variable so the tests can make linking fail
// the way a workdir on another filesystem does.
var linkArtifact = os.Link

// stagedCacheName is the subdirectory of a version's cache that holds the
// cached copy of every artifact staged into the workdir — the file the workdir
// links to.
//
// It is not where the cache used to keep them, on purpose. An agent from before
// linking stages by opening the workdir file with O_TRUNC and copying its cached
// file into it. Were that cached file at the path such an agent knows, it would
// be the very file the workdir names: truncating the destination would empty
// the source, and the copy would read nothing. Every staged artifact would be
// left at 0 bytes the moment a device went back to an older agent — which is
// what the self-update gate does, unasked, when a new version fails. Kept here,
// an older agent finds no cached copy, downloads its own, and its copy writes
// the same bytes into the shared file.
const stagedCacheName = ".staged"

// ensureStagedArtifact is artifact.Ensure for an artifact that is staged into
// the workdir: its cached copy lives under stagedCacheName.
//
// A copy an older agent cached at the top of the version's directory is moved
// there, so the first start after an upgrade costs a rename and not a download;
// its digest is checked by the Ensure that follows, as any cached copy's is.
// If the staged copy already exists, the older one is a second copy — an older
// agent ran in between and downloaded it again — and is removed.
//
// Either way, the path an older agent knows ends up naming nothing, and its
// entry goes from the index at the top of the version. Going back to one costs
// that agent a download of every staged artifact: their sources have to be
// reachable for it to start its components.
func ensureStagedArtifact(artDir, uri, sha string, timeout time.Duration, httpOpts artifact.HTTPOptions) (string, error) {
	stagedDir := filepath.Join(artDir, stagedCacheName)
	older, hasOlder := olderCachedCopy(artDir, uri)
	if hasOlder && !hasCachedCopy(stagedDir, uri) {
		moveCachedCopy(older, artDir, stagedDir)
	}
	path, _, err := artifact.Ensure(stagedDir, uri, sha, timeout, httpOpts)
	if err != nil {
		return "", err
	}
	if hasOlder {
		if err := os.Remove(older.Path); err == nil {
			log.Printf("[artifact] removed %s, a second cached copy left by an older agent", older.Path)
			forgetCachedCopy(artDir, uri)
		}
	}
	return path, nil
}

// olderCachedCopy returns the entry an agent from before linking left for uri
// in the index at the top of the version's cache, if its file is still there.
// Only a file in that directory counts: nothing else is this cache's to move.
func olderCachedCopy(artDir, uri string) (artifact.IndexEntry, bool) {
	idx, _ := artifact.LoadIndex(artDir)
	e, ok := idx.Get(uri)
	if !ok || filepath.Dir(e.Path) != filepath.Clean(artDir) {
		return e, false
	}
	fi, err := os.Lstat(e.Path)
	return e, err == nil && fi.Mode().IsRegular()
}

// hasCachedCopy reports whether the index in dir names a file for uri that is
// still there. Whether it verifies is Ensure's to find out.
func hasCachedCopy(dir, uri string) bool {
	idx, _ := artifact.LoadIndex(dir)
	e, ok := idx.Get(uri)
	if !ok {
		return false
	}
	_, err := os.Stat(e.Path)
	return err == nil
}

// moveCachedCopy renames an older agent's cached copy from artDir into dir and
// indexes it there. A failure only costs the download it was meant to save, and
// leaves the copy and its entry where they were.
//
// The entry in artDir's index is dropped once the file has moved, never
// before. An agent cut off in between leaves an entry naming nothing, which
// Ensure stats and takes for no cached copy at all; the other order would leave
// a whole artifact in artDir that no index names.
func moveCachedCopy(older artifact.IndexEntry, artDir, dir string) {
	dst := filepath.Join(dir, filepath.Base(older.Path))
	err := os.MkdirAll(dir, 0o755)
	if err == nil {
		err = os.Rename(older.Path, dst)
	}
	if err != nil {
		log.Printf("[artifact] cannot move the cached %s into %s (%v); downloading it again", filepath.Base(dst), dir, err)
		return
	}
	idx, _ := artifact.LoadIndex(dir)
	older.Path = dst
	idx.Put(older)
	if err := idx.Save(); err != nil {
		log.Printf("[artifact] warning: failed to save index: %v", err)
	}
	forgetCachedCopy(artDir, older.URI)
	log.Printf("[artifact] moved the cached %s into %s", filepath.Base(dst), dir)
}

// forgetCachedCopy drops uri's entry from the index in dir, once its file has
// gone: left there, it would name a path that no longer exists.
func forgetCachedCopy(dir, uri string) {
	idx, _ := artifact.LoadIndex(dir)
	if _, ok := idx.Get(uri); !ok {
		return
	}
	idx.Delete(uri)
	if err := idx.Save(); err != nil {
		log.Printf("[artifact] warning: failed to save index: %v", err)
	}
}

// stageArtifactToWorkDir puts a verified artifact from the cache into the
// component workDir under its basename, as a hard link to the cached file. Why
// it links rather than copies is in site/content/internals/artifacts.md.
//
// The destination is never opened for writing. The link is made under a
// temporary name in the same directory and renamed over the destination, so a
// running process keeps the inode it has, and a reader sees the old file or the
// new one and nothing in between. When the destination already is the cached
// file, there is nothing to do: rename(2) would leave the temporary name behind
// if both names were links to the same inode.
//
// The owner and the permission bits of a file being replaced carry over, owner
// first (see keepOwnerAndMode). An install hook runs once per workdir and may
// have made the file executable; a fresh download must not take that away.
//
// The workdir file and the cached file are one file, so a write into it in
// place is a write into the cache. The next staging detects it only when the
// recipe declares the artifact's sha256, which it must unless the agent runs
// with --insecure-skip-verify: the digest no longer matches, and the artifact
// is downloaded again. Without one, the cache reuses the file unchecked and the
// write stays.
//
// Where a link is not possible — a workdir on another filesystem, or a
// filesystem without hard links — the artifact is copied instead, the same way:
// to a temporary name, synced, and renamed over the destination.
func stageArtifactToWorkDir(srcPath, workDir string) error {
	st, err := os.Stat(srcPath)
	if err != nil {
		return err
	}
	if st.IsDir() {
		return fmt.Errorf("artifact path is a directory: %s", srcPath)
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return err
	}
	base := filepath.Base(srcPath)
	dstPath := filepath.Join(workDir, base)

	var prev os.FileInfo
	if fi, err := os.Lstat(dstPath); err == nil {
		if os.SameFile(fi, st) {
			return nil
		}
		if fi.Mode().IsRegular() {
			prev = fi
		}
	}

	removeStagingLeftovers(workDir, base)
	tmp, err := linkOrCopyToTemp(srcPath, workDir, base, st)
	if err != nil {
		return err
	}
	if prev != nil {
		keepOwnerAndMode(tmp, prev, base)
	}
	if err := os.Rename(tmp, dstPath); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// stagingPrefix names the temporary entries of base in its workdir. The pid
// lets a later agent tell its own in-flight names from those of one that died
// halfway through a staging.
func stagingPrefix(base string) string { return "." + base + ".keystone-" }

func stagingPrefixOf(base string, pid int) string {
	return stagingPrefix(base) + strconv.Itoa(pid) + "-"
}

// linkOrCopyToTemp makes a temporary name in dir for src: a hard link when it
// can, a synced copy when it cannot.
func linkOrCopyToTemp(src, dir, base string, st os.FileInfo) (string, error) {
	prefix := stagingPrefixOf(base, os.Getpid())
	for attempt := 0; ; attempt++ {
		tmp := filepath.Join(dir, prefix+strconv.FormatUint(rand.Uint64(), 36))
		err := linkArtifact(src, tmp)
		if err == nil {
			return tmp, nil
		}
		if errors.Is(err, fs.ErrExist) && attempt < 8 {
			continue
		}
		log.Printf("[artifact] cannot hard-link %s into %s (%v); copying it instead", base, dir, err)
		return copyToTemp(src, dir, prefix, st)
	}
}

func copyToTemp(src, dir, prefix string, st os.FileInfo) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()

	out, err := os.CreateTemp(dir, prefix+"*")
	if err != nil {
		return "", err
	}
	tmp := out.Name()
	fail := func(err error) (string, error) {
		_ = out.Close()
		_ = os.Remove(tmp)
		return "", err
	}
	mode := st.Mode().Perm()
	if mode == 0 {
		mode = 0o644
	}
	if err := out.Chmod(mode); err != nil {
		return fail(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		return fail(err)
	}
	// Synced before the rename, so a power cut cannot leave the destination
	// pointing at a file whose data never reached the disk.
	if err := out.Sync(); err != nil {
		return fail(err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return tmp, nil
}

// keepOwnerAndMode gives path the owner and the permission bits of prev, the
// file it is about to replace. With a link, path is the cached file itself, so
// the cache takes them too, which nothing in the agent reads.
//
// The owner goes first: changing it clears the setuid and setgid bits, root
// included, so setting the mode after is what keeps them.
//
// A failure is reported and not returned. The bytes are right and verified;
// refusing to start a component over a permission that could not be copied
// would turn a cosmetic difference into an outage.
func keepOwnerAndMode(path string, prev os.FileInfo, base string) {
	cur, err := os.Stat(path)
	if err != nil {
		log.Printf("[artifact] warning: cannot keep the owner and mode of %s: %v", base, err)
		return
	}
	if ps, ok := prev.Sys().(*syscall.Stat_t); ok {
		if cs, ok := cur.Sys().(*syscall.Stat_t); ok && (cs.Uid != ps.Uid || cs.Gid != ps.Gid) {
			if err := os.Chown(path, int(ps.Uid), int(ps.Gid)); err != nil {
				log.Printf("[artifact] warning: cannot keep the owner of %s (%d:%d): %v", base, ps.Uid, ps.Gid, err)
			}
		}
	}
	const bits = fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky
	if cur, err = os.Stat(path); err == nil && cur.Mode()&bits == prev.Mode()&bits {
		return
	}
	if err := os.Chmod(path, prev.Mode()&bits); err != nil {
		log.Printf("[artifact] warning: cannot keep the mode of %s (%v): %v", base, prev.Mode()&bits, err)
	}
}

// removeStagingLeftovers deletes the temporary names an agent left in dir when
// it died between making one and renaming it. A leftover link costs nothing,
// but a leftover copy is a whole artifact. Names carrying this agent's pid are
// left alone: they may belong to a staging still in progress.
func removeStagingLeftovers(dir, base string) {
	matches, _ := filepath.Glob(filepath.Join(dir, stagingPrefix(base)+"*"))
	mine := stagingPrefixOf(base, os.Getpid())
	for _, m := range matches {
		if strings.HasPrefix(filepath.Base(m), mine) {
			continue
		}
		if err := os.Remove(m); err == nil {
			log.Printf("[artifact] removed %s, left by an interrupted staging", m)
		}
	}
}
