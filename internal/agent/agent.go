package agent

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/carlosprados/keystone/internal/artifact"
	"github.com/carlosprados/keystone/internal/config"
	"github.com/carlosprados/keystone/internal/deploy"
	"github.com/carlosprados/keystone/internal/metrics"
	"github.com/carlosprados/keystone/internal/recipe"
	"github.com/carlosprados/keystone/internal/runner"
	"github.com/carlosprados/keystone/internal/security"
	"github.com/carlosprados/keystone/internal/state"
	"github.com/carlosprados/keystone/internal/store"
	"github.com/carlosprados/keystone/internal/supervisor"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Options defines basic runtime configuration for the agent.
type Options struct {
	HTTPAddr string
	DryRun   bool
}

// Agent is the top-level runtime handle for Keystone.
// For the MVP it only exposes health and a tiny local API surface.
type Agent struct {
	opts    Options
	closed  atomic.Bool
	start   time.Time
	mu      sync.RWMutex
	comps   *store.MemoryStore
	procs   map[string]*runner.ProcessHandle
	cancels map[string]context.CancelFunc
	// plan tracking
	planPath   string
	planStatus string // idle | applying | running | failed
	planErr    string
	// persistence
	stateDir string
	// plan mapping
	planComps []state.PlanComponent
	// cache budget
	artifactCacheLimit int64
	dryRun             bool
	// trust
	trustPool *x509.CertPool
	trustPath string
}

// New creates an Agent with the provided options.
func New(opts Options) *Agent {
	// Load .env (best-effort) before anything else so that subsequent code
	// can use variables (e.g., tokens for artifact downloads).
	config.LoadDotEnvDefault()

	a := &Agent{opts: opts, start: time.Now(), comps: store.NewMemoryStore(), procs: make(map[string]*runner.ProcessHandle), cancels: make(map[string]context.CancelFunc), stateDir: filepath.Join("runtime", "state"), dryRun: opts.DryRun}
	// Set artifact cache limit from env (bytes). Default: 2 GiB.
	a.artifactCacheLimit = 2 * 1024 * 1024 * 1024
	if v := os.Getenv("KEYSTONE_ARTIFACT_CACHE_LIMIT_BYTES"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			a.artifactCacheLimit = n
		}
	}
	// Best-effort load snapshot
	if snap, err := state.Load(a.stateDir); err == nil {
		a.planPath = snap.Plan.Path
		a.planStatus = snap.Plan.Status
		a.planErr = snap.Plan.Error
		for _, ci := range snap.Components {
			a.comps.Upsert(ci)
		}
		a.planComps = snap.PlanComponents
	}
	// Load trust bundle if provided via env KEYSTONE_TRUST_BUNDLE
	if tb := os.Getenv("KEYSTONE_TRUST_BUNDLE"); tb != "" {
		if pool, err := security.LoadTrustBundle(tb); err == nil {
			// cache in context or in agent; store path and pool for use during apply
			a.trustPool = pool
			a.trustPath = tb
		} else {
			log.Printf("failed to load trust bundle: %v", err)
		}
	}
	// Periodic snapshots
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			if a.closed.Load() {
				return
			}
			a.persistSnapshot()
		}
	}()
	return a
}

