// Package sessionstore stores facts, never provider request views or credentials.
package sessionstore

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/ketitongxue/go-tiny-claw/internal/provider"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
)

var ErrNotFound = errors.New("session not found")
var ErrCorrupted = errors.New("SessionCorrupted")
var ErrExists = errors.New("session already exists")

type Metadata struct {
	ID          string               `json:"session_id"`
	WorkDir     string               `json:"work_dir"`
	CreatedAt   time.Time            `json:"created_at"`
	UpdatedAt   time.Time            `json:"updated_at"`
	ProviderID  string               `json:"provider_id"`
	ModelID     string               `json:"model_id"`
	AuthType    string               `json:"auth_type"`
	BillingMode provider.BillingMode `json:"billing_mode"`
	Messages    int                  `json:"messages"`
}
type Usage struct {
	PromptTokens     int                  `json:"prompt_tokens"`
	CompletionTokens int                  `json:"completion_tokens"`
	EstimatedCost    float64              `json:"estimated_cost"`
	CostKnown        bool                 `json:"cost_known"`
	Currency         string               `json:"currency"`
	BillingMode      provider.BillingMode `json:"billing_mode"`
}
type Snapshot struct {
	Metadata Metadata
	History  []schema.Message
	Usage    Usage
}

// Usage events contain cumulative totals; metadata events contain full metadata.
type Event struct {
	Version   int             `json:"version"`
	Type      string          `json:"type"`
	Timestamp time.Time       `json:"timestamp"`
	Metadata  *Metadata       `json:"metadata,omitempty"`
	Message   *schema.Message `json:"message,omitempty"`
	Usage     *Usage          `json:"usage,omitempty"`
}

func NewEvent(kind string) Event { return Event{Version: 1, Type: kind, Timestamp: time.Now()} }

type Store interface {
	Create(context.Context, Snapshot) error
	Append(context.Context, string, ...Event) error
	Load(context.Context, string) (Snapshot, error)
	List(context.Context) ([]Metadata, error)
	Delete(context.Context, string) error
}

func apply(s *Snapshot, e Event) error {
	if e.Version != 1 {
		return ErrCorrupted
	}
	switch e.Type {
	case "session_created":
		if e.Metadata == nil || s.Metadata.ID != "" {
			return ErrCorrupted
		}
		s.Metadata = *e.Metadata
		if e.Usage != nil {
			s.Usage = *e.Usage
		}
	case "metadata_updated":
		if e.Metadata == nil || e.Metadata.ID != s.Metadata.ID {
			return ErrCorrupted
		}
		s.Metadata = *e.Metadata
	case "message":
		if e.Message == nil || s.Metadata.ID == "" {
			return ErrCorrupted
		}
		s.History = append(s.History, *e.Message)
	case "usage":
		if e.Usage == nil || s.Metadata.ID == "" {
			return ErrCorrupted
		}
		s.Usage = *e.Usage
	default:
		return ErrCorrupted
	}
	s.Metadata.UpdatedAt = e.Timestamp
	s.Metadata.Messages = len(s.History)
	return nil
}
func creation(s Snapshot) []Event {
	e := NewEvent("session_created")
	e.Timestamp = s.Metadata.CreatedAt
	e.Metadata = &s.Metadata
	e.Usage = &s.Usage
	events := []Event{e}
	for _, m := range s.History {
		e := NewEvent("message")
		e.Message = &m
		events = append(events, e)
	}
	return events
}
func clone(s Snapshot) Snapshot {
	b, _ := json.Marshal(s)
	var out Snapshot
	_ = json.Unmarshal(b, &out)
	return out
}

type memoryEntry struct {
	mu       sync.RWMutex
	snapshot Snapshot
}
type MemoryStore struct{ sessions sync.Map }

func NewMemoryStore() *MemoryStore { return &MemoryStore{} }
func (m *MemoryStore) Create(ctx context.Context, s Snapshot) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.Metadata.ID == "" {
		return errors.New("session ID required")
	}
	if _, loaded := m.sessions.LoadOrStore(s.Metadata.ID, &memoryEntry{snapshot: clone(s)}); loaded {
		return ErrExists
	}
	return nil
}
func (m *MemoryStore) Append(ctx context.Context, id string, events ...Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	v, ok := m.sessions.Load(id)
	if !ok {
		return ErrNotFound
	}
	entry := v.(*memoryEntry)
	entry.mu.Lock()
	defer entry.mu.Unlock()
	s := clone(entry.snapshot)
	for _, e := range events {
		if err := apply(&s, e); err != nil {
			return err
		}
	}
	entry.snapshot = clone(s)
	return nil
}
func (m *MemoryStore) Load(ctx context.Context, id string) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	v, ok := m.sessions.Load(id)
	if !ok {
		return Snapshot{}, ErrNotFound
	}
	entry := v.(*memoryEntry)
	entry.mu.RLock()
	defer entry.mu.RUnlock()
	return clone(entry.snapshot), nil
}
func (m *MemoryStore) List(ctx context.Context) ([]Metadata, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := []Metadata{}
	m.sessions.Range(func(_, v any) bool {
		entry := v.(*memoryEntry)
		entry.mu.RLock()
		meta := entry.snapshot.Metadata
		meta.Messages = len(entry.snapshot.History)
		entry.mu.RUnlock()
		out = append(out, meta)
		return true
	})
	return out, nil
}
func (m *MemoryStore) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, ok := m.sessions.LoadAndDelete(id); !ok {
		return ErrNotFound
	}
	return nil
}
