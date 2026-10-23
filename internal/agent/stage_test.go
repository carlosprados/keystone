package agent

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/carlosprados/keystone/internal/artifact"
	"github.com/carlosprados/keystone/internal/dataset"
	"github.com/carlosprados/keystone/internal/manifest"
	"github.com/carlosprados/keystone/internal/recipe"
)

func statT(t *testing.T, path string) *syscall.Stat_t {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return fi.Sys().(*syscall.Stat_t)
}

func writeArtifact(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}

// stagingTemps lists the temporary names staging leaves in dir, which must be
// none once it returns.
func stagingTemps(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		if strings.Contains(e.Name(), ".keystone-") {
			out = append(out, e.Name())
		}
	}
	return out
}

// TestStagingLinksTheCachedFile is the change itself: the workdir name is a
// second name for the cached file, so the artifact takes its size once.
func TestStagingLinksTheCachedFile(t *testing.T) {
	cache, work := t.TempDir(), t.TempDir()
	src := filepath.Join(cache, "app")
	writeArtifact(t, src, []byte("the artifact"), 0o644)

	if err := stageArtifactToWorkDir(src, work); err != nil {
		t.Fatalf("stage: %v", err)
	}
	dst := filepath.Join(work, "app")
	s, d := statT(t, src), statT(t, dst)
	if s.Ino != d.Ino {
		t.Fatalf("workdir file is inode %d, cache is %d: it was copied, not linked", d.Ino, s.Ino)
	}
	if d.Nlink != 2 {
		t.Errorf("link count %d, want 2 (the cache and the workdir)", d.Nlink)
	}
	if got := stagingTemps(t, work); len(got) > 0 {
		t.Errorf("temporary names left in the workdir: %v", got)
	}
}

// TestStagingAgainChangesNothing is what every restart does. Opening the
// destination for writing would now truncate the cache itself, since they are
// the same file; and renaming a link over another link to the same inode does
// nothing and leaves the temporary name behind.
func TestStagingAgainChangesNothing(t *testing.T) {
	cache, work := t.TempDir(), t.TempDir()
	src := filepath.Join(cache, "app")
	content := bytes.Repeat([]byte("artifact "), 4096)
	writeArtifact(t, src, content, 0o644)

	for i := 0; i < 3; i++ {
		if err := stageArtifactToWorkDir(src, work); err != nil {
			t.Fatalf("stage %d: %v", i, err)
		}
	}
	if got := mustRead(t, src); !bytes.Equal(got, content) {
		t.Fatalf("the cached artifact changed: %d bytes, want %d", len(got), len(content))
	}
	if statT(t, src).Ino != statT(t, filepath.Join(work, "app")).Ino {
		t.Error("workdir file is no longer the cached file")
	}
	if n := statT(t, src).Nlink; n != 2 {
		t.Errorf("link count %d after restaging, want 2", n)
	}
	if got := stagingTemps(t, work); len(got) > 0 {
		t.Errorf("temporary names left in the workdir: %v", got)
	}
}

// TestStagingReplacesACopyFromAnOlderAgent is the first start after an upgrade:
// the workdir holds a separate copy, made executable by the recipe's install
// hook, which does not run again. The copy goes, its mode stays.
func TestStagingReplacesACopyFromAnOlderAgent(t *testing.T) {
	cache, work := t.TempDir(), t.TempDir()
	src := filepath.Join(cache, "app")
	writeArtifact(t, src, []byte("the artifact"), 0o644)
	dst := filepath.Join(work, "app")
	writeArtifact(t, dst, []byte("the artifact"), 0o755)
	old := statT(t, dst).Ino

	if err := stageArtifactToWorkDir(src, work); err != nil {
		t.Fatalf("stage: %v", err)
	}
	d := statT(t, dst)
	if d.Ino == old {
		t.Fatal("the old copy is still there")
	}
	if d.Ino != statT(t, src).Ino {
		t.Fatal("workdir file is not the cached file")
	}
	if fi, _ := os.Stat(dst); fi.Mode().Perm() != 0o755 {
		t.Errorf("mode %v, want 0755: the executable bit the install hook set was lost", fi.Mode().Perm())
	}
}

