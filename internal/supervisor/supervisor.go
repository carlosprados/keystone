package supervisor

// Minimal supervisor skeleton with a simple DAG and component lifecycle hooks.
// This is intentionally stdlib-only to keep the MVP self-contained.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// State represents the current state of a component.
type State string

const (
	StateNone       State = "none"
	StateInstalling State = "installing"
	StateStarting   State = "starting"
	StateRunning    State = "running"
	StateStopping   State = "stopping"
	StateStopped    State = "stopped"
	StateFailed     State = "failed"
)

// Component models a managed unit with lifecycle callbacks and dependencies.
type Component struct {
	Name      string
	Deps      []string
	mu        sync.Mutex
	state     State
	InstallFn func(ctx context.Context) error
	StartFn   func(ctx context.Context) error
	StopFn    func(ctx context.Context) error
	// ReadyCh is an optional channel that should be closed by the component
	// implementation when the service has actually started (e.g., child
	// process spawned). If set, Start will wait for it up to ReadyTimeout
	// before transitioning to Running.
	ReadyCh      chan struct{}
	ReadyTimeout time.Duration
}

// NewComponent creates a component with the provided hooks.
func NewComponent(name string, deps []string, install, start, stop func(context.Context) error) *Component {
	return &Component{Name: name, Deps: deps, state: StateNone, InstallFn: install, StartFn: start, StopFn: stop, ReadyTimeout: 15 * time.Second}
}

// State returns the current component state.
func (c *Component) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// setState changes state with logging.
func (c *Component) setState(s State) {
	c.state = s
	log.Info().Str("component", c.Name).Str("state", string(s)).Msg("state change")
}

// Install runs the install hook when appropriate.
func (c *Component) Install(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != StateNone && c.state != StateStopped {
		return nil
	}
	c.setState(StateInstalling)
	if c.InstallFn != nil {
		if err := c.InstallFn(ctx); err != nil {
			c.setState(StateFailed)
			return err
		}
	}
	c.setState(StateStopped)
	return nil
}

// Start runs the start hook and moves to running.
func (c *Component) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state == StateRunning {
		return nil
	}
	c.setState(StateStarting)
	if c.StartFn != nil {
		// release lock while running user code
		c.mu.Unlock()
		err := c.StartFn(ctx)
		c.mu.Lock()
		if err != nil {
			c.setState(StateFailed)
			return err
		}
	}
	// If a readiness channel is provided, wait for it before moving to running
	if ch := c.ReadyCh; ch != nil {
		timeout := c.ReadyTimeout
		if timeout <= 0 {
			timeout = 15 * time.Second
		}
		c.mu.Unlock()
		select {
		case <-ch:
			// ready
		case <-ctx.Done():
			c.mu.Lock()
			c.setState(StateFailed)
			// Do not unlock here; defer will release the lock acquired at function entry
			return ctx.Err()
		case <-time.After(timeout):
			c.mu.Lock()
			c.setState(StateFailed)
			// Do not unlock here; defer will release the lock acquired at function entry
			return errors.New("start readiness timeout")
		}
		c.mu.Lock()
	}
	c.setState(StateRunning)
	return nil
}

// Stop invokes the stop hook and moves to stopped.
func (c *Component) Stop(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != StateRunning && c.state != StateStarting {
		return nil
	}
	c.setState(StateStopping)
	if c.StopFn != nil {
		// release lock while running user code
		c.mu.Unlock()
		err := c.StopFn(ctx)
		c.mu.Lock()
		if err != nil {
			c.setState(StateFailed)
			return err
		}
	}
	c.setState(StateStopped)
	return nil
}

// Graph is a simple dependency graph.
type Graph struct {
	Nodes map[string]*Component
	Edges map[string][]string // from -> to (dependency -> dependent)
	InDeg map[string]int
}

