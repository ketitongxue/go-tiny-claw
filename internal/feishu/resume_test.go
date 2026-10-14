package feishu

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ctxpkg "github.com/ketitongxue/go-tiny-claw/internal/context"
	"github.com/ketitongxue/go-tiny-claw/internal/engine"
	"github.com/ketitongxue/go-tiny-claw/internal/provider"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
	"github.com/ketitongxue/go-tiny-claw/internal/sessionstore"
	"github.com/ketitongxue/go-tiny-claw/internal/tools"
)

type resumeProvider func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error)

func (p resumeProvider) Generate(ctx context.Context, m []schema.Message, d []schema.ToolDefinition) (*schema.Message, error) {
	return p(ctx, m, d)
}
func TestFeishuRestartResumeWithPinnedModelAndContext(t *testing.T) {
	t.Setenv("FEISHU_APP_ID", "cli_test")
	t.Setenv("FEISHU_APP_SECRET", "test")
	dir, home := t.TempDir(), t.TempDir()
	t.Setenv("CLAW_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "AGENTS.md"), []byte("INITIAL USER CONTEXT"), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := sessionstore.NewFileStore(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	seen := make(chan []schema.Message, 2)
	refs := make(chan string, 2)
	start := func(defaultModel string) *FeishuBot {
		svc := ctxpkg.NewSessionService(store)
		b, err := NewFeishuBotWithSessions(dir, func(ctx context.Context, id string) (*ctxpkg.Session, error) {
			return svc.GetOrCreate(ctx, id, dir, provider.ModelInfo{ProviderID: "fake", ID: defaultModel}, "none")
		}, func(_ context.Context, s *ctxpkg.Session, _ engine.Reporter) (*engine.AgentEngine, error) {
			refs <- s.ModelRef().String()
			p := resumeProvider(func(_ context.Context, m []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
				seen <- m
				return &schema.Message{Role: schema.RoleAssistant}, nil
			})
			return engine.NewAgentEngine(p, tools.NewRegistry(), false, false), nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	a := start("model-a")
	if err := a.handleMessage(ctx, textEvent("chat", "1", "first task")); err != nil {
		t.Fatal(err)
	}
	if messages := receive(t, seen); !strings.Contains(messages[0].Content, "INITIAL USER CONTEXT") {
		t.Fatal("user context not loaded")
	}
	if receive(t, refs) != "fake/model-a" {
		t.Fatal("wrong initial model")
	}
	a.Close()
	a.workers.Wait()
	if err := os.WriteFile(filepath.Join(home, "AGENTS.md"), []byte("UPDATED USER CONTEXT"), 0600); err != nil {
		t.Fatal(err)
	}
	b := start("model-b")
	defer func() { b.Close(); b.workers.Wait() }()
	if err := b.handleMessage(ctx, textEvent("chat", "2", "continue")); err != nil {
		t.Fatal(err)
	}
	messages := receive(t, seen)
	if !strings.Contains(messages[0].Content, "UPDATED USER CONTEXT") || strings.Contains(messages[0].Content, "INITIAL USER CONTEXT") {
		t.Fatal("Feishu resume retained stale prompt")
	}
	if receive(t, refs) != "fake/model-a" {
		t.Fatal("default replaced pinned model")
	}
	found := false
	for _, m := range messages {
		if m.Content == "first task" {
			found = true
		}
	}
	if !found {
		t.Fatal("history lost after restart")
	}
	if err := b.handleMessage(ctx, textEvent("new-chat", "3", "new")); err != nil {
		t.Fatal(err)
	}
	receive(t, seen)
	if receive(t, refs) != "fake/model-b" {
		t.Fatal("new chat did not select current default")
	}
}
