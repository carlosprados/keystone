package supervisor

// Minimal supervisor skeleton with a simple DAG and component lifecycle hooks.
// This is intentionally stdlib-only to keep the MVP self-contained.

import (
    "context"
    "errors"
    "fmt"
    "log"
    "sync"
    "time"
)

// State represents the current state of a component.
type State string

const (
    StateNone     State = "none"
    StateInstalling State = "installing"
    StateStarting State = "starting"
    StateRunning  State = "running"
    StateStopping State = "stopping"
    StateStopped  State = "stopped"
    StateFailed   State = "failed"
)

// Component models a managed unit with lifecycle callbacks and dependencies.
type Component struct {
    Name    string
    Deps    []string
    mu      sync.Mutex
    state   State
    InstallFn func(ctx context.Context) error
    StartFn   func(ctx context.Context) error
    StopFn    func(ctx context.Context) error
}

// NewComponent creates a component with the provided hooks.
func NewComponent(name string, deps []string, install, start, stop func(context.Context) error) *Component {
    return &Component{ Name: name, Deps: deps, state: StateNone, InstallFn: install, StartFn: start, StopFn: stop }
}

// State returns the current component state.
func (c *Component) State() State {
    c.mu.Lock(); defer c.mu.Unlock()
    return c.state
}

// setState changes state with logging.
func (c *Component) setState(s State) {
    c.state = s
    log.Printf("component[%s] -> %s", c.Name, s)
}

// Install runs the install hook when appropriate.
func (c *Component) Install(ctx context.Context) error {
    c.mu.Lock(); defer c.mu.Unlock()
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
    c.mu.Lock(); defer c.mu.Unlock()
    if c.state == StateRunning { return nil }
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
    c.setState(StateRunning)
    return nil
}

// Stop invokes the stop hook and moves to stopped.
func (c *Component) Stop(ctx context.Context) error {
    c.mu.Lock(); defer c.mu.Unlock()
    if c.state != StateRunning && c.state != StateStarting { return nil }
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
        if _, ok := g.InDeg[c.Name]; !ok { g.InDeg[c.Name] = 0 }
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
    for k, v := range g.InDeg { in[k] = v }
    var q []string
    for n, d := range in { if d == 0 { q = append(q, n) } }
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
                if in[v] == 0 { q = append(q, v) }
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
    ctx, cancel := context.WithCancel(parent)
    defer cancel()

    g := BuildGraph(comps)
    layers, err := g.TopoLayers()
    if err != nil { return err }
    started := make(map[string]*Component)

    for i, layer := range layers {
        log.Printf("starting layer %d: %v", i, layer)
        var wg sync.WaitGroup
        errCh := make(chan error, len(layer))

        for _, name := range layer {
            comp := g.Nodes[name]
            wg.Add(1)
            go func(c *Component) {
                defer wg.Done()
                // Install with a bounded timeout, start without (managed by caller)
                installCtx, cancelInstall := context.WithTimeout(ctx, 30*time.Second)
                defer cancelInstall()
                if err := c.Install(installCtx); err != nil { errCh <- fmt.Errorf("%s install: %w", c.Name, err); return }
                if err := c.Start(ctx); err != nil { errCh <- fmt.Errorf("%s start: %w", c.Name, err); return }
                started[c.Name] = c
            }(comp)
        }
        wg.Wait()
        close(errCh)
        if first := <-errCh; first != nil {
            log.Printf("layer %d failed: %v", i, first)
            cancel()
            // Stop what started so far (best-effort)
            for j := i; j >= 0; j-- {
                for _, n := range layers[j] {
                    if s, ok := started[n]; ok { _ = s.Stop(context.Background()) }
                }
            }
            return first
        }
    }
    log.Printf("all components running")
    return nil
}

// Demo helpers used for early smoke tests.
func MockInstall(label string, d time.Duration) func(context.Context) error {
    return func(ctx context.Context) error {
        select { case <-time.After(d): return nil; case <-ctx.Done(): return ctx.Err() }
    }
}
func MockStart(label string, d time.Duration) func(context.Context) error {
    return func(ctx context.Context) error {
        select { case <-time.After(d): return nil; case <-ctx.Done(): return ctx.Err() }
    }
}
func MockStop(label string, d time.Duration) func(context.Context) error {
    return func(ctx context.Context) error {
        select { case <-time.After(d): return nil; case <-ctx.Done(): return ctx.Err() }
    }
}
