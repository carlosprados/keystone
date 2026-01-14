package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/carlosprados/keystone/internal/runner"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog/log"
)

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
			log.Info().Str("component", name).Strs("dependents", depsOrder).Msg("restart: stopping dependents")
			for _, dn := range depsOrder {
				log.Info().Str("dependent", dn).Msg("stopping dependent")
				a.stopComponent(dn)
			}
			// stop target
			log.Info().Str("target", name).Msg("stopping target")
			a.stopComponent(name)
			// start target
			log.Info().Str("target", name).Msg("starting target")
			if err := a.restartFromPlan(name); err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(err.Error()))
				log.Error().Str("target", name).Err(err).Msg("restart failed to start")
				return
			}
			// wait target
			log.Info().Str("target", name).Str("mode", mode).Dur("timeout", to).Msg("waiting for target")
			if err := a.waitReady(name, mode, to); err != nil {
				w.WriteHeader(http.StatusGatewayTimeout)
				_, _ = w.Write([]byte(err.Error()))
				log.Error().Str("target", name).Err(err).Msg("restart wait failed")
				return
			}
			log.Info().Str("target", name).Int("pid", a.currentPID(name)).Msg("target ready")
			// start dependents in order and wait for each (best-effort)
			depPIDs := map[string]int{}
			for _, dn := range depsOrder {
				log.Info().Str("dependent", dn).Msg("starting dependent")
				_ = a.restartFromPlan(dn)
				if err := a.waitReady(dn, mode, to); err != nil {
					log.Warn().Str("dependent", dn).Err(err).Msg("dependent not ready")
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

func parseDurationDefault(s string, d time.Duration) time.Duration {
	if s == "" {
		return d
	}
	if dd, err := time.ParseDuration(s); err == nil && dd > 0 {
		return dd
	}
	return d
}
