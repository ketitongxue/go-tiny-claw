package gateway

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ketitongxue/go-tiny-claw/internal/approval"
	"github.com/ketitongxue/go-tiny-claw/internal/channel"
	ctxpkg "github.com/ketitongxue/go-tiny-claw/internal/context"
	"github.com/ketitongxue/go-tiny-claw/internal/engine"
	"github.com/ketitongxue/go-tiny-claw/internal/observability"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
	"github.com/ketitongxue/go-tiny-claw/internal/tools"
)

type traceProvider struct{}

func (traceProvider) Generate(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
	return &schema.Message{Role: schema.RoleAssistant, Content: "done"}, nil
}

func TestMessagingRootTraceDimensionsAndAddressRedaction(t *testing.T) {
	sessions := make(chan *ctxpkg.Session, 1)
	g, a, _ := testGateway(t, func(_ context.Context, s *ctxpkg.Session, _ channel.Route, _ engine.Reporter, _ *approval.Service) (AgentRunner, error) {
		sessions <- s
		return engine.NewAgentEngine(traceProvider{}, tools.NewRegistry(), false, false), nil
	})
	msg := inbound("private-chat-address", "1", "hello")
	msg.UserID = "private-user-address"
	admit(t, g, msg)
	s := receive(t, sessions)
	receive(t, a.sent)
	if err := g.Shutdown(); err != nil {
		t.Fatal(err)
	}
	paths, err := filepath.Glob(filepath.Join(s.WorkDir, ".claw", "traces", "*.json"))
	if err != nil || len(paths) != 1 {
		t.Fatal("missing trace", err)
	}
	data, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{msg.Route.ChatID, msg.UserID} {
		if strings.Contains(string(data), address) || strings.Contains(paths[0], address) {
			t.Fatal("raw address entered trace")
		}
	}
	var span observability.Span
	if err := json.Unmarshal(data, &span); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"channel": "test", "account_id": "default", "chat_type": "direct", "SessionID": observability.SessionReference(s.ID)}
	for k, v := range want {
		if span.Attributes[k] != v {
			t.Fatalf("missing %s: %+v", k, span.Attributes)
		}
	}
}
