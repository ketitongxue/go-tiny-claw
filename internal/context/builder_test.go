package context

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ketitongxue/go-tiny-claw/internal/schema"
	"github.com/ketitongxue/go-tiny-claw/internal/sessionstore"
)

func TestBuilderBudgetAndDurableHistory(t *testing.T) {
	system := schema.Message{Role: schema.RoleSystem, Content: "system rules"}
	history := []schema.Message{}
	for i := 0; i < 20; i++ {
		id := fmt.Sprint(i)
		history = append(history, schema.Message{Role: schema.RoleUser, Content: "inspect " + id}, schema.Message{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: id, Name: "read_file", Arguments: []byte(`{"path":"中文.go"}`)}}}, schema.Message{Role: schema.RoleUser, ToolCallID: id, Content: "HEAD" + strings.Repeat("中文输出😀", 1000) + "TAIL"}, schema.Message{Role: schema.RoleAssistant, Content: "done"}, schema.Message{Role: schema.RoleAssistant, Content: "extra answer"})
	}
	ctx := context.Background()
	f, err := sessionstore.NewFileStore(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	snap := sessionstore.Snapshot{Metadata: sessionstore.Metadata{ID: "100", WorkDir: t.TempDir()}, History: history}
	if err := f.Create(ctx, snap); err != nil {
		t.Fatal(err)
	}
	loaded, err := f.Load(ctx, "100")
	if err != nil {
		t.Fatal(err)
	}
	b := NewContextBuilder(ContextPolicy{DefaultReservedOutput: 100, MaxToolResultTokens: 200})
	result, err := b.Build(system, loaded.History, ModelContextSpec{ContextWindow: 1500})
	if err != nil {
		t.Fatal(err)
	}
	if result.OmittedMessages == 0 || result.TruncatedToolResults == 0 || result.EstimatedTokens > result.BudgetTokens {
		t.Fatalf("%+v", result)
	}
	if result.Messages[0].Content != system.Content || !strings.Contains(result.Messages[1].Content, "inspect") {
		t.Fatal("system or turn boundary lost")
	}
	if result.Messages[len(result.Messages)-3].ToolCallID != "19" {
		t.Fatal("latest tool chain lost")
	}
	if _, err := units(result.Messages[1:]); err != nil {
		t.Fatal(err)
	}
	for _, m := range result.Messages {
		if !utf8.ValidString(m.Content) {
			t.Fatal("broken UTF-8")
		}
		if m.ToolCallID != "" && (!strings.HasPrefix(m.Content, "HEAD") || !strings.HasSuffix(m.Content, "TAIL")) {
			t.Fatal("head/tail lost")
		}
	}
	disk, err := f.Load(ctx, "100")
	if err != nil || len(disk.History) != 100 || !reflect.DeepEqual(history, disk.History) {
		t.Fatal("request view modified durable history")
	}
	short := history[:5]
	r, err := b.Build(system, short, ModelContextSpec{})
	if err != nil || r.OmittedMessages != 0 {
		t.Fatalf("short history: %+v %v", r, err)
	}
}
func TestBudgetErrorsAndFallback(t *testing.T) {
	b := NewContextBuilder(ContextPolicy{})
	window, input := b.Policy.Budget(ModelContextSpec{})
	if window != DefaultContextWindow || input != 25395 {
		t.Fatalf("fallback %d %d", window, input)
	}
	model := ModelContextSpec{ContextWindow: 200, MaxOutput: 20}
	for _, tc := range []struct {
		name   string
		system string
		user   string
	}{{"system", strings.Repeat("中", 200), "task"}, {"user", "system", strings.Repeat("中", 200)}} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := b.Build(schema.Message{Role: schema.RoleSystem, Content: tc.system}, []schema.Message{{Role: schema.RoleUser, Content: tc.user}}, model)
			if !errors.Is(err, ErrContextBudgetExceeded) {
				t.Fatal(err)
			}
		})
	}
	_, err := b.Build(schema.Message{}, []schema.Message{{Role: schema.RoleUser, ToolCallID: "orphan"}}, ModelContextSpec{})
	if !errors.Is(err, ErrToolChain) {
		t.Fatal(err)
	}
	_, err = b.Build(schema.Message{}, []schema.Message{{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "pending"}}}}, ModelContextSpec{})
	if !errors.Is(err, ErrToolChain) {
		t.Fatal(err)
	}
	e := HeuristicEstimator{}
	plain := schema.Message{Role: schema.RoleAssistant, Content: "hello"}
	calls := plain
	calls.ToolCalls = []schema.ToolCall{{ID: "call", Name: "bash", Arguments: []byte(`{"command":"` + strings.Repeat("x", 1000) + `"}`)}}
	if e.EstimateMessage(calls) < e.EstimateMessage(plain)+500 {
		t.Fatal("arguments not counted")
	}
	if estimateText("中文") < 4 {
		t.Fatal("CJK underestimated")
	}
}
