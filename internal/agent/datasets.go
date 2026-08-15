package agent

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/carlosprados/keystone/internal/adapter"
	"github.com/carlosprados/keystone/internal/artifact"
	"github.com/carlosprados/keystone/internal/dataset"
	"github.com/carlosprados/keystone/internal/manifest"
	"github.com/carlosprados/keystone/internal/metrics"
	"github.com/carlosprados/keystone/internal/security"
	"github.com/carlosprados/keystone/internal/state"
)

// maxManifestBytes caps a manifest download. A manifest is a few hundred bytes;
// anything approaching this is either a mistake or someone feeding the device a
// file to exhaust its memory before a single signature has been checked.
const maxManifestBytes = 1 << 20 // 1 MiB

// datasetBinding is one dataset attached to one component: the spec from the
// recipe, plus what it takes to tell that component its data changed.
type datasetBinding struct {
	spec      dataset.Spec
	component string
	reload    dataset.ReloadPlan
	workDir   string
}

// resolveDataset brings a dataset to the version its manifest names, and
// reports whether anything changed.
//
// The order is the whole design, and every step depends on the one before it:
//
//  1. fetch the manifest — small, and the only request made when nothing has
//     changed;
//  2. verify its signature, which is a hard failure and never a fallback;
//  3. enforce the anti-replay rule against the last accepted publication;
//  4. compare with what is already active and stop if it matches;
//  5. download and verify the payload against the digest the manifest carries;
//  6. extract into a fresh directory, with no idempotence markers to skip it;
//  7. swap the symlink atomically;
//  8. reload the component and keep or roll back.
//
// Steps 7 and 8 are the caller's, through activateDataset: a first install has
// no component running to reload, and a refresh does.
func (a *Agent) resolveDataset(ctx context.Context, spec dataset.Spec) (*manifest.Manifest, bool, error) {
	m, err := a.fetchAndVerifyManifest(ctx, spec)
	if err != nil {
		return nil, false, err
	}

	last := a.datasetPublished(spec.Name)
	if !m.IsNewerThan(last) {
		return nil, false, fmt.Errorf("%w: manifest for %s is published %s, which is not newer than the %s already accepted; refusing a possible replay",
			adapter.ErrInvalidInput, spec.Name, m.Published.Format(time.RFC3339), last.Format(time.RFC3339))
	}

	// A signed publication timestamp newer than anything seen before is proof
	// the real time has reached it — evidence a device that has never seen NTP
	// has no other way to get. Only after the signature and the replay check.
	a.clock.Advance(m.Published)

	if active := a.datasets.Active(spec.Name); active == m.Version {
		return m, false, nil
	}

	dir, err := a.datasets.Prepare(spec.Name, m.Version)
	if err != nil {
		return nil, false, err
	}
	if err := a.materialiseDataset(spec, m, dir); err != nil {
		// Leave nothing half-extracted behind to be mistaken for a version.
		_ = os.RemoveAll(dir)
		return nil, false, err
	}
	return m, true, nil
}

// fetchAndVerifyManifest downloads the manifest and its signature and checks
// them, unless verification is disabled for development.
func (a *Agent) fetchAndVerifyManifest(ctx context.Context, spec dataset.Spec) (*manifest.Manifest, error) {
	tmp, err := os.MkdirTemp("", "keystone-manifest-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)

	manifestPath := filepath.Join(tmp, "manifest.toml")
	if err := a.fetchTo(ctx, spec, spec.Manifest, manifestPath); err != nil {
		return nil, fmt.Errorf("fetching the manifest for %s: %w", spec.Name, err)
	}

	if !a.insecureSkipVerify {
		if a.trustPool == nil {
			return nil, fmt.Errorf("dataset %q rejected: no trust bundle configured; set KEYSTONE_TRUST_BUNDLE (or --insecure-skip-verify for dev)", spec.Name)
		}
		sigPath := filepath.Join(tmp, "manifest.toml.sig")
		if err := a.fetchTo(ctx, spec, spec.SigURI, sigPath); err != nil {
			return nil, fmt.Errorf("fetching the manifest signature for %s: %w", spec.Name, err)
		}
		certPath := spec.CertURI
		if certPath != "" {
			local := filepath.Join(tmp, "leaf.pem")
			if err := a.fetchTo(ctx, spec, spec.CertURI, local); err != nil {
				return nil, fmt.Errorf("fetching the signing certificate for %s: %w", spec.Name, err)
			}
			certPath = local
		} else {
			certPath = os.Getenv("KEYSTONE_LEAF_CERT")
		}
		if certPath == "" {
			return nil, fmt.Errorf("dataset %q rejected: no certificate for the manifest signature (set cert_uri or KEYSTONE_LEAF_CERT)", spec.Name)
		}
		now, err := a.verificationTime()
		if err != nil {
			return nil, fmt.Errorf("dataset %q rejected: %w", spec.Name, err)
		}
		if err := security.VerifyDetachedAt(manifestPath, sigPath, certPath, a.trustPool, now); err != nil {
			return nil, fmt.Errorf("%w: manifest signature verify failed for %s: %v", adapter.ErrInvalidInput, spec.Name, err)
		}
	}

	m, err := manifest.Load(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", adapter.ErrInvalidInput, err)
	}
	for _, u := range m.UnknownFields {
		log.Printf("[dataset] name=%s msg=ignoring unknown manifest field %s", spec.Name, u)
	}
	if m.Name != "" && m.Name != spec.Name {
		// Not fatal on its own, but it is nearly always a copied-and-pasted
		// manifest pointing at the wrong data.
		log.Printf("[dataset] name=%s msg=manifest declares name %q; using the recipe's name", spec.Name, m.Name)
	}
	return m, nil
}

// materialiseDataset downloads the payload the manifest names, verifies it
// against the digest the manifest carries, and puts it in dir.
func (a *Agent) materialiseDataset(spec dataset.Spec, m *manifest.Manifest, dir string) error {
	httpOpts := artifact.HTTPOptions{Headers: spec.Headers, GithubToken: spec.GithubToken}
	cacheDir := filepath.Join("runtime", "artifacts", "datasets", spec.Name)

	// The digest comes from the signed manifest, so Ensure's URI cache cannot
	// serve stale bytes: a mismatch re-downloads.
	path, _, err := artifact.Ensure(cacheDir, m.Artifact.URI, m.Artifact.SHA256, a.artifactDownloadTimeout, httpOpts)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", spec.Name, err)
	}

	if artifact.LooksLikeArchive(path) {
		if err := artifact.Unpack(path, dir); err != nil {
			return fmt.Errorf("unpacking %s: %w", spec.Name, err)
		}
		return nil
	}
	return stageArtifactToWorkDir(path, dir)
}