// TestStagingReplacesALinkToAnOlderDownload: after the cache downloads an
// artifact again (a new inode), the workdir still names the old one. It is
// moved to the new one, and the old one is left alone for whoever still has it
// open.
func TestStagingReplacesALinkToAnOlderDownload(t *testing.T) {
	cache, work := t.TempDir(), t.TempDir()
	old := filepath.Join(cache, "old")
	writeArtifact(t, old, []byte("previous download"), 0o644)
	dst := filepath.Join(work, "app")
	if err := os.Link(old, dst); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(cache, "app")
	writeArtifact(t, src, []byte("current download"), 0o644)

	if err := stageArtifactToWorkDir(src, work); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if statT(t, dst).Ino != statT(t, src).Ino {
		t.Fatal("workdir file is not the current download")
	}
	if got := string(mustRead(t, old)); got != "previous download" {
		t.Errorf("the previous file was written to: %q", got)
	}
	if n := statT(t, old).Nlink; n != 1 {
		t.Errorf("previous file has %d links, want 1", n)
	}
}

// TestStagingKeepsOwnerAndModeOfWhatItReplaces: what O_TRUNC kept for free.
// The owner goes before the mode, because changing the owner clears setuid and
// setgid; the order is only observable as root, and so is changing the owner.
func TestStagingKeepsOwnerAndModeOfWhatItReplaces(t *testing.T) {
	cache, work := t.TempDir(), t.TempDir()
	src := filepath.Join(cache, "app")
	writeArtifact(t, src, []byte("the artifact"), 0o644)
	dst := filepath.Join(work, "app")
	writeArtifact(t, dst, []byte("an older copy"), 0o750|fs.ModeSetgid)
	want := fs.FileMode(0o750) | fs.ModeSetgid

	root := os.Geteuid() == 0
	if root {
		if err := os.Chown(dst, 4242, 4343); err != nil {
			t.Fatal(err)
		}
		// chown cleared setuid and setgid on dst; set them back, as a hook would
		if err := os.Chmod(dst, 0o750|fs.ModeSetuid|fs.ModeSetgid); err != nil {
			t.Fatal(err)
		}
		want = 0o750 | fs.ModeSetuid | fs.ModeSetgid
	}

	if err := stageArtifactToWorkDir(src, work); err != nil {
		t.Fatalf("stage: %v", err)
	}
	fi, _ := os.Stat(dst)
	const bits = fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky
	if got := fi.Mode() & bits; got != want {
		t.Errorf("mode %v, want %v", got, want)
	}
	if !root {
		t.Log("not root: the owner part is skipped")
		return
	}
	if s := statT(t, dst); s.Uid != 4242 || s.Gid != 4343 {
		t.Errorf("owner %d:%d, want 4242:4343", s.Uid, s.Gid)
	}
}

// TestStagingCopiesWhenItCannotLink covers a workdir on another filesystem, or
// a filesystem without hard links: the artifact is copied, still to a
// temporary name and renamed into place.
func TestStagingCopiesWhenItCannotLink(t *testing.T) {
	orig := linkArtifact
	linkArtifact = func(oldname, newname string) error {
		return &os.LinkError{Op: "link", Old: oldname, New: newname, Err: syscall.EXDEV}
	}
	t.Cleanup(func() { linkArtifact = orig })

	cache, work := t.TempDir(), t.TempDir()
	src := filepath.Join(cache, "app")
	content := bytes.Repeat([]byte("artifact "), 4096)
	writeArtifact(t, src, content, 0o640)

	if err := stageArtifactToWorkDir(src, work); err != nil {
		t.Fatalf("stage: %v", err)
	}
	dst := filepath.Join(work, "app")
	if statT(t, dst).Ino == statT(t, src).Ino {
		t.Fatal("linked although linking was made to fail")
	}
	if got := mustRead(t, dst); !bytes.Equal(got, content) {
		t.Fatalf("copy is %d bytes, want %d", len(got), len(content))
	}
	if fi, _ := os.Stat(dst); fi.Mode().Perm() != 0o640 {
		t.Errorf("mode %v, want the cached file's 0640", fi.Mode().Perm())
	}
	// and again, over the copy it made: the content stays right, nothing is left over
	if err := stageArtifactToWorkDir(src, work); err != nil {
		t.Fatalf("restage: %v", err)
	}
	if got := mustRead(t, dst); !bytes.Equal(got, content) {
		t.Fatalf("copy is %d bytes after restaging, want %d", len(got), len(content))
	}
	if got := stagingTemps(t, work); len(got) > 0 {
		t.Errorf("temporary names left in the workdir: %v", got)
	}
}

