package channel

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

type Registry struct {
	mu       sync.RWMutex
	adapters map[string]Adapter
}

func NewRegistry() *Registry { return &Registry{adapters: make(map[string]Adapter)} }

func (r *Registry) Register(adapter Adapter) error {
	if adapter == nil || strings.TrimSpace(adapter.ID()) == "" {
		return errors.New("channel adapter ID required")
	}
	id := adapter.ID()
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.adapters[id]; exists {
		return fmt.Errorf("channel %q already registered", id)
	}
	if r.adapters == nil {
		r.adapters = make(map[string]Adapter)
	}
	r.adapters[id] = adapter
	return nil
}

func (r *Registry) Get(id string) (Adapter, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.adapters[id]
	return a, ok
}

// All returns a stable snapshot for gateway startup. Register before serving.
func (r *Registry) All() []Adapter {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, 0, len(r.adapters))
	for id := range r.adapters {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]Adapter, 0, len(ids))
	for _, id := range ids {
		out = append(out, r.adapters[id])
	}
	return out
}