// Router returns the HTTP handler for the local API.
func (a *Agent) Router() http.Handler {
	mux := http.NewServeMux()

	// Liveness probe
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":   "ok",
			"uptime":   time.Since(a.start).String(),
			"closed":   a.closed.Load(),
			"time_utc": time.Now().UTC().Format(time.RFC3339),
		})
	})

	// Very small info endpoint (component listing)
	mux.HandleFunc("/v1/components", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		list := a.comps.List()
		_ = json.NewEncoder(w).Encode(list)
	})

	// Per-component control:
	// - POST /v1/components/{name}:stop
	// - POST /v1/components/{name}:restart
	mux.HandleFunc("/v1/components/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/v1/components/")
		var action string
		switch {
		case strings.HasSuffix(path, ":stop"):
			action = "stop"
		case strings.HasSuffix(path, ":restart"):
			action = "restart"
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
		name := strings.TrimSuffix(path, ":"+action)
		name = strings.Trim(name, "/")
		if name == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch action {
		case "stop":
			a.mu.Lock()
			if cancel := a.cancels[name]; cancel != nil {
				cancel()
				delete(a.cancels, name)
			}
			if h := a.procs[name]; h != nil {
				_ = (&runner.ProcessRunner{}).Stop(context.Background(), h, 5*time.Second)
				delete(a.procs, name)
			}
			ci, ok := a.comps.Get(name)
			if ok {
				ci.State = "stopped"
				a.comps.Upsert(ci)
			}
			a.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case "restart":
			// stop dependents, then target; start target, then dependents (topo order)
			depsOrder := a.planDependentsTopological(name)
			// dry-run path: return orders only
			if r.URL.Query().Get("dry") == "true" {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"stopOrder":  depsOrder,
					"startOrder": append([]string{name}, depsOrder...),
				})
				return
			}
            // Determine wait mode and timeout with precedence: Query > Default (pid, 60s)
            mode := strings.ToLower(r.URL.Query().Get("wait"))
            if mode == "" {
                mode = "pid"
            }
            if mode != "health" {
                mode = "pid"
            }
            var to time.Duration
            if qto := r.URL.Query().Get("timeout"); qto != "" {
                to = parseDurationDefault(qto, 60*time.Second)
            } else {
                to = 60 * time.Second
            }

			// stop dependents first
			log.Printf("api: restart %s stop dependents: %v", name, depsOrder)
			for _, dn := range depsOrder {
				log.Printf("api: stopping dependent %s", dn)
				a.stopComponent(dn)
			}
			// stop target
			log.Printf("api: stopping target %s", name)
			a.stopComponent(name)
			// start target
			log.Printf("api: starting target %s", name)
			if err := a.restartFromPlan(name); err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(err.Error()))
				log.Printf("api: restart %s failed to start: %v", name, err)
				return
			}
			// wait target
			log.Printf("api: waiting %s for %s (timeout=%s)", mode, name, to)
			if err := a.waitReady(name, mode, to); err != nil {
				w.WriteHeader(http.StatusGatewayTimeout)
				_, _ = w.Write([]byte(err.Error()))
				log.Printf("api: restart %s wait failed: %v", name, err)
				return
			}
			log.Printf("api: target %s ready pid=%d", name, a.currentPID(name))
			// start dependents in order and wait for each (best-effort)
			depPIDs := map[string]int{}
			for _, dn := range depsOrder {
				log.Printf("api: starting dependent %s", dn)
				_ = a.restartFromPlan(dn)
				if err := a.waitReady(dn, mode, to); err != nil {
					log.Printf("api: dependent %s not ready: %v", dn, err)
				}
				if pid := a.currentPID(dn); pid > 0 {
					depPIDs[dn] = pid
				}
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"component":  name,
				"pid":        a.currentPID(name),
				"dependents": depPIDs,
				"wait":       mode,
				"timeout":    to.String(),
			})
		}
	})

	// Prometheus metrics
	mux.Handle("/metrics", promhttp.Handler())

	// Plan status
	mux.HandleFunc("/v1/plan/status", func(w http.ResponseWriter, r *http.Request) {
		a.mu.RLock()
		resp := map[string]any{
			"planPath":   a.planPath,
			"status":     a.planStatus,
			"error":      a.planErr,
			"components": a.comps.List(),
		}
		a.mu.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	// Plan graph (nodes, edges, topo order)
	mux.HandleFunc("/v1/plan/graph", func(w http.ResponseWriter, r *http.Request) {
		a.mu.RLock()
		nodes := make([]string, 0, len(a.planComps))
		for _, pc := range a.planComps {
			nodes = append(nodes, pc.Name)
		}
		edges := map[string][]string{}
		indeg := map[string]int{}
		for _, pc := range a.planComps {
			for _, d := range pc.Deps {
				edges[d] = append(edges[d], pc.Name)
				indeg[pc.Name]++
			}
			if _, ok := indeg[pc.Name]; !ok {
				indeg[pc.Name] = 0
			}
		}
		order := topoOrder(edges, indeg)
		a.mu.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"nodes": nodes, "edges": edges, "order": order})
	})

	// Plan apply: POST {"planPath":"...", "dry":bool}
	mux.HandleFunc("/v1/plan/apply", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			PlanPath string `json:"planPath"`
			Dry      bool   `json:"dry"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(err.Error()))
			return
		}
		if req.PlanPath == "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("missing planPath"))
			return
		}
		if err := a.ApplyPlanAPI(req.PlanPath, req.Dry); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(err.Error()))
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})

	// Plan stop (POST)
	mux.HandleFunc("/v1/plan/stop", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		a.mu.Lock()
		for name, cancel := range a.cancels {
			if cancel != nil {
				cancel()
			}
			delete(a.cancels, name)
		}
		for name, h := range a.procs {
			_ = (&runner.ProcessRunner{}).Stop(context.Background(), h, 5*time.Second)
			delete(a.procs, name)
			ci, ok := a.comps.Get(name)
			if ok {
				ci.State = "stopped"
				a.comps.Upsert(ci)
			}
		}
		a.planStatus = "stopped"
		a.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})

	// Root handler with tiny landing
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("Keystone agent is running. See /healthz, /metrics and /v1/components\n"))
	})

	return mux
}

// Close releases agent resources.
func (a *Agent) Close() error {
	if a.closed.Swap(true) {
		return nil
	}
	log.Printf("agent closed")
	a.persistSnapshot()
	return nil
}

// StartDemo boots an internal 3-component demo using the supervisor.
func (a *Agent) StartDemo() error {
	a.mu.Lock()
	defer a.mu.Unlock()

	log.Printf("starting demo stack (db -> cache -> api)")
	db := supervisor.NewComponent(
		"db", nil,
		supervisor.MockInstall("db", 200*time.Millisecond),
		supervisor.MockStart("db", 500*time.Millisecond),
		supervisor.MockStop("db", 150*time.Millisecond),
	)
	cache := supervisor.NewComponent(
		"cache", []string{"db"},
		supervisor.MockInstall("cache", 200*time.Millisecond),
		supervisor.MockStart("cache", 600*time.Millisecond),
		supervisor.MockStop("cache", 150*time.Millisecond),
	)
	api := supervisor.NewComponent(
		"api", []string{"db", "cache"},
		supervisor.MockInstall("api", 200*time.Millisecond),
		supervisor.MockStart("api", 700*time.Millisecond),
		supervisor.MockStop("api", 150*time.Millisecond),
	)

	// Track in store + metrics
	for _, c := range []*supervisor.Component{db, cache, api} {
		a.comps.Upsert(store.ComponentInfo{Name: c.Name, State: string(c.State())})
	}

	go func() {
		// Update store and metrics as state changes (polling for MVP)
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for range ticker.C {
			if a.closed.Load() {
				return
			}
			for _, name := range []string{"db", "cache", "api"} {
				// read state from supervisor component references
				var c *supervisor.Component
				switch name {
				case "db":
					c = db
				case "cache":
					c = cache
				case "api":
					c = api
				}
				st := string(c.State())
				a.comps.Upsert(store.ComponentInfo{Name: name, State: st})
				metrics.ObserveComponentState(name, st)
			}
		}
	}()

	ctx := rctx()
	if err := supervisor.StartStack(ctx, []*supervisor.Component{db, cache, api}); err != nil {
		return fmt.Errorf("demo stack error: %w", err)
	}
	return nil
}

// rctx returns a background context bound to agent lifetime via closed flag.
func rctx() (ctx context.Context) {
	// Simple helper; in a richer agent we would wire this to a root context.
	return context.Background()
}

// ApplyPlan loads a deployment plan and runs its components using ProcessRunner.
func (a *Agent) ApplyPlan(planPath string) error {
	p, err := deploy.Load(planPath)
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.planPath = planPath
	a.planStatus = "applying"
	a.planErr = ""
	a.mu.Unlock()
	// Prepare supervisor components based on recipes
	comps := make([]*supervisor.Component, 0, len(p.Components))
	planMap := make([]state.PlanComponent, 0, len(p.Components))
	// First pass: load all recipes and build name mapping recipeMeta -> compName
	type loaded struct {
		item deploy.Component
		rec  *recipe.Recipe
	}
	var loadedList []loaded
	metaToComp := map[string]string{}
	for _, it := range p.Components {
		r, err := recipe.Load(it.RecipePath)
		if err != nil {
			return fmt.Errorf("%s: %w", it.Name, err)
		}
		loadedList = append(loadedList, loaded{item: it, rec: r})
		metaToComp[r.Metadata.Name] = it.Name
	}
	// Second pass: compute deps among plan components using recipe dependencies
    for _, l := range loadedList {
        // compute dep component names
        var depNames []string
        for _, d := range l.rec.Dependencies {
            if compName, ok := metaToComp[d.Name]; ok {
                depNames = append(depNames, compName)
            }
        }
        planMap = append(planMap, state.PlanComponent{
            Name:       l.item.Name,
            RecipePath: l.item.RecipePath,
            RecipeMeta: l.rec.Metadata.Name,
            Deps:       depNames,
        })
    }
	// Build supervisor components now using computed deps
	pr := runner.New()
	// readiness channels per component (closed when process actually starts)
	compReady := make(map[string]chan struct{})
	for _, l := range loadedList {
		// find deps for this comp
		var depNames []string
		for _, pc := range planMap {
			if pc.Name == l.item.Name {
				depNames = pc.Deps
				break
			}
		}
		r := l.rec
		it := l.item
		var startedOnce sync.Once
		healthBasedReady := false
		// Prepare workspace per component version
		workDir := fmt.Sprintf("runtime/components/%s/%s", r.Metadata.Name, r.Metadata.Version)
		artDir := fmt.Sprintf("runtime/artifacts/%s/%s", r.Metadata.Name, r.Metadata.Version)
            installFn := func(ctx context.Context) error {
                // Download and verify artifacts
                for _, adef := range r.Artifacts {
                    httpOpts := artifact.HTTPOptions{Headers: adef.Headers, GithubToken: adef.GithubToken}
                    path, _, err := artifact.Ensure(artDir, adef.URI, adef.SHA256, 0, httpOpts)
                    if err != nil {
                        return err
                    }
                    // Signature verification if configured
                    if adef.SigURI != "" && a.trustPool != nil {
                        sigPath, _, err := artifact.Ensure(artDir, adef.SigURI, "", 0, httpOpts)
                        if err != nil {
                            return fmt.Errorf("sig download: %w", err)
                        }
                        // Cert can come from recipe or env KEYSTONE_LEAF_CERT
                        certPath := os.Getenv("KEYSTONE_LEAF_CERT")
                        if adef.CertURI != "" {
                            cp, _, err := artifact.Ensure(artDir, adef.CertURI, "", 0, httpOpts)
                            if err != nil {
                                return fmt.Errorf("cert download: %w", err)
                            }
                            certPath = cp
                        }
					if certPath == "" {
						return fmt.Errorf("no certificate specified for signature verification")
					}
					if err := security.VerifyDetached(path, sigPath, certPath, a.trustPool); err != nil {
						return fmt.Errorf("signature verify failed for %s: %w", filepath.Base(path), err)
					}
				}
                    if adef.Unpack {
                        marker := filepath.Join(workDir, ".unpacked-"+filepath.Base(path))
                        if _, err := os.Stat(marker); os.IsNotExist(err) {
                            if err := artifact.Unpack(path, workDir); err != nil {
                                return err
                            }
                            _ = os.MkdirAll(filepath.Dir(marker), 0o755)
                            _ = os.WriteFile(marker, []byte(time.Now().Format(time.RFC3339)), 0o644)
                        }
                    }
                }
			// If not unpacked, ensure working dir exists
			if _, err := os.Stat(workDir); os.IsNotExist(err) {
				if err := os.MkdirAll(workDir, 0o755); err != nil {
					return err
				}
			}
			// Run install script if any (idempotent via .installed marker)
			installedMarker := filepath.Join(workDir, ".installed")
            if r.Lifecycle.Install.Script != "" {
                    if _, err := os.Stat(installedMarker); err == nil {
                        // already installed
                        return nil
                    }
                out, err := runShellWithOutput(ctx, workDir, r.Lifecycle.Install.Script)
                if err != nil {
                        // include trimmed output for diagnostics
                        return fmt.Errorf("install script failed: %v\n--- output ---\n%s", err, out)
                    }
                    _ = os.WriteFile(installedMarker, []byte(time.Now().Format(time.RFC3339)), 0o644)
                    return nil
            }
			return nil
		}
		startFn := func(ctx context.Context) error {
			// Build env
			var env []string
			for k, v := range r.Lifecycle.Run.Exec.Env {
				env = append(env, fmt.Sprintf("%s=%s", k, v))
			}
			// Health config
			hc := runner.HealthConfig{}
			if r.Lifecycle.Run.Health.Check != "" {
				hc.Check = r.Lifecycle.Run.Health.Check
				healthBasedReady = true
			}
			if d, err := time.ParseDuration(defaultString(r.Lifecycle.Run.Health.Interval, "10s")); err == nil {
				hc.Interval = d
			}
			if d, err := time.ParseDuration(defaultString(r.Lifecycle.Run.Health.Timeout, "3s")); err == nil {
				hc.Timeout = d
			}
			if r.Lifecycle.Run.Health.FailureThreshold > 0 {
				hc.FailureThreshold = r.Lifecycle.Run.Health.FailureThreshold
			}
			// Restart policy
			rp := runner.RestartPolicy(r.Lifecycle.Run.RestartPolicy)
			if rp == "" {
				rp = runner.RestartAlways
			}

			// Validate command availability early to surface clear errors
			cmdPath := r.Lifecycle.Run.Exec.Command
			if cmdPath == "" {
				return fmt.Errorf("empty run.exec.command")
			}
			// If relative like ./bin, ensure it exists under workDir
			if strings.HasPrefix(cmdPath, "./") {
				abs := filepath.Join(workDir, strings.TrimPrefix(cmdPath, "./"))
				if st, err := os.Stat(abs); err != nil || st.IsDir() {
					return fmt.Errorf("run command not found: %s", abs)
				}
			}

			opts := runner.Options{
				Name:       it.Name,
				Command:    r.Lifecycle.Run.Exec.Command,
				Args:       r.Lifecycle.Run.Exec.Args,
				Env:        env,
				WorkingDir: workDir,
				NoFile:     r.Resources.OpenFiles,
			}
			log.Printf("starting component %s: cwd=%s cmd=%s args=%v", it.Name, workDir, opts.Command, opts.Args)
			// Run managed in background and capture first start handle for metrics/stop
			go func() {
				// component-specific context for stop
				ctx2, cancel := context.WithCancel(ctx)
				a.mu.Lock()
				a.cancels[it.Name] = cancel
				a.mu.Unlock()
				_ = pr.RunManaged(ctx2, it.Name, opts, hc, rp,
					func(h *runner.ProcessHandle) {
						a.mu.Lock()
						// If already present, count as restart; else first start
						if _, ok := a.procs[it.Name]; ok {
							if ci, ok2 := a.comps.Get(it.Name); ok2 {
								ci.Restarts++
								a.comps.Upsert(ci)
							}
							metrics.IncRestarts(it.Name)
						}
						a.procs[it.Name] = h
						// set PID on store
						ci, ok2 := a.comps.Get(it.Name)
						if ok2 {
							ci.PID = h.PID
							a.comps.Upsert(ci)
						}
						a.mu.Unlock()
						// signal readiness once on first successful start (if no health check)
						if !healthBasedReady {
							startedOnce.Do(func() {
								if ch, ok := compReady[it.Name]; ok && ch != nil {
									close(ch)
								}
							})
						}
						go metrics.SampleProcessMetrics(ctx2, it.Name, h.PID)
					},
					func(ok bool) {
						// last health status update
						a.mu.Lock()
						ci, ok2 := a.comps.Get(it.Name)
						if ok2 {
							if ok {
								ci.LastHealth = "healthy"
							} else {
								ci.LastHealth = "unhealthy"
							}
							a.comps.Upsert(ci)
						}
						a.mu.Unlock()
						metrics.SetHealthy(it.Name, ok)
						if healthBasedReady && ok {
							startedOnce.Do(func() {
								if ch, ok := compReady[it.Name]; ok && ch != nil {
									close(ch)
								}
							})
						}
					},
				)
			}()
			return nil
		}
		stopFn := func(ctx context.Context) error {
			a.mu.RLock()
			h := a.procs[it.Name]
			a.mu.RUnlock()
			if h == nil {
				return nil
			}
			return pr.Stop(ctx, h, 10*time.Second)
		}
		// Create component with readiness channel and register it
		readyCh := make(chan struct{})
		c := supervisor.NewComponent(it.Name, depNames, installFn, startFn, stopFn)
		c.ReadyCh = readyCh
		comps = append(comps, c)
		compReady[it.Name] = readyCh
	}
	// If dry-run, set status and return after printing order
	if a.dryRun {
		a.mu.Lock()
		a.planComps = planMap
		a.planStatus = "dry-run"
		a.planErr = ""
		a.mu.Unlock()
		a.persistSnapshot()
		// Log order
		edges := map[string][]string{}
		indeg := map[string]int{}
		for _, pc := range planMap {
			for _, d := range pc.Deps {
				edges[d] = append(edges[d], pc.Name)
				indeg[pc.Name]++
			}
			if _, ok := indeg[pc.Name]; !ok {
				indeg[pc.Name] = 0
			}
		}
		order := topoOrder(edges, indeg)
		log.Printf("dry-run plan order: %v", order)
		return nil
	}

	// Update store initially
	for _, c := range comps {
		a.comps.Upsert(store.ComponentInfo{Name: c.Name, State: string(c.State())})
	}
    // Poll states to store and component-state metric
    go func() {
        t := time.NewTicker(500 * time.Millisecond)
        defer t.Stop()
        for range t.C {
            if a.closed.Load() {
                return
            }
            for _, c := range comps {
                // Derive running/stopped from presence of a managed process handle.
                a.mu.RLock()
                _, running := a.procs[c.Name]
                a.mu.RUnlock()
                st := "stopped"
                if running {
                    st = "running"
                }
                a.comps.Upsert(store.ComponentInfo{Name: c.Name, State: st})
                // read health for label
                ci, _ := a.comps.Get(c.Name)
                metrics.ObserveComponentState(c.Name, st)
                metrics.ObserveComponentStateWithHealth(c.Name, st, ci.LastHealth)
            }
            a.persistSnapshot()
        }
    }()

	err = supervisor.StartStack(context.Background(), comps)
	a.mu.Lock()
	if err != nil {
		a.planStatus = "failed"
		a.planErr = err.Error()
	} else {
		a.planStatus = "running"
		a.planErr = ""
	}
	a.mu.Unlock()
	a.persistSnapshot()
	// GC artifacts best-effort: keep ones used by this plan
	keep := make(map[string]struct{})
	for _, it := range p.Components {
		// keep all artifact dirs under component name
		rcp, _ := recipe.Load(it.RecipePath)
		if rcp != nil {
			dir := fmt.Sprintf("%s/%s", rcp.Metadata.Name, rcp.Metadata.Version)
			keep[dir] = struct{}{}
		}
	}
	_ = artifact.GC("runtime/artifacts", keep)
	// Enforce artifact cache budget
	_ = artifact.EnforceCacheLimit("runtime/artifacts", a.artifactCacheLimit)
	// Save plan mapping
	a.mu.Lock()
	a.planComps = planMap
	a.mu.Unlock()
	a.persistSnapshot()
	return err
}

func defaultString(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func (a *Agent) persistSnapshot() {
	snap := state.Snapshot{
		Plan: state.PlanStatus{
			Path:    a.planPath,
			Status:  a.planStatus,
			Error:   a.planErr,
			Updated: time.Now(),
		},
		Components:     a.comps.List(),
		PlanComponents: a.planComps,
	}
	_ = state.Save(a.stateDir, snap)
}

// restartFromPlan restarts a single component using the current plan's recipe.
func (a *Agent) restartFromPlan(name string) error {
	if a.planPath == "" {
		return fmt.Errorf("no plan applied")
	}
	p, err := deploy.Load(a.planPath)
	if err != nil {
		return err
	}
	var recPath string
	for _, c := range p.Components {
		if c.Name == name {
			recPath = c.RecipePath
			break
		}
	}
	if recPath == "" {
		return fmt.Errorf("component %q not found in plan", name)
	}
	r, err := recipe.Load(recPath)
	if err != nil {
		return err
	}

	workDir := fmt.Sprintf("runtime/components/%s/%s", r.Metadata.Name, r.Metadata.Version)
	artDir := fmt.Sprintf("runtime/artifacts/%s/%s", r.Metadata.Name, r.Metadata.Version)
	// Ensure artifacts and (optional) unpack
    for _, adef := range r.Artifacts {
        httpOpts := artifact.HTTPOptions{Headers: adef.Headers, GithubToken: adef.GithubToken}
        path, _, err := artifact.Ensure(artDir, adef.URI, adef.SHA256, 0, httpOpts)
        if err != nil {
            return err
        }
        if adef.Unpack {
            marker := filepath.Join(workDir, ".unpacked-"+filepath.Base(path))
            if _, err := os.Stat(marker); os.IsNotExist(err) {
                if err := artifact.Unpack(path, workDir); err != nil {
                    return err
                }
                _ = os.MkdirAll(filepath.Dir(marker), 0o755)
                _ = os.WriteFile(marker, []byte(time.Now().Format(time.RFC3339)), 0o644)
            }
        }
    }
	if _, err := os.Stat(workDir); os.IsNotExist(err) {
		if err := os.MkdirAll(workDir, 0o755); err != nil {
			return err
		}
	}
    if r.Lifecycle.Install.Script != "" {
        installedMarker := filepath.Join(workDir, ".installed")
        if _, err := os.Stat(installedMarker); os.IsNotExist(err) {
            out, err := runShellWithOutput(context.Background(), workDir, r.Lifecycle.Install.Script)
            if err != nil {
                return fmt.Errorf("install script failed: %v\n--- output ---\n%s", err, out)
            }
            _ = os.WriteFile(installedMarker, []byte(time.Now().Format(time.RFC3339)), 0o644)
        }
    }
	// Start managed
	pr := runner.New()
	var env []string
	for k, v := range r.Lifecycle.Run.Exec.Env {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}
	hc := runner.HealthConfig{}
	if r.Lifecycle.Run.Health.Check != "" {
		hc.Check = r.Lifecycle.Run.Health.Check
	}
	if d, err := time.ParseDuration(defaultString(r.Lifecycle.Run.Health.Interval, "10s")); err == nil {
		hc.Interval = d
	}
	if d, err := time.ParseDuration(defaultString(r.Lifecycle.Run.Health.Timeout, "3s")); err == nil {
		hc.Timeout = d
	}
	if r.Lifecycle.Run.Health.FailureThreshold > 0 {
		hc.FailureThreshold = r.Lifecycle.Run.Health.FailureThreshold
	}
	rp := runner.RestartPolicy(r.Lifecycle.Run.RestartPolicy)
	if rp == "" {
		rp = runner.RestartAlways
	}
	// Validate command presence if relative
	if cmd := r.Lifecycle.Run.Exec.Command; strings.HasPrefix(cmd, "./") {
		abs := filepath.Join(workDir, strings.TrimPrefix(cmd, "./"))
		if st, err := os.Stat(abs); err != nil || st.IsDir() {
			return fmt.Errorf("run command not found: %s", abs)
		}
	}
	opts := runner.Options{Name: name, Command: r.Lifecycle.Run.Exec.Command, Args: r.Lifecycle.Run.Exec.Args, Env: env, WorkingDir: workDir, NoFile: r.Resources.OpenFiles}
	log.Printf("restarting component %s: cwd=%s cmd=%s args=%v", name, workDir, opts.Command, opts.Args)
	go func() {
		ctx2, cancel := context.WithCancel(context.Background())
		a.mu.Lock()
		a.cancels[name] = cancel
		a.mu.Unlock()
		_ = pr.RunManaged(ctx2, name, opts, hc, rp,
			func(h *runner.ProcessHandle) {
				a.mu.Lock()
				// On restart (existing), increment counters
				if _, ok := a.procs[name]; ok {
					if ci, ok2 := a.comps.Get(name); ok2 {
						ci.Restarts++
						a.comps.Upsert(ci)
					}
					metrics.IncRestarts(name)
				}
				a.procs[name] = h
				ci, ok2 := a.comps.Get(name)
				if ok2 {
					ci.PID = h.PID
					a.comps.Upsert(ci)
				}
				a.mu.Unlock()
				go metrics.SampleProcessMetrics(ctx2, name, h.PID)
			},
			func(ok bool) {
				a.mu.Lock()
				ci, ok2 := a.comps.Get(name)
				if ok2 {
					if ok {
						ci.LastHealth = "healthy"
					} else {
						ci.LastHealth = "unhealthy"
					}
					a.comps.Upsert(ci)
				}
				a.mu.Unlock()
				metrics.SetHealthy(name, ok)
			},
		)
		// Enforce artifact cache budget after (re)start
		_ = artifact.EnforceCacheLimit("runtime/artifacts", a.artifactCacheLimit)
	}()
	return nil
}

// stopComponent cancels managed loop and stops process, updating store.
func (a *Agent) stopComponent(name string) {
	log.Printf("agent: stopComponent start name=%s", name)
	// Grab references under lock, but perform blocking ops outside the lock
	a.mu.Lock()
	cancel := a.cancels[name]
	var pid int
	var h *runner.ProcessHandle
	if cancel != nil {
		delete(a.cancels, name)
	}
	if ph, ok := a.procs[name]; ok && ph != nil {
		h = ph
		pid = ph.PID
		delete(a.procs, name)
	}
	a.mu.Unlock()

	if cancel != nil {
		log.Printf("agent: cancel managed loop for %s", name)
		cancel()
	}
    if h != nil {
        log.Printf("agent: stopping process %s pid=%d", name, pid)
        if err := (&runner.ProcessRunner{}).Stop(context.Background(), h, 5*time.Second); err != nil {
            log.Printf("agent: stopComponent Stop error name=%s pid=%d err=%v", name, pid, err)
        }
    }
	// Update store (no need to hold a.mu here)
	ci, ok := a.comps.Get(name)
	if ok {
		ci.State = "stopped"
		a.comps.Upsert(ci)
	}
	log.Printf("agent: stopComponent done name=%s", name)
}

// planDependentsTopological returns dependents of 'name' in topological order (closest first -> farthest last).
func (a *Agent) planDependentsTopological(name string) []string {
	// Build graph: dep -> comp
	edges := map[string][]string{}
	indeg := map[string]int{}
	nodes := map[string]struct{}{}
	for _, pc := range a.planComps {
		nodes[pc.Name] = struct{}{}
	}
	for _, pc := range a.planComps {
		for _, d := range pc.Deps {
			edges[d] = append(edges[d], pc.Name)
			indeg[pc.Name]++
		}
	}
	// Collect all dependents reachable from 'name'
	visited := map[string]bool{}
	var stack []string
	stack = append(stack, name)
	for len(stack) > 0 {
		u := stack[0]
		stack = stack[1:]
		for _, v := range edges[u] {
			if !visited[v] {
				visited[v] = true
				stack = append(stack, v)
			}
		}
	}
	// Topological order among subgraph of dependents
	// Filter indegrees to subgraph only
	subIn := map[string]int{}
	for n := range visited {
		subIn[n] = 0
	}
	subEdges := map[string][]string{}
	for d, outs := range edges {
		for _, v := range outs {
			if visited[d] && visited[v] {
				subEdges[d] = append(subEdges[d], v)
				subIn[v]++
				if _, ok := subIn[d]; !ok {
					subIn[d] = 0
				}
			}
		}
	}
	// Kahn
	var q []string
	for n, deg := range subIn {
		if deg == 0 {
			q = append(q, n)
		}
	}
	var order []string
	for len(q) > 0 {
		u := q[0]
		q = q[1:]
		order = append(order, u)
		for _, v := range subEdges[u] {
			subIn[v]--
			if subIn[v] == 0 {
				q = append(q, v)
			}
		}
	}
	// order now lists dependents in topological order from nearer roots; for stopping, we stop in reverse
	// but the restart handler stops dependents first in the given order; to be safe, reverse for stopping
	for i, j := 0, len(order)-1; i < j; i, j = i+1, j-1 {
		order[i], order[j] = order[j], order[i]
	}
	return order
}

// topoOrder returns a topological order given edges and indegrees (maps).
func topoOrder(edges map[string][]string, indeg map[string]int) []string {
	// copy indeg
	in := map[string]int{}
	for k, v := range indeg {
		in[k] = v
	}
	var q []string
	for n, d := range in {
		if d == 0 {
			q = append(q, n)
		}
	}
	var order []string
	for len(q) > 0 {
		u := q[0]
		q = q[1:]
		order = append(order, u)
		for _, v := range edges[u] {
			in[v]--
			if in[v] == 0 {
				q = append(q, v)
			}
		}
	}
	return order
}

// ApplyPlanAPI runs ApplyPlan with optional dry-run override.
func (a *Agent) ApplyPlanAPI(planPath string, dry bool) error {
	if dry {
		saved := a.dryRun
		a.dryRun = true
		defer func() { a.dryRun = saved }()
	}
	return a.ApplyPlan(planPath)
}

// Helpers for synchronous restart
func (a *Agent) currentPID(name string) int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if h := a.procs[name]; h != nil {
		return h.PID
	}
	return 0
}

// waitReady waits until a component has a PID (wait=pid) or reports healthy (wait=health).
// mode: "pid" (default) or "health". timeout caps wait time.
func (a *Agent) waitReady(name, mode string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	lastLog := time.Now().Add(-10 * time.Second)
	for {
		if mode == "health" {
			if ci, ok := a.comps.Get(name); ok && ci.LastHealth == "healthy" {
				return nil
			}
		} else { // pid
			if pid := a.currentPID(name); pid > 0 {
				return nil
			}
		}
		if time.Since(lastLog) >= 5*time.Second {
			// periodic progress log
			pid := a.currentPID(name)
			ci, _ := a.comps.Get(name)
			log.Printf("waitReady: name=%s mode=%s pid=%d last_health=%s remaining=%s", name, mode, pid, ci.LastHealth, time.Until(deadline).Truncate(time.Second))
			lastLog = time.Now()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("wait for %s %s timed out after %s", name, mode, timeout)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func parseDurationDefault(s string, d time.Duration) time.Duration {
	if s == "" {
		return d
	}
	if dd, err := time.ParseDuration(s); err == nil && dd > 0 {
		return dd
	}
	return d
}

// runShellWithOutput runs a shell script in the given working directory and returns trimmed combined output.
func runShellWithOutput(ctx context.Context, workDir, script string) (string, error) {
    cmd := exec.CommandContext(ctx, "/bin/sh", "-c", script)
    cmd.Dir = workDir
    out, err := cmd.CombinedOutput()
    const limit = 8192 // cap output to 8KiB
    if len(out) > limit {
        // keep tail
        out = out[len(out)-limit:]
    }
    return string(out), err
}