// TestStagingOverARunningExecutable is the resume that adopts a survivor: its
// executable is staged again while it runs. Opening a running executable for
// writing fails with ETXTBSY, which used to fail the apply and get the survivor
// reaped.
func TestStagingOverARunningExecutable(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep binary")
	}
	bin := mustRead(t, sleep)

	for _, linking := range []bool{true, false} {
		name := "linking"
		if !linking {
			name = "copying"
		}
		t.Run(name, func(t *testing.T) {
			if !linking {
				orig := linkArtifact
				linkArtifact = func(o, n string) error {
					return &os.LinkError{Op: "link", Old: o, New: n, Err: syscall.EXDEV}
				}
				t.Cleanup(func() { linkArtifact = orig })
			}
			cache, work := t.TempDir(), t.TempDir()
			src := filepath.Join(cache, "app")
			writeArtifact(t, src, bin, 0o755)
			dst := filepath.Join(work, "app")
			writeArtifact(t, dst, bin, 0o755) // the copy an older agent staged

			cmd := exec.Command(dst, "30")
			if err := cmd.Start(); err != nil {
				t.Fatalf("start: %v", err)
			}
			t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
			time.Sleep(100 * time.Millisecond)

			if err := stageArtifactToWorkDir(src, work); err != nil {
				t.Fatalf("staging over a running executable: %v", err)
			}
			if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
				t.Errorf("the running process is gone: %v", err)
			}
			if got := mustRead(t, dst); !bytes.Equal(got, bin) {
				t.Error("workdir file does not hold the artifact")
			}
			if linking && statT(t, dst).Ino != statT(t, src).Ino {
				t.Error("workdir file is not the cached file")
			}
		})
	}
}

// TestStagingNeverLeavesTheDestinationMissing: the new name is made aside and
// renamed into place. Removing the destination first and linking after would
// save the same disk and leave a moment with no file, which a component
// starting, or anything reading the workdir, could fall into.
func TestStagingNeverLeavesTheDestinationMissing(t *testing.T) {
	cache, work := t.TempDir(), t.TempDir()
	src := filepath.Join(cache, "app")
	writeArtifact(t, src, []byte("current"), 0o644)
	dst := filepath.Join(work, "app")
	writeArtifact(t, dst, []byte("previous"), 0o644)

	orig := linkArtifact
	calls := 0
	linkArtifact = func(o, n string) error {
		calls++
		if n == dst {
			t.Errorf("linked straight onto the destination")
		}
		if got, err := os.ReadFile(dst); err != nil || string(got) != "previous" {
			t.Errorf("while the new name was made, the destination was %q (%v), want the previous file", got, err)
		}
		return orig(o, n)
	}
	t.Cleanup(func() { linkArtifact = orig })

	if err := stageArtifactToWorkDir(src, work); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if calls == 0 {
		t.Fatal("staging did not link")
	}
	if got := string(mustRead(t, dst)); got != "current" {
		t.Errorf("destination holds %q, want the current artifact", got)
	}
}

