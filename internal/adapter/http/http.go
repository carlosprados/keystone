// Package http provides an HTTP adapter for the Keystone agent.
// It exposes a REST API for controlling the agent and its components.
package http

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/carlosprados/keystone/internal/adapter"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Adapter implements the HTTP REST API for the agent.
type Adapter struct {
	addr    string
	handler adapter.CommandHandler
	server  *http.Server
}

// Config holds the configuration for the HTTP adapter.
type Config struct {
	Addr string // Listen address (e.g., ":8080")
}

// New creates a new HTTP adapter.
func New(cfg Config, handler adapter.CommandHandler) *Adapter {
	return &Adapter{
		addr:    cfg.Addr,
		handler: handler,
	}
}

// Name returns the adapter identifier.
func (a *Adapter) Name() string {
	return "http"
}

// Start initializes the HTTP server and begins listening.
func (a *Adapter) Start(ctx context.Context) error {
	mux := a.buildRouter()

	a.server = &http.Server{
		Addr:    a.addr,
		Handler: mux,
	}

	log.Printf("[http] starting HTTP adapter on %s", a.addr)

	// Start server in goroutine
	errCh := make(chan error, 1)
	go func() {
		if err := a.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()

	// Give the server a moment to start or fail
	select {
	case err := <-errCh:
		return fmt.Errorf("http server failed to start: %w", err)
	case <-time.After(100 * time.Millisecond):
		// Server started successfully
		return nil
	}
}

// Stop gracefully shuts down the HTTP server.
func (a *Adapter) Stop(ctx context.Context) error {
	if a.server == nil {
		return nil
	}
	log.Printf("[http] stopping HTTP adapter")
	return a.server.Shutdown(ctx)
}

// buildRouter creates the HTTP router with all endpoints.
func (a *Adapter) buildRouter() *http.ServeMux {
	mux := http.NewServeMux()

	// Health and metrics
	mux.HandleFunc("/healthz", a.handleHealthz)
	mux.Handle("/metrics", promhttp.Handler())

	// Components
	mux.HandleFunc("/v1/components", a.handleComponents)
	mux.HandleFunc("/v1/components/", a.handleComponentAction)

	// Recipes
	mux.HandleFunc("/v1/recipes", a.handleRecipes)
	mux.HandleFunc("/v1/recipes/", a.handleRecipeDelete)

	// Plan
	mux.HandleFunc("/v1/plan/status", a.handlePlanStatus)
	mux.HandleFunc("/v1/plan/graph", a.handlePlanGraph)
	mux.HandleFunc("/v1/plan/apply", a.handlePlanApply)
	mux.HandleFunc("/v1/plan/stop", a.handlePlanStop)

	// Root
	mux.HandleFunc("/", a.handleRoot)

	return mux
}

// handleRoot serves the landing page.
func (a *Adapter) handleRoot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("Keystone agent is running. See /healthz, /metrics and /v1/components\n"))
}

// handleHealthz returns the agent health status.
func (a *Adapter) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(a.handler.GetHealth())
}

// handleComponents returns the list of managed components.
func (a *Adapter) handleComponents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(a.handler.GetComponents())
}

// handleRecipes handles GET (list) and POST (add) for recipes.
func (a *Adapter) handleRecipes(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		list, err := a.handler.ListRecipes()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(list)

	case http.MethodPost:
		force := r.URL.Query().Get("force") == "true"
		content, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		name, version, err := a.handler.AddRecipe(string(content), force)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":  "created",
			"name":    name,
			"version": version,
		})

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleRecipeDelete handles DELETE /v1/recipes/{name}/{version}
func (a *Adapter) handleRecipeDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/v1/recipes/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 {
		http.Error(w, "invalid path, expected /v1/recipes/{name}/{version}", http.StatusBadRequest)
		return
	}

	name, version := parts[0], parts[1]
	if err := a.handler.DeleteRecipe(name, version); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleComponentAction handles POST /v1/components/{name}:stop and :restart
func (a *Adapter) handleComponentAction(w http.ResponseWriter, r *http.Request) {
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
		if err := a.handler.StopComponent(name); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	case "restart":
		// Dry-run: return planned order without executing
		if r.URL.Query().Get("dry") == "true" {
			result := a.handler.RestartComponentDry(name)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(result)
			return
		}

		// Parse wait mode and timeout
		wait := strings.ToLower(r.URL.Query().Get("wait"))
		if wait == "" {
			wait = "pid"
		}
		timeout := parseDurationDefault(r.URL.Query().Get("timeout"), 60*time.Second)

		log.Printf("[http] component=%s wait=%s timeout=%v msg=restarting component", name, wait, timeout)

		result, err := a.handler.RestartComponent(name, wait, timeout)
		if err != nil {
			log.Printf("[http] component=%s error=%v msg=restart failed", name, err)
			if strings.Contains(err.Error(), "timed out") {
				http.Error(w, err.Error(), http.StatusGatewayTimeout)
			} else {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		}

		log.Printf("[http] component=%s pid=%d msg=restart completed", name, result.PID)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	}
}

// handlePlanStatus returns the current plan status.
func (a *Adapter) handlePlanStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(a.handler.GetPlanStatus())
}

// handlePlanGraph returns the dependency graph.
func (a *Adapter) handlePlanGraph(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(a.handler.GetPlanGraph())
}

// handlePlanApply applies a deployment plan.
func (a *Adapter) handlePlanApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	dry := r.URL.Query().Get("dry") == "true"

	// Try to parse as JSON first (legacy format)
	var req struct {
		PlanPath string `json:"planPath"`
		Dry      bool   `json:"dry"`
	}
	if json.Unmarshal(body, &req) == nil && req.PlanPath != "" {
		if err := a.handler.ApplyPlan(req.PlanPath, req.Dry || dry); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		return
	}

	// Fallback to raw TOML content
	if err := a.handler.ApplyPlanContent(string(body), dry); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// handlePlanStop stops all running components.
func (a *Adapter) handlePlanStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	if err := a.handler.StopPlan(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
