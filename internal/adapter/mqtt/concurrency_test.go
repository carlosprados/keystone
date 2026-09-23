package mqtt

import (
	"sync"
	"testing"
	"time"
)

// TestDeduperIsSafeForConcurrentUse: with OrderMatters(false) every handler
// runs in its own goroutine, so the deduper is now reached concurrently. It was
// written under a single routing goroutine, where a data race would never have
// shown up.
func TestDeduperIsSafeForConcurrentUse(t *testing.T) {
	d := newCommandDeduper(time.Minute)

	const workers = 32
	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted := 0

	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			if d.firstSight("same-command") {
				mu.Lock()
				accepted++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	// The point of the guard: exactly one of them may proceed, however many
	// arrive at once.
	if accepted != 1 {
		t.Fatalf("%d goroutines were allowed through for one commandId, want 1", accepted)
	}
}

// TestDeduperConcurrentDistinctIDs checks the other direction: distinct
// commands must not block each other, because that is the behaviour a single
// routing goroutine used to destroy.
func TestDeduperConcurrentDistinctIDs(t *testing.T) {
	d := newCommandDeduper(time.Minute)

	ids := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	var wg sync.WaitGroup
	results := make([]bool, len(ids))

	wg.Add(len(ids))
	for i, id := range ids {
		go func(i int, id string) {
			defer wg.Done()
			results[i] = d.firstSight(id)
		}(i, id)
	}
	wg.Wait()

	for i, ok := range results {
		if !ok {
			t.Errorf("command %q was rejected as a duplicate", ids[i])
		}
	}
}