// TestStagingRemovesLeftoversOfADeadAgent: an agent that dies between making a
// temporary name and renaming it leaves the name behind, and a leftover copy is
// a whole artifact. Names carrying this agent's pid may be a staging in progress
// and are kept.
func TestStagingRemovesLeftoversOfADeadAgent(t *testing.T) {
	cache, work := t.TempDir(), t.TempDir()
	src := filepath.Join(cache, "app")
	writeArtifact(t, src, []byte("the artifact"), 0o644)
	dead := filepath.Join(work, ".app.keystone-999999999-abc")
	mine := filepath.Join(work, ".app.keystone-"+strconv.Itoa(os.Getpid())+"-def")
	other := filepath.Join(work, ".other.keystone-999999999-abc")
	for _, p := range []string{dead, mine, other} {
		writeArtifact(t, p, []byte("leftover"), 0o644)
	}

	if err := stageArtifactToWorkDir(src, work); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Error("leftover of a dead agent was kept")
	}
	if _, err := os.Stat(mine); err != nil {
		t.Errorf("a name of this agent was removed: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("a leftover of another artifact was removed: %v", err)
	}
}

func serveBytes(t *testing.T, data []byte) (url, sum string) {
	t.Helper()
	url, sum, _ = serveCounted(t, "app", data)
	return url, sum
}

// serveCounted serves data as name and counts the downloads.
func serveCounted(t *testing.T, name string, data []byte) (url, sum string, hits *atomic.Int32) {
	t.Helper()
	hits = new(atomic.Int32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write(data)
	}))
	t.Cleanup(srv.Close)
	h := sha256.Sum256(data)
	return srv.URL + "/" + name, hex.EncodeToString(h[:]), hits
}

