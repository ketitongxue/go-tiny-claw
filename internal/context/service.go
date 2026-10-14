package context

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/ketitongxue/go-tiny-claw/internal/provider"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
	"github.com/ketitongxue/go-tiny-claw/internal/sessionstore"
)

func cloneMessages(msgs []schema.Message) []schema.Message {
	out := make([]schema.Message, len(msgs))
	for i, m := range msgs {
		out[i] = m
		if m.Usage != nil {
			u := *m.Usage
			out[i].Usage = &u
		}
		if m.ToolCalls != nil {
			out[i].ToolCalls = make([]schema.ToolCall, len(m.ToolCalls))
			for j, c := range m.ToolCalls {
				out[i].ToolCalls[j] = c
				out[i].ToolCalls[j].Arguments = append([]byte(nil), c.Arguments...)
			}
		}
	}
	return out
}
func (s *Session) usageLocked() sessionstore.Usage {
	return sessionstore.Usage{PromptTokens: s.TotalPromptTokens, CompletionTokens: s.TotalCompletionTokens, EstimatedCost: s.TotalEstimatedCost, CostKnown: s.CostKnown, Currency: s.Currency, BillingMode: s.BillingMode}
}
func (s *Session) snapshotLocked() sessionstore.Snapshot {
	return sessionstore.Snapshot{Metadata: sessionstore.Metadata{ID: s.ID, WorkDir: s.WorkDir, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, ProviderID: s.ProviderID, ModelID: s.ModelID, AuthType: s.AuthType, BillingMode: s.BillingMode, Messages: len(s.history)}, History: cloneMessages(s.history), Usage: s.usageLocked()}
}
func (s *Session) ModelRef() provider.ModelRef {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return provider.ModelRef{ProviderID: s.ProviderID, ModelID: s.ModelID}
}
func UniqueSessionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "demo_" + hex.EncodeToString(b[:]), nil
}

// SessionService owns durable session lifecycle. Mutations live on Session so
// callers cannot accidentally update memory without writing the corresponding fact.
type SessionService struct {
	Store    sessionstore.Store
	locks    sync.Map
	sessions sync.Map
}

func NewSessionService(store sessionstore.Store) *SessionService {
	return &SessionService{Store: store}
}
func (s *SessionService) lock(id string) *sync.Mutex {
	v, _ := s.locks.LoadOrStore(id, &sync.Mutex{})
	return v.(*sync.Mutex)
}
func (s *SessionService) Create(ctx context.Context, id, dir string, model provider.ModelInfo, authType string) (*Session, error) {
	mu := s.lock(id)
	mu.Lock()
	defer mu.Unlock()
	return s.create(ctx, id, dir, model, authType)
}
func (s *SessionService) create(ctx context.Context, id, dir string, model provider.ModelInfo, authType string) (*Session, error) {
	sess := NewSession(id, dir)
	sess.SetModelInfo(model)
	sess.SetAuthType(authType)
	if err := s.Store.Create(ctx, sess.snapshotLocked()); err != nil {
		return nil, err
	}
	sess.store = s.Store
	s.sessions.Store(id, sess)
	return sess, nil
}
func (s *SessionService) Load(ctx context.Context, id string) (*Session, error) {
	mu := s.lock(id)
	mu.Lock()
	defer mu.Unlock()
	return s.load(ctx, id)
}
func (s *SessionService) Resume(ctx context.Context, id string) (*Session, error) {
	return s.Load(ctx, id)
}
func (s *SessionService) load(ctx context.Context, id string) (*Session, error) {
	if v, ok := s.sessions.Load(id); ok {
		return v.(*Session), nil
	}
	snap, err := s.Store.Load(ctx, id)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(snap.Metadata.WorkDir)
	if err != nil {
		return nil, fmt.Errorf("resume workdir unavailable: %w", err)
	}
	if !info.IsDir() {
		return nil, errors.New("resume workdir is not a directory")
	}
	m, u := snap.Metadata, snap.Usage
	sess := NewSession(m.ID, m.WorkDir)
	sess.CreatedAt = m.CreatedAt
	sess.UpdatedAt = m.UpdatedAt
	sess.ProviderID = m.ProviderID
	sess.ModelID = m.ModelID
	sess.AuthType = m.AuthType
	sess.BillingMode = m.BillingMode
	sess.history = cloneMessages(snap.History)
	sess.TotalPromptTokens = u.PromptTokens
	sess.TotalCompletionTokens = u.CompletionTokens
	sess.TotalEstimatedCost = u.EstimatedCost
	sess.CostKnown = u.CostKnown
	sess.Currency = u.Currency
	sess.store = s.Store
	s.sessions.Store(id, sess)
	return sess, nil
}
func (s *SessionService) GetOrCreate(ctx context.Context, id, dir string, model provider.ModelInfo, authType string) (*Session, error) {
	mu := s.lock(id)
	mu.Lock()
	defer mu.Unlock()
	sess, err := s.load(ctx, id)
	if errors.Is(err, sessionstore.ErrNotFound) {
		return s.create(ctx, id, dir, model, authType)
	}
	return sess, err
}
func (s *SessionService) List(ctx context.Context) ([]sessionstore.Metadata, error) {
	return s.Store.List(ctx)
}
func (s *SessionService) Delete(ctx context.Context, id string) error {
	mu := s.lock(id)
	mu.Lock()
	defer mu.Unlock()
	if err := s.Store.Delete(ctx, id); err != nil {
		return err
	}
	s.sessions.Delete(id)
	return nil
}