// fetchTo downloads a small document to a path, with no caching anywhere.
//
// Deliberately not artifact.Ensure: that caches by URI with no expiry, which is
// exactly right for an immutable artifact and exactly wrong for a manifest
// whose whole purpose is to change.
func (a *Agent) fetchTo(ctx context.Context, spec dataset.Spec, uri, dest string) error {
	if !strings.HasPrefix(uri, "http://") && !strings.HasPrefix(uri, "https://") {
		// A local path: copy it, so the same code path serves a file-based hub.
		return copyFile(uri, dest)
	}

	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return err
	}
	for k, v := range spec.Headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s returned %s", uri, resp.Status)
	}

	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, io.LimitReader(resp.Body, maxManifestBytes)); err != nil {
		return err
	}
	return nil
}

func copyFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, io.LimitReader(in, maxManifestBytes))
	return err
}

// activateDataset switches a dataset to a new version and tells the component,
// rolling back if the component does not survive it.
func (a *Agent) activateDataset(ctx context.Context, b datasetBinding, m *manifest.Manifest) error {
	previous := a.datasets.Active(b.spec.Name)

	if err := a.datasets.Activate(b.spec.Name, m.Version); err != nil {
		return err
	}
	log.Printf("[dataset] name=%s version=%s previous=%s msg=activated", b.spec.Name, m.Version, orNone(previous))

	if b.reload.Declared() {
		if err := a.reloadComponent(ctx, b); err != nil {
			log.Printf("[dataset] name=%s component=%s msg=reload failed (%v); rolling back to %s", b.spec.Name, b.component, err, orNone(previous))
			a.rollbackDataset(ctx, b, previous)
			return fmt.Errorf("reloading %s after %s changed: %w", b.component, b.spec.Name, err)
		}
		if ok, why := a.componentSurvivedReload(b); !ok {
			log.Printf("[dataset] name=%s component=%s msg=%s; rolling back to %s", b.spec.Name, b.component, why, orNone(previous))
			a.rollbackDataset(ctx, b, previous)
			return fmt.Errorf("%s did not survive the %s reload: %s", b.component, b.spec.Name, why)
		}
	}

	if err := a.datasets.Prune(b.spec.Name, b.spec.Keep, previous); err != nil {
		log.Printf("[dataset] name=%s msg=pruning old versions failed: %v", b.spec.Name, err)
	}
	return nil
}

// rollbackDataset puts the previous version back and tries to reload again. A
// feed one day old is a much smaller problem than a component that will not run.
func (a *Agent) rollbackDataset(ctx context.Context, b datasetBinding, previous string) {
	if previous == "" {
		// Nothing to go back to: the first version of this dataset is also the
		// one that broke the component. Leave it in place — removing it would
		// leave a dangling symlink — and let the failure be reported.
		return
	}
	if err := a.datasets.Activate(b.spec.Name, previous); err != nil {
		log.Printf("[dataset] name=%s msg=ROLLBACK FAILED to %s: %v", b.spec.Name, previous, err)
		return
	}
	if b.reload.Declared() {
		if err := a.reloadComponent(ctx, b); err != nil {
			log.Printf("[dataset] name=%s msg=reload after rollback failed: %v", b.spec.Name, err)
		}
	}
	metrics.ObserveDatasetActivation(b.spec.Name, "rolled-back")
}