// TestAWriteThroughTheWorkdirIsDetectedAndRepaired is the one behaviour a link
// changes. Writing in place into a staged file now writes into the cache; the
// next staging finds the digest wrong, downloads the artifact again, and puts
// it back, with the mode the install hook gave it.
func TestAWriteThroughTheWorkdirIsDetectedAndRepaired(t *testing.T) {
	t.Chdir(t.TempDir())
	content := bytes.Repeat([]byte("artifact "), 4096)
	url, sum := serveBytes(t, content)
	a := &Agent{insecureSkipVerify: true}
	adef := recipe.Artifact{URI: url, SHA256: sum}
	artDir, workDir := "runtime/artifacts/app/1", "runtime/components/app/1"

	if err := a.ensureAndVerifyArtifact(artDir, workDir, adef); err != nil {
		t.Fatalf("first staging: %v", err)
	}
	dst := filepath.Join(workDir, "app")
	if err := os.Chmod(dst, 0o755); err != nil { // the install hook
		t.Fatal(err)
	}
	f, err := os.OpenFile(dst, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteAt([]byte("tampered"), 0)
	_ = f.Close()

	if err := a.ensureAndVerifyArtifact(artDir, workDir, adef); err != nil {
		t.Fatalf("staging after the write: %v", err)
	}
	if got := mustRead(t, dst); !bytes.Equal(got, content) {
		t.Fatal("the workdir file was not repaired")
	}
	if statT(t, dst).Ino != statT(t, filepath.Join(artDir, stagedCacheName, "app")).Ino {
		t.Error("workdir file is not the downloaded file")
	}
	if fi, _ := os.Stat(dst); fi.Mode().Perm() != 0o755 {
		t.Errorf("mode %v after the download, want 0755", fi.Mode().Perm())
	}
}

// TestADatasetPayloadIsLinkedIntoItsVersion: datasets stage a payload that is
// not an archive the same way.
func TestADatasetPayloadIsLinkedIntoItsVersion(t *testing.T) {
	t.Chdir(t.TempDir())
	url, sum := serveBytes(t, []byte("oui,vendor\n"))
	a := &Agent{}
	dir := filepath.Join("runtime", "datasets", "oui", "v1")
	m := &manifest.Manifest{Artifact: manifest.Artifact{URI: url, SHA256: sum}}

	if err := a.materialiseDataset(dataset.Spec{Name: "oui"}, m, dir); err != nil {
		t.Fatalf("materialise: %v", err)
	}
	cached := filepath.Join("runtime", "artifacts", "datasets", "oui", "app")
	if statT(t, filepath.Join(dir, "app")).Ino != statT(t, cached).Ino {
		t.Error("the dataset payload was copied, not linked")
	}
}

// indexEntry reads uri's entry from the index in dir, as the next start will.
func indexEntry(dir, uri string) (artifact.IndexEntry, bool) {
	idx, _ := artifact.LoadIndex(dir)
	return idx.Get(uri)
}

// indexFiles returns the bytes of a version's two indexes, to tell whether
// anything rewrote them.
func indexFiles(t *testing.T, artDir string) string {
	t.Helper()
	var out string
	for _, dir := range []string{artDir, filepath.Join(artDir, stagedCacheName)} {
		b, err := os.ReadFile(filepath.Join(dir, "index.json"))
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		out += dir + ": " + string(b) + "\n"
	}
	return out
}

// stageAsAnOlderAgent is how every agent before linking staged an artifact,
// step for step: its cached copy at the top of the version's directory, then
// the workdir file opened with O_TRUNC and the cached file copied into it.
func stageAsAnOlderAgent(t *testing.T, artDir, workDir, uri, sum string) {
	t.Helper()
	src, _, err := artifact.Ensure(artDir, uri, sum, 0, artifact.HTTPOptions{})
	if err != nil {
		t.Fatalf("older agent: %v", err)
	}
	st, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.OpenFile(filepath.Join(workDir, filepath.Base(src)), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, st.Mode().Perm())
	if err != nil {
		t.Fatalf("older agent: %v", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		t.Fatalf("older agent: %v", err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestGoingBackToAnOlderAgentEmptiesNothing is why the linked copy lives under
// .staged. An older agent stages with O_TRUNC: had it found its cached copy at
// the path it knows, that copy would be the workdir file itself, and truncating
// the one would empty both. It finds nothing there, downloads a copy of its
// own, and writes the same bytes into the shared file. Back on a newer agent,
// that second copy is removed.
func TestGoingBackToAnOlderAgentEmptiesNothing(t *testing.T) {
	t.Chdir(t.TempDir())
	content := bytes.Repeat([]byte("artifact "), 4096)
	url, sum, hits := serveCounted(t, "app", content)
	a := &Agent{insecureSkipVerify: true}
	adef := recipe.Artifact{URI: url, SHA256: sum}
	artDir, workDir := "runtime/artifacts/app/1", "runtime/components/app/1"
	dst := filepath.Join(workDir, "app")
	staged := filepath.Join(artDir, stagedCacheName, "app")
	older := filepath.Join(artDir, "app")

	if err := a.ensureAndVerifyArtifact(artDir, workDir, adef); err != nil {
		t.Fatalf("staging: %v", err)
	}
	if _, err := os.Lstat(older); !os.IsNotExist(err) {
		t.Errorf("%s exists (%v): an older agent would find the workdir file there", older, err)
	}

	stageAsAnOlderAgent(t, artDir, workDir, url, sum)
	for _, p := range []string{dst, staged} {
		if got := mustRead(t, p); !bytes.Equal(got, content) {
			t.Fatalf("after an older agent staged, %s holds %d bytes, want %d", p, len(got), len(content))
		}
	}
	if n := hits.Load(); n != 2 {
		t.Errorf("%d downloads, want 2: the older agent fetches a copy of its own", n)
	}
	linked := statT(t, dst).Ino

	if err := a.ensureAndVerifyArtifact(artDir, workDir, adef); err != nil {
		t.Fatalf("staging again: %v", err)
	}
	if statT(t, dst).Ino != linked {
		t.Error("the workdir file was replaced: the staged copy it names is the one to keep, not the older agent's")
	}
	if _, err := os.Lstat(older); !os.IsNotExist(err) {
		t.Errorf("the older agent's copy is still at %s (%v): the artifact is stored twice", older, err)
	}
	if e, ok := indexEntry(artDir, url); ok {
		t.Errorf("the index at the top of the version still names the removed copy: %+v", e)
	}
	if statT(t, dst).Ino != statT(t, staged).Ino {
		t.Error("workdir file is not the staged copy")
	}
	if n := statT(t, dst).Nlink; n != 2 {
		t.Errorf("link count %d, want 2 (the staged copy and the workdir)", n)
	}
	if n := hits.Load(); n != 2 {
		t.Errorf("%d downloads, want 2: coming back downloads nothing", n)
	}
	if got := mustRead(t, dst); !bytes.Equal(got, content) {
		t.Errorf("back on a newer agent, the workdir file holds %d bytes, want %d", len(got), len(content))
	}
}

// TestUpgradingMovesTheCachedCopyInsteadOfDownloadingIt is the first start
// after upgrading from an agent that copied: its cached copy at the top of the
// version's directory, a separate copy in the workdir. The cached copy is moved
// under .staged — the same file, no download — and the workdir copy gives way to
// a link to it, keeping the mode the install hook gave it.
func TestUpgradingMovesTheCachedCopyInsteadOfDownloadingIt(t *testing.T) {
	t.Chdir(t.TempDir())
	content := bytes.Repeat([]byte("artifact "), 4096)
	url, sum, hits := serveCounted(t, "app", content)
	artDir, workDir := "runtime/artifacts/app/1", "runtime/components/app/1"
	older := filepath.Join(artDir, "app")
	staged := filepath.Join(artDir, stagedCacheName, "app")
	dst := filepath.Join(workDir, "app")

	stageAsAnOlderAgent(t, artDir, workDir, url, sum)
	if err := os.Chmod(dst, 0o755); err != nil { // the install hook
		t.Fatal(err)
	}
	cached := statT(t, older).Ino
	before := hits.Load()

	a := &Agent{insecureSkipVerify: true}
	adef := recipe.Artifact{URI: url, SHA256: sum}
	if err := a.ensureAndVerifyArtifact(artDir, workDir, adef); err != nil {
		t.Fatalf("staging: %v", err)
	}
	indexes := indexFiles(t, artDir)
	// the second start reuses what the first moved, and has nothing to tidy
	if err := a.ensureAndVerifyArtifact(artDir, workDir, adef); err != nil {
		t.Fatalf("staging again: %v", err)
	}
	if again := indexFiles(t, artDir); again != indexes {
		t.Errorf("the second start rewrote an index:\n%s\nthen\n%s", indexes, again)
	}
	if n := hits.Load() - before; n != 0 {
		t.Errorf("%d downloads, want 0: the cached copy was there to move", n)
	}
	if statT(t, staged).Ino != cached {
		t.Error("the staged copy is not the file the older agent cached")
	}
	if _, err := os.Lstat(older); !os.IsNotExist(err) {
		t.Errorf("the older agent's copy is still at %s (%v)", older, err)
	}
	if e, ok := indexEntry(artDir, url); ok {
		t.Errorf("the index at the top of the version still names the moved copy: %+v", e)
	}
	if e, ok := indexEntry(filepath.Join(artDir, stagedCacheName), url); !ok || e.Path != staged {
		t.Errorf("the index of %s does not name the moved copy: %+v", stagedCacheName, e)
	}
	if statT(t, dst).Ino != cached {
		t.Error("workdir file is not the staged copy")
	}
	if fi, _ := os.Stat(dst); fi.Mode().Perm() != 0o755 {
		t.Errorf("mode %v, want the 0755 the install hook set", fi.Mode().Perm())
	}
}

// TestAMovedCopyIsVerifiedLikeAnyOther: moving the older agent's cached copy
// does not vouch for it. One that no longer matches its digest is replaced by a
// download, as it would have been where it was.
func TestAMovedCopyIsVerifiedLikeAnyOther(t *testing.T) {
	t.Chdir(t.TempDir())
	content := bytes.Repeat([]byte("artifact "), 4096)
	url, sum, hits := serveCounted(t, "app", content)
	artDir, workDir := "runtime/artifacts/app/1", "runtime/components/app/1"

	stageAsAnOlderAgent(t, artDir, workDir, url, sum)
	f, err := os.OpenFile(filepath.Join(artDir, "app"), os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteAt([]byte("tampered"), 0)
	_ = f.Close()
	before := hits.Load()

	a := &Agent{insecureSkipVerify: true}
	if err := a.ensureAndVerifyArtifact(artDir, workDir, recipe.Artifact{URI: url, SHA256: sum}); err != nil {
		t.Fatalf("staging: %v", err)
	}
	if n := hits.Load() - before; n != 1 {
		t.Errorf("%d downloads, want 1: the moved copy did not match its digest", n)
	}
	dst := filepath.Join(workDir, "app")
	if got := mustRead(t, dst); !bytes.Equal(got, content) {
		t.Fatal("the workdir file does not hold the artifact")
	}
	if statT(t, dst).Ino != statT(t, filepath.Join(artDir, stagedCacheName, "app")).Ino {
		t.Error("workdir file is not the staged copy")
	}
}

func tarGz(t *testing.T, name string, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestAnUnpackedArtifactStaysWhereDeltasLookForIt: an archive with unpack is
// extracted, never linked, so an older agent has nothing to truncate. And its
// cached copy is what a delta download looks for in the index at the top of
// each version, so it stays there.
func TestAnUnpackedArtifactStaysWhereDeltasLookForIt(t *testing.T) {
	t.Chdir(t.TempDir())
	url, sum, _ := serveCounted(t, "app.tar.gz", tarGz(t, "bin/app", []byte("the app")))
	artDir, workDir := "runtime/artifacts/app/1", "runtime/components/app/1"

	a := &Agent{insecureSkipVerify: true}
	if err := a.ensureAndVerifyArtifact(artDir, workDir, recipe.Artifact{URI: url, SHA256: sum, Unpack: true}); err != nil {
		t.Fatalf("install: %v", err)
	}
	if got := string(mustRead(t, filepath.Join(workDir, "bin", "app"))); got != "the app" {
		t.Fatalf("unpacked %q, want the archive's file", got)
	}
	idx, _ := artifact.LoadIndex(artDir)
	if e, ok := idx.Get(url); !ok || filepath.Dir(e.Path) != artDir {
		t.Errorf("the archive is not in its version's own index, where a delta looks for a base: %+v", e)
	}
	if _, err := os.Stat(filepath.Join(artDir, stagedCacheName)); !os.IsNotExist(err) {
		t.Errorf("%s exists for an unpacked artifact (%v)", stagedCacheName, err)
	}
}

// TestAnOlderAgentRematerialisingADatasetLeavesTheCacheWhole: a dataset's
// cached payload keeps its place, unlike a staged artifact's, because an older
// agent never writes into a file a newer one linked. Every agent that knows
// datasets prepares a version by removing its directory and creating it again,
// so the file its copy opens is always a new one.
func TestAnOlderAgentRematerialisingADatasetLeavesTheCacheWhole(t *testing.T) {
	t.Chdir(t.TempDir())
	content := []byte("oui,vendor\n")
	url, sum := serveBytes(t, content)
	store := dataset.NewStore(filepath.Join("runtime", "datasets"))
	cacheDir := filepath.Join("runtime", "artifacts", "datasets", "oui")
	m := &manifest.Manifest{Artifact: manifest.Artifact{URI: url, SHA256: sum}}

	dir, err := store.Prepare("oui", "v1")
	if err != nil {
		t.Fatal(err)
	}
	if err := (&Agent{}).materialiseDataset(dataset.Spec{Name: "oui"}, m, dir); err != nil {
		t.Fatalf("materialise: %v", err)
	}

	// an older agent materialising the same version
	if dir, err = store.Prepare("oui", "v1"); err != nil {
		t.Fatal(err)
	}
	stageAsAnOlderAgent(t, cacheDir, dir, url, sum)
	if got := mustRead(t, filepath.Join(cacheDir, "app")); !bytes.Equal(got, content) {
		t.Fatalf("the cached payload holds %d bytes after an older agent, want %d", len(got), len(content))
	}
}

// TestOnlyTheVersionsOwnFilesAreMoved: an index entry naming a file outside the
// version's directory is not this cache's to move or remove, whatever put it
// there. The artifact is downloaded instead.
func TestOnlyTheVersionsOwnFilesAreMoved(t *testing.T) {
	t.Chdir(t.TempDir())
	content := []byte("the artifact")
	url, sum, hits := serveCounted(t, "app", content)
	artDir, workDir := "runtime/artifacts/app/1", "runtime/components/app/1"
	elsewhere := filepath.Join("elsewhere", "app")
	writeArtifact(t, elsewhere, content, 0o644)
	idx, _ := artifact.LoadIndex(artDir)
	idx.Put(artifact.IndexEntry{URI: url, SHA256: sum, Path: elsewhere, Size: int64(len(content))})
	if err := idx.Save(); err != nil {
		t.Fatal(err)
	}

	a := &Agent{insecureSkipVerify: true}
	if err := a.ensureAndVerifyArtifact(artDir, workDir, recipe.Artifact{URI: url, SHA256: sum}); err != nil {
		t.Fatalf("staging: %v", err)
	}
	if got, err := os.ReadFile(elsewhere); err != nil || !bytes.Equal(got, content) {
		t.Errorf("the file outside the version's directory was moved or changed: %q, %v", got, err)
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("%d downloads, want 1", n)
	}
}

// TestAMoveThatFailsKeepsTheOlderCopyIndexed: the entry at the top of the
// version goes only once its file has. A move that fails leaves both where they
// were, so an older agent still finds its copy there.
func TestAMoveThatFailsKeepsTheOlderCopyIndexed(t *testing.T) {
	t.Chdir(t.TempDir())
	content := bytes.Repeat([]byte("artifact "), 4096)
	url, sum, _ := serveCounted(t, "app", content)
	artDir, workDir := "runtime/artifacts/app/1", "runtime/components/app/1"
	older := filepath.Join(artDir, "app")
	stagedDir := filepath.Join(artDir, stagedCacheName)

	stageAsAnOlderAgent(t, artDir, workDir, url, sum)
	writeArtifact(t, stagedDir, nil, 0o644) // a file in the way: the move cannot create the directory
	e, ok := olderCachedCopy(artDir, url)
	if !ok {
		t.Fatal("the older agent's copy is not there to move")
	}
	moveCachedCopy(e, artDir, stagedDir)

	if got := mustRead(t, older); !bytes.Equal(got, content) {
		t.Fatalf("the older agent's copy holds %d bytes after a failed move, want %d", len(got), len(content))
	}
	if e, ok := indexEntry(artDir, url); !ok || e.Path != older {
		t.Errorf("a failed move dropped the entry of a copy that is still there: %+v", e)
	}
}

// TestMovingACopyLeavesTheRestOfTheIndex: only the moved copy's entry goes.
// The index at the top of a version also names its unpacked archives, which is
// where a delta download looks for its base.
func TestMovingACopyLeavesTheRestOfTheIndex(t *testing.T) {
	t.Chdir(t.TempDir())
	url, sum, _ := serveCounted(t, "app", bytes.Repeat([]byte("artifact "), 4096))
	tarURL, tarSum, _ := serveCounted(t, "app.tar.gz", tarGz(t, "bin/app", []byte("the app")))
	artDir, workDir := "runtime/artifacts/app/1", "runtime/components/app/1"

	// an older agent cached both: the archive where everyone keeps it
	if _, _, err := artifact.Ensure(artDir, tarURL, tarSum, 0, artifact.HTTPOptions{}); err != nil {
		t.Fatal(err)
	}
	stageAsAnOlderAgent(t, artDir, workDir, url, sum)

	a := &Agent{insecureSkipVerify: true}
	if err := a.ensureAndVerifyArtifact(artDir, workDir, recipe.Artifact{URI: url, SHA256: sum}); err != nil {
		t.Fatalf("staging: %v", err)
	}
	if e, ok := indexEntry(artDir, url); ok {
		t.Errorf("the moved copy is still in the index at the top of the version: %+v", e)
	}
	if e, ok := indexEntry(artDir, tarURL); !ok || filepath.Dir(e.Path) != artDir {
		t.Errorf("moving the staged copy dropped the archive's entry, where a delta looks for a base: %+v", e)
	}
}
