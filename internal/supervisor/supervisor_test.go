package supervisor

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTopoLayers(t *testing.T) {
	// A -> B -> C
	//      B -> D
	// E
	// Layers: [A, E], [B], [C, D]

	comps := []*Component{
		{Name: "A", Deps: []string{}},
		{Name: "B", Deps: []string{"A"}},
		{Name: "C", Deps: []string{"B"}},
		{Name: "D", Deps: []string{"B"}},
		{Name: "E", Deps: []string{}},
	}

	g := BuildGraph(comps)
	layers, err := g.TopoLayers()
	require.NoError(t, err)

	assert.Len(t, layers, 3)
	// Layer 0: A, E (order within layer is not guaranteed, but set content is)
	assert.ElementsMatch(t, []string{"A", "E"}, layers[0])
	// Layer 1: B
	assert.ElementsMatch(t, []string{"B"}, layers[1])
	// Layer 2: C, D
	assert.ElementsMatch(t, []string{"C", "D"}, layers[2])
}

func TestTopoLayers_Cycle(t *testing.T) {
	// A -> B -> A
	comps := []*Component{
		{Name: "A", Deps: []string{"B"}},
		{Name: "B", Deps: []string{"A"}},
	}

	g := BuildGraph(comps)
	_, err := g.TopoLayers()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cycle detected")
}

func TestStartStack(t *testing.T) {
	// A -> B
	var mu sync.Mutex
	var order []string

	startFn := func(name string) func(context.Context) error {
		return func(ctx context.Context) error {
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
			return nil
		}
	}

	compA := NewComponent("A", nil, nil, startFn("A"), nil)
	compB := NewComponent("B", []string{"A"}, nil, startFn("B"), nil)

	err := StartStack(context.Background(), []*Component{compA, compB})
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"A", "B"}, order)
}

func TestStartStack_Failure(t *testing.T) {
	// A (fails) -> B
	// Should fail at A and not start B

	compA := NewComponent("A", nil, nil, func(ctx context.Context) error {
		return assert.AnError
	}, nil)

	startedB := false
	compB := NewComponent("B", []string{"A"}, nil, func(ctx context.Context) error {
		startedB = true
		return nil
	}, nil)

	err := StartStack(context.Background(), []*Component{compA, compB})
	assert.Error(t, err)
	assert.False(t, startedB, "B should not have started")
}
