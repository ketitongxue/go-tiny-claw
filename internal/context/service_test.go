package context

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ketitongxue/go-tiny-claw/internal/provider"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
	"github.com/ketitongxue/go-tiny-claw/internal/sessionstore"
)

func TestDurableResumePinning(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	f, err := sessionstore.NewFileStore(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	model := provider.ModelInfo{ProviderID: "zai-coding-cn", ID: "model-a", BillingMode: provider.BillingAPI, Pricing: &provider.Pricing{Currency: "CNY"}}
	s, err := NewSessionService(f).GetOrCreate(ctx, "chat-a", dir, model, "api_key")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Append(ctx, schema.Message{Role: schema.RoleUser, Content: "remember"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordUsage(ctx, 10, 5, provider.UsageBilling{CostKnown: true, EstimatedCost: 2}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	model.ID = "model-b"
	resumed, err := NewSessionService(f).GetOrCreate(ctx, "chat-a", dir, model, "oauth")
	if err != nil {
		t.Fatal(err)
	}
	if resumed.ModelRef().ModelID != "model-a" || len(resumed.GetWorkingMemory(0)) != 1 || resumed.UsageSnapshot().PromptTokens != 10 {
		t.Fatal("resume lost facts")
	}
	if err := resumed.RecordUsage(ctx, 3, 2, provider.UsageBilling{CostKnown: true}); err != nil {
		t.Fatal(err)
	}
	loaded, err := f.Load(ctx, "chat-a")
	if err != nil || loaded.Usage.PromptTokens != 13 {
		t.Fatal(err)
	}
	if err := resumed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSessionService(f).Resume(ctx, "chat-a"); err == nil {
		t.Fatal("missing workdir accepted")
	}
	ids := map[string]bool{}
	for i := 0; i < 100; i++ {
		id, err := UniqueSessionID()
		if err != nil || ids[id] {
			t.Fatal("nonunique demo id")
		}
		ids[id] = true
	}
}

type failingStore struct{ *sessionstore.MemoryStore }

func (f failingStore) Append(context.Context, string, ...sessionstore.Event) error {
	return errors.New("disk full")
}
func TestMutationFailure(t *testing.T) {
	svc := NewSessionService(failingStore{sessionstore.NewMemoryStore()})
	s, err := svc.Create(context.Background(), "s", t.TempDir(), provider.ModelInfo{}, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Append(context.Background(), schema.Message{Role: schema.RoleUser, Content: "x"}); err == nil {
		t.Fatal("failure hidden")
	}
	if len(s.GetWorkingMemory(0)) != 0 {
		t.Fatal("memory committed on failure")
	}
	if err := s.RecordUsage(context.Background(), 10, 10, provider.UsageBilling{}); err == nil {
		t.Fatal("failure hidden")
	}
	if s.UsageSnapshot().PromptTokens != 0 {
		t.Fatal("usage committed on failure")
	}
}