// BuildGraph creates a DAG from components' dependencies.
func BuildGraph(comps []*Component) *Graph {
	g := &Graph{Nodes: map[string]*Component{}, Edges: map[string][]string{}, InDeg: map[string]int{}}
	for _, c := range comps {
		g.Nodes[c.Name] = c
		if _, ok := g.InDeg[c.Name]; !ok {
			g.InDeg[c.Name] = 0
		}
	}
	for _, c := range comps {
		for _, dep := range c.Deps {
			g.Edges[dep] = append(g.Edges[dep], c.Name)
			g.InDeg[c.Name]++
		}
	}
	return g
}

// TopoLayers returns ordered layers where each layer can start in parallel.
func (g *Graph) TopoLayers() ([][]string, error) {
	in := make(map[string]int, len(g.InDeg))
	for k, v := range g.InDeg {
		in[k] = v
	}
	var q []string
	for n, d := range in {
		if d == 0 {
			q = append(q, n)
		}
	}
	var layers [][]string
	visited := 0
	for len(q) > 0 {
		layer := append([]string{}, q...)
		layers = append(layers, layer)
		q = q[:0]
		for _, u := range layer {
			visited++
			for _, v := range g.Edges[u] {
				in[v]--
				if in[v] == 0 {
					q = append(q, v)
				}
			}
		}
	}
	if visited != len(g.Nodes) {
		return nil, errors.New("cycle detected in component graph")
	}
	return layers, nil
}

// StartStack starts components respecting the DAG.
func StartStack(parent context.Context, comps []*Component) error {
	// Create a derived context, but do not cancel it on successful return.
	// Cancellation is used only on failure to stop started components.
	ctx, cancel := context.WithCancel(parent)

	g := BuildGraph(comps)
	layers, err := g.TopoLayers()
	if err != nil {
		cancel()
		return err
	}
	started := make(map[string]*Component)

	for i, layer := range layers {
		log.Info().Int("layer", i).Strs("components", layer).Msg("starting layer")
		var wg sync.WaitGroup
		errCh := make(chan error, len(layer))

		installTimeout := installTimeoutFromEnv()
		for _, name := range layer {
			comp := g.Nodes[name]
			wg.Add(1)
			go func(c *Component) {
				defer wg.Done()
				// Install with a bounded timeout, start without (managed by caller)
				installCtx, cancelInstall := context.WithTimeout(ctx, installTimeout)
				defer cancelInstall()
				if err := c.Install(installCtx); err != nil {
					errCh <- fmt.Errorf("%s install: %w", c.Name, err)
					return
				}
				if err := c.Start(ctx); err != nil {
					errCh <- fmt.Errorf("%s start: %w", c.Name, err)
					return
				}
				started[c.Name] = c
			}(comp)
		}
		wg.Wait()
		close(errCh)
		if first := <-errCh; first != nil {
			log.Error().Int("layer", i).Err(first).Msg("layer failed")
			cancel()
			// Stop what started so far (best-effort)
			for j := i; j >= 0; j-- {
				for _, n := range layers[j] {
					if s, ok := started[n]; ok {
						_ = s.Stop(context.Background())
					}
				}
			}
			return first
		}
	}
	log.Info().Msg("all components running")
	return nil
}

// installTimeoutFromEnv returns a timeout for the install phase. Default 2m.
// Override with KEYSTONE_INSTALL_TIMEOUT (e.g., "90s", "5m").
func installTimeoutFromEnv() time.Duration {
	v := os.Getenv("KEYSTONE_INSTALL_TIMEOUT")
	if v == "" {
		return 2 * time.Minute
	}
	if d, err := time.ParseDuration(v); err == nil && d > 0 {
		return d
	}
	return 2 * time.Minute
}

// Demo helpers used for early smoke tests.
func MockInstall(label string, d time.Duration) func(context.Context) error {
	return func(ctx context.Context) error {
		select {
		case <-time.After(d):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
func MockStart(label string, d time.Duration) func(context.Context) error {
	return func(ctx context.Context) error {
		select {
		case <-time.After(d):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
func MockStop(label string, d time.Duration) func(context.Context) error {
	return func(ctx context.Context) error {
		select {
		case <-time.After(d):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
