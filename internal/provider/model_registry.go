package provider

import (
	"fmt"
	"sort"
	"sync"
)

type ModelRegistry struct {
	mu     sync.RWMutex
	models map[string]ModelInfo
}

func NewModelRegistry() *ModelRegistry { return &ModelRegistry{models: make(map[string]ModelInfo)} }
func (r *ModelRegistry) Register(m ModelInfo) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := ParseModelRef(m.Ref().String()); err != nil {
		return err
	}
	if _, ok := r.models[m.Ref().String()]; ok {
		return fmt.Errorf("duplicate model %s", m.Ref())
	}
	if m.BillingMode == "" {
		m.BillingMode = BillingUnknown
	}
	if m.BillingMode != BillingUnknown && m.BillingMode != BillingAPI && m.BillingMode != BillingSubscription {
		return fmt.Errorf("invalid billing mode for %s", m.Ref())
	}
	if m.Name == "" {
		m.Name = m.ID
	}
	r.models[m.Ref().String()] = cloneModel(m)
	return nil
}

// ReplaceProvider atomically installs the catalog for the current credential.
// Validation finishes before any old models are removed.
func (r *ModelRegistry) ReplaceProvider(id string, models []ModelInfo) error {
	if err := ValidateProviderID(id); err != nil {
		return err
	}
	next := NewModelRegistry()
	for _, model := range models {
		if model.ProviderID != id {
			return fmt.Errorf("model belongs to a different provider")
		}
		if err := next.Register(model); err != nil {
			return err
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, model := range r.models {
		if model.ProviderID == id {
			delete(r.models, key)
		}
	}
	for key, model := range next.models {
		r.models[key] = model
	}
	return nil
}
func (r *ModelRegistry) Get(ref ModelRef) (ModelInfo, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.models[ref.String()]
	return cloneModel(m), ok
}
func (r *ModelRegistry) List() []ModelInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]ModelInfo, 0, len(r.models))
	for _, m := range r.models {
		result = append(result, cloneModel(m))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Ref().String() < result[j].Ref().String() })
	return result
}
