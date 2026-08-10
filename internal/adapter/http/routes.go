package http

import (
	"net/http"

	"github.com/carlosprados/keystone/internal/adapter"
	"github.com/carlosprados/keystone/internal/store"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Route describes one endpoint of the HTTP control plane.
//
// This table is the single source of truth: buildRouter registers from it, and
// cmd/openapi-gen reflects over it to produce the published OpenAPI document. An
// endpoint therefore cannot exist without appearing in the spec, and a response
// type cannot change shape without the generated spec changing with it.
//
// Request and Response hold a pointer to the very struct the handler encodes
// (`new(adapter.PlanStatus)`), not a copy of its fields. That is what keeps the
// documented schema honest — it is reflected from the real type at generation
// time. They are plain `any` so this package stays free of any OpenAPI
// dependency; only the generator command imports one.
type Route struct {
	// Pattern is what gets registered on the mux (a trailing slash means prefix).
	Pattern string
	// Path is how the endpoint is addressed, with {placeholders} for the parts
	// the handler parses out of the pattern.
	Path string
	// Methods the handler accepts. Empty means the endpoint is not part of the
	// documented API surface (the root landing page).
	Methods []string
	// Summary is a one-line description for the spec.
	Summary string
	// Description is optional prose for the spec.
	Description string
	// Request is a pointer to the request body type, or nil. RawTOML marks the
	// endpoints that take a TOML document rather than JSON.
	Request any
	// RawTOML says the request body is a TOML document (a plan or a recipe).
	RawTOML bool
	// Response is a pointer to the success response body type, or nil for the
	// endpoints that answer 204 with no content.
	Response any
	// ResponseAlt is a second success body the same endpoint can return when a
	// parameter changes its shape — :restart answers a RestartDryResult under
	// dry=true and a RestartResult otherwise. Documenting only one of the two
	// would make the spec describe a response the binary sometimes does not
	// send.
	ResponseAlt any
	// SuccessStatus is the status code on success.
	SuccessStatus int
	// Params documents the query and path parameters.
	Params []Param
	// Errors documents the failure statuses this endpoint can return.
	Errors []int
	// NoAuth marks endpoints exempt from the bearer token.
	NoAuth bool
	// PlainText marks non-JSON responses (metrics, the landing page).
	PlainText bool

	handler http.HandlerFunc
	raw     http.Handler
}

// Param documents a query or path parameter.
type Param struct {
	Name        string
	In          string // "query" or "path"
	Description string
	Required    bool
	Example     string
}

// routes returns the endpoint table with handlers attached.
func (a *Adapter) routes() []Route {
	return []Route{
		{
			Pattern:       "/healthz",
			Path:          "/healthz",
			Methods:       []string{http.MethodGet},
			Summary:       "Agent liveness",
			Description:   "Exempt from authentication and free of component detail, so it is safe for an unauthenticated probe.",
			Response:      new(adapter.HealthStatus),
			SuccessStatus: http.StatusOK,
			NoAuth:        true,
			handler:       a.handleHealthz,
		},
		{
			Pattern:       "/metrics",
			Path:          "/metrics",
			Methods:       []string{http.MethodGet},
			Summary:       "Prometheus metrics",
			Description:   "Component state, health, restarts, and per-process CPU and memory.",
			SuccessStatus: http.StatusOK,
			PlainText:     true,
			raw:           promhttp.Handler(),
		},
		{
			Pattern:       "/v1/components",
			Path:          "/v1/components",
			Methods:       []string{http.MethodGet},
			Summary:       "List components",
			Description:   "A component reported running has a live supervision loop and, for a process, a live PID. See the component state contract in the documentation.",
			Response:      new([]store.ComponentInfo),
			SuccessStatus: http.StatusOK,
			handler:       a.handleComponents,
		},
		{
			Pattern:       "/v1/components/{name}:stop",
			Path:          "/v1/components/{name}:stop",
			Methods:       []string{http.MethodPost},
			Summary:       "Stop one component",
			Description:   "Stops a single component. Its dependents are left running.",
			SuccessStatus: http.StatusNoContent,
			Params: []Param{
				{Name: "name", In: "path", Description: "Component name as given in the plan", Required: true, Example: "api"},
			},
			Errors:  []int{http.StatusBadRequest, http.StatusInternalServerError},
			handler: a.handleComponentAction,
		},
		{
			Pattern:       "/v1/components/{name}:restart",
			Path:          "/v1/components/{name}:restart",
			Methods:       []string{http.MethodPost},
			Summary:       "Restart one component",
			Description:   "Restarts a component and cascades to its dependents according to each dependency type: hard and soft cascade, ordering does not. With dry=true the response is the planned order instead (stopOrder/startOrder).",
			Response:      new(adapter.RestartResult),
			ResponseAlt:   new(adapter.RestartDryResult),
			SuccessStatus: http.StatusOK,
			Params: []Param{
				{Name: "name", In: "path", Description: "Component name as given in the plan", Required: true, Example: "api"},
				{Name: "wait", In: "query", Description: "pid (default) returns once a new PID exists; health returns once it probes healthy", Example: "health"},
				{Name: "timeout", In: "query", Description: "How long to wait, as a Go duration. Default 60s", Example: "120s"},
				{Name: "dry", In: "query", Description: "true reports what would be restarted and changes nothing", Example: "true"},
			},
			Errors:  []int{http.StatusBadRequest, http.StatusGatewayTimeout, http.StatusInternalServerError},
			handler: a.handleComponentAction,
		},
		{
			Pattern:       "/v1/recipes",
			Path:          "/v1/recipes",
			Methods:       []string{http.MethodGet, http.MethodPost},
			Summary:       "List or add recipes",
			Description:   "GET lists name:version entries in the agent's recipe store. POST stores a recipe TOML document so plans can refer to it as name:version; recipes added this way are trusted through API authentication rather than a file signature.",
			RawTOML:       true,
			Response:      new([]string),
			SuccessStatus: http.StatusOK,
			Params: []Param{
				{Name: "force", In: "query", Description: "true overwrites an existing recipe with the same name and version", Example: "true"},
			},
			Errors:  []int{http.StatusBadRequest, http.StatusConflict, http.StatusInternalServerError},
			handler: a.handleRecipes,
		},
		{
			Pattern:       "/v1/recipes/{name}/{version}",
			Path:          "/v1/recipes/{name}/{version}",
			Methods:       []string{http.MethodDelete},
			Summary:       "Delete a recipe",
			Description:   "Removes a recipe from the store. Name and version are validated against an allow-list, so neither can traverse a path.",
			SuccessStatus: http.StatusNoContent,
			Params: []Param{
				{Name: "name", In: "path", Description: "Recipe metadata.name", Required: true, Example: "com.acme.api"},
				{Name: "version", In: "path", Description: "Recipe metadata.version", Required: true, Example: "1.4.0"},
			},
			Errors:  []int{http.StatusBadRequest},
			handler: a.handleRecipeDelete,
		},
		{
			Pattern:       "/v1/plan/status",
			Path:          "/v1/plan/status",
			Methods:       []string{http.MethodGet},
			Summary:       "Plan status",
			Description:   "The applied plan, its status, the last error if any, and the component list — so a poller needs one request rather than two.",
			Response:      new(adapter.PlanStatus),
			SuccessStatus: http.StatusOK,
			handler:       a.handlePlanStatus,
		},
		{
			Pattern:       "/v1/plan/graph",
			Path:          "/v1/plan/graph",
			Methods:       []string{http.MethodGet},
			Summary:       "Dependency graph",
			Description:   "Nodes, edges (dependency to dependents) and a valid topological start order.",
			Response:      new(adapter.GraphInfo),
			SuccessStatus: http.StatusOK,
			handler:       a.handlePlanGraph,
		},
		{
			Pattern:       "/v1/plan/apply",
			Path:          "/v1/plan/apply",
			Methods:       []string{http.MethodPost},
			Summary:       "Apply a plan",
			Description:   "The body is the plan TOML itself. A JSON body naming a planPath is rejected: letting the API name a local path would turn it into a file-read primitive. Applying an unchanged plan is a no-op — unchanged components that are alive and supervised are left running.",
			RawTOML:       true,
			SuccessStatus: http.StatusAccepted,
			Params: []Param{
				{Name: "dry", In: "query", Description: "true validates and reports the reconcile without installing or starting anything", Example: "true"},
			},
			Errors:  []int{http.StatusBadRequest, http.StatusInternalServerError},
			handler: a.handlePlanApply,
		},
		{
			Pattern:       "/v1/plan/stop",
			Path:          "/v1/plan/stop",
			Methods:       []string{http.MethodPost},
			Summary:       "Stop every component",
			Description:   "Stops the whole plan in reverse dependency order and records the plan as stopped, which the agent remembers across reboots: it will not resume a plan you stopped.",
			SuccessStatus: http.StatusNoContent,
			Errors:        []int{http.StatusInternalServerError},
			handler:       a.handlePlanStop,
		},
		{
			// Not part of the documented API: a human landing page.
			Pattern:   "/",
			Path:      "/",
			PlainText: true,
			NoAuth:    false,
			handler:   a.handleRoot,
		},
	}
}

// DocumentedRoutes returns the endpoints that belong in the OpenAPI document:
// everything with at least one method, which excludes the landing page.
//
// It takes no adapter, so the generator can call it without constructing one.
func DocumentedRoutes() []Route {
	var out []Route
	for _, r := range (&Adapter{}).routes() {
		if len(r.Methods) == 0 {
			continue
		}
		r.handler, r.raw = nil, nil
		out = append(out, r)
	}
	return out
}

// buildRouter creates the HTTP router from the route table.
//
// Several endpoints share one mux pattern — /v1/components/ handles both :stop
// and :restart, /v1/recipes/ handles the delete — so patterns are registered
// once, on their first appearance.
func (a *Adapter) buildRouter() *http.ServeMux {
	mux := http.NewServeMux()
	registered := map[string]bool{}

	for _, r := range a.routes() {
		pattern := muxPattern(r.Pattern)
		if registered[pattern] {
			continue
		}
		registered[pattern] = true
		switch {
		case r.raw != nil:
			mux.Handle(pattern, r.raw)
		case r.handler != nil:
			mux.HandleFunc(pattern, r.handler)
		}
	}
	return mux
}

// muxPattern turns a documented path into the pattern the handlers expect. The
// handlers parse names and actions out of the URL themselves, so anything with a
// placeholder is registered as a prefix.
func muxPattern(path string) string {
	switch {
	case path == "/v1/components/{name}:stop", path == "/v1/components/{name}:restart":
		return "/v1/components/"
	case path == "/v1/recipes/{name}/{version}":
		return "/v1/recipes/"
	default:
		return path
	}
}