// componentSurvivedReload waits out the grace period and reports whether the
// component is still in good shape.
//
// Without a health check there is no verdict to wait for, and the agent can
// confirm nothing beyond "the process is still there" — which is stated in the
// documentation rather than papered over, because it means a component with no
// health check gets no automatic rollback from a bad dataset.
func (a *Agent) componentSurvivedReload(b datasetBinding) (bool, string) {
	deadline := time.Now().Add(b.reload.Grace)
	hasHealth := false
	if ci, ok := a.comps.Get(b.component); ok {
		hasHealth = ci.LastHealth == "healthy" || ci.LastHealth == "unhealthy"
	}

	for time.Now().Before(deadline) {
		ci, ok := a.comps.Get(b.component)
		if !ok {
			return false, "the component vanished from the store"
		}
		if ci.State == "failed" || (ci.PID > 0 && !processAlive(ci.PID)) {
			return false, "the component died"
		}
		if hasHealth && ci.LastHealth == "unhealthy" {
			return false, "the component reported unhealthy"
		}
		if hasHealth && ci.LastHealth == "healthy" {
			return true, ""
		}
		time.Sleep(200 * time.Millisecond)
	}
	if hasHealth {
		return false, fmt.Sprintf("the component did not report healthy within %s", b.reload.Grace)
	}
	// No health check: alive at the end of the grace period is all there is.
	ci, ok := a.comps.Get(b.component)
	if !ok || ci.State == "failed" {
		return false, "the component is not running"
	}
	return true, ""
}

// datasetPublished returns the publication time of the last accepted manifest.
func (a *Agent) datasetPublished(name string) time.Time {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if st, ok := a.datasetState[name]; ok {
		return st.Published
	}
	return time.Time{}
}

// recordDataset stores the outcome of a refresh.
func (a *Agent) recordDataset(spec dataset.Spec, m *manifest.Manifest, result string) {
	a.mu.Lock()
	if a.datasetState == nil {
		a.datasetState = map[string]state.DatasetState{}
	}
	st := a.datasetState[spec.Name]
	st.Name = spec.Name
	st.ManifestURI = spec.Manifest
	st.LastRefresh = time.Now().UTC()
	st.LastResult = result
	if m != nil {
		st.Version = m.Version
		st.Published = m.Published.UTC()
		st.SHA256 = m.Artifact.SHA256
	}
	a.datasetState[spec.Name] = st
	a.mu.Unlock()
	a.persistSnapshot()
}

// DatasetStates returns what every known dataset is serving, for the API.
func (a *Agent) DatasetStates() []adapter.DatasetInfo {
	a.mu.RLock()
	specs := make(map[string]dataset.Spec, len(a.datasetSpecs))
	for k, v := range a.datasetSpecs {
		specs[k] = v
	}
	states := make(map[string]state.DatasetState, len(a.datasetState))
	for k, v := range a.datasetState {
		states[k] = v
	}
	a.mu.RUnlock()

	names := make([]string, 0, len(states))
	for name := range states {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]adapter.DatasetInfo, 0, len(names))
	for _, name := range names {
		st := states[name]
		info := adapter.DatasetInfo{
			Name:        st.Name,
			Version:     st.Version,
			ManifestURI: st.ManifestURI,
			LastResult:  st.LastResult,
			Path:        a.datasets.Path(name),
		}
		if !st.Published.IsZero() {
			info.Published = st.Published.Format(time.RFC3339)
		}
		if !st.LastRefresh.IsZero() {
			info.LastRefresh = st.LastRefresh.Format(time.RFC3339)
		}
		if spec, ok := specs[name]; ok {
			info.Refresh = spec.Refresh.String()
			info.MaxAge = spec.MaxAge.String()
			age, known := a.datasetAge(st)
			if known {
				info.AgeSeconds = int64(age.Seconds())
				info.Stale = age > spec.MaxAge
			} else {
				// A wrong clock makes age meaningless, and a plausible wrong
				// number is worse than an honest gap: it silences the alert
				// that should have fired.
				info.AgeUnknown = true
			}
		}
		out = append(out, info)
	}
	return out
}

// datasetAge is how old the data is, and whether that can be known at all.
func (a *Agent) datasetAge(st state.DatasetState) (time.Duration, bool) {
	if st.Published.IsZero() {
		return 0, false
	}
	if a.clock != nil && !a.clock.Trusted() {
		return 0, false
	}
	return time.Since(st.Published), true
}

// publishDatasetMetrics refreshes the gauges. Called from the state poller.
func (a *Agent) publishDatasetMetrics() {
	for _, info := range a.DatasetStates() {
		if info.AgeUnknown {
			metrics.ClearDatasetAge(info.Name)
			continue
		}
		metrics.SetDatasetAge(info.Name, float64(info.AgeSeconds), info.Stale)
	}
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

// datasetSnapshot returns the persisted form of every dataset's state, sorted
// so the snapshot file — and the fingerprint that decides whether to write it —
// does not change just because a map iterated differently.
func (a *Agent) datasetSnapshot() []state.DatasetState {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if len(a.datasetState) == 0 {
		return nil
	}
	out := make([]state.DatasetState, 0, len(a.datasetState))
	for _, st := range a.datasetState {
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
