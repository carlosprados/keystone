package store

import "sync"

// ComponentInfo represents a simplified view of a component for listing.
type ComponentInfo struct {
	Name       string `json:"name"`
	State      string `json:"state"`
	Restarts   int    `json:"restarts"`
	LastHealth string `json:"last_health"` // healthy|unhealthy|unknown
	PID        int    `json:"pid"`
}

// MemoryStore is a tiny in-memory store for components.
type MemoryStore struct {
	mu    sync.RWMutex
	items map[string]ComponentInfo
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{items: make(map[string]ComponentInfo)}
}

func (s *MemoryStore) Upsert(ci ComponentInfo) {
	s.mu.Lock()
	prev, ok := s.items[ci.Name]
	if ok {
		if ci.State == "" {
			ci.State = prev.State
		}
		if ci.Restarts == 0 {
			ci.Restarts = prev.Restarts
		}
		if ci.LastHealth == "" {
			ci.LastHealth = prev.LastHealth
		}
		if ci.PID == 0 {
			ci.PID = prev.PID
		}
	}
	if ci.LastHealth == "" {
		ci.LastHealth = "unknown"
	}
	s.items[ci.Name] = ci
	s.mu.Unlock()
}

func (s *MemoryStore) List() []ComponentInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ComponentInfo, 0, len(s.items))
	for _, v := range s.items {
		out = append(out, v)
	}
	return out
}

func (s *MemoryStore) Get(name string) (ComponentInfo, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.items[name]
	return v, ok
}
