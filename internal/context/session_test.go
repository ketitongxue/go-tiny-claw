package context

import (
	"testing"

	"github.com/ketitongxue/go-tiny-claw/internal/schema"
)

func TestGetWorkingMemoryPreservesConversationStart(t *testing.T) {
	session := NewSession("test", t.TempDir())
	session.Append(
		schema.Message{Role: schema.RoleUser, Content: "搭建项目"},
		schema.Message{
			Role: schema.RoleAssistant,
			ToolCalls: []schema.ToolCall{{
				ID:        "call-1",
				Name:      "bash",
				Arguments: []byte(`{"command":"ls"}`),
			}},
		},
		schema.Message{Role: schema.RoleUser, Content: "第一轮结果", ToolCallID: "call-1"},
		schema.Message{
			Role: schema.RoleAssistant,
			ToolCalls: []schema.ToolCall{{
				ID:        "call-2",
				Name:      "read_file",
				Arguments: []byte(`{"path":"main.go"}`),
			}},
		},
		schema.Message{Role: schema.RoleUser, Content: "第二轮结果", ToolCallID: "call-2"},
		schema.Message{
			Role: schema.RoleAssistant,
			ToolCalls: []schema.ToolCall{{
				ID:        "call-3",
				Name:      "bash",
				Arguments: []byte(`{"command":"go test ./..."}`),
			}},
		},
		schema.Message{Role: schema.RoleUser, Content: "第三轮结果", ToolCallID: "call-3"},
	)

	// 最近 6 条会从第一条 assistant 消息开始，必须回溯补回初始 User 消息。
	got := session.GetWorkingMemory(6)
	if len(got) != 7 {
		t.Fatalf("expected the complete conversation turn, got %d messages", len(got))
	}
	if got[0].Role != schema.RoleUser || got[0].ToolCallID != "" {
		t.Fatalf("working memory must start with a normal user message, got %#v", got[0])
	}

	for i, msg := range got {
		if msg.Role == schema.RoleUser && msg.ToolCallID != "" {
			if i == 0 || got[i-1].Role != schema.RoleAssistant || len(got[i-1].ToolCalls) == 0 {
				t.Fatalf("tool result at index %d has no preceding assistant tool call", i)
			}
		}
	}
}

func TestGetWorkingMemoryStartsAtLatestUserTurn(t *testing.T) {
	session := NewSession("test", t.TempDir())
	session.Append(
		schema.Message{Role: schema.RoleUser, Content: "旧任务"},
		schema.Message{Role: schema.RoleAssistant, Content: "旧回复"},
		schema.Message{Role: schema.RoleUser, Content: "新任务"},
		schema.Message{Role: schema.RoleAssistant, Content: "新回复"},
	)

	got := session.GetWorkingMemory(2)
	if len(got) != 2 || got[0].Content != "新任务" || got[1].Content != "新回复" {
		t.Fatalf("expected the latest complete user turn, got %#v", got)
	}
}
