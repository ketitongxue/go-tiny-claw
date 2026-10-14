package context

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ketitongxue/go-tiny-claw/internal/schema"
)

func TestWorkingMemoryKeepsCompleteParallelToolTurn(t *testing.T) {
	s := NewSession("chat", t.TempDir())
	s.Append(context.Background(),
		schema.Message{Role: schema.RoleUser, Content: "old task"},
		schema.Message{Role: schema.RoleAssistant, Content: "old answer"},
		schema.Message{Role: schema.RoleUser, Content: "inspect"},
		schema.Message{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "a", Name: "read_file"}, {ID: "b", Name: "read_file"}}},
		schema.Message{Role: schema.RoleUser, ToolCallID: "a", Content: "file a"},
		schema.Message{Role: schema.RoleUser, ToolCallID: "b", Content: "file b"},
		schema.Message{Role: schema.RoleAssistant, Content: "answer"},
	)
	for _, limit := range []int{1, 2, 3, 4, 5} {
		msgs := s.GetWorkingMemory(limit)
		if len(msgs) != 5 || msgs[0].Content != "inspect" || len(msgs[1].ToolCalls) != 2 || msgs[2].ToolCallID != "a" || msgs[3].ToolCallID != "b" {
			t.Fatalf("limit %d produced broken tool history: %+v", limit, msgs)
		}
	}
	msgs := s.GetWorkingMemory(2)
	msgs[0].Content = "modified by caller"
	if s.GetWorkingMemory(2)[0].Content != "inspect" {
		t.Fatal("working memory aliases the session history slice")
	}
	if len(s.GetWorkingMemory(0)) != 7 {
		t.Fatal("unlimited working memory lost history")
	}
}

func TestBuilderPreservesToolReferencesAndLatestEvidence(t *testing.T) {
	oldOutput := strings.Repeat("old log ", 200)
	latestOutput := "HEADER" + strings.Repeat("middle", 500) + "TRAILER"
	msgs := []schema.Message{
		{Role: schema.RoleSystem, Content: "system instructions"},
		{Role: schema.RoleUser, Content: "user request"},
		{Role: schema.RoleAssistant, Content: strings.Repeat("thinking", 100), ToolCalls: []schema.ToolCall{{ID: "old", Name: "read_file", Arguments: json.RawMessage(`{"path":"old"}`)}}},
		{Role: schema.RoleUser, ToolCallID: "old", Content: oldOutput},
		{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "latest", Name: "read_file", Arguments: json.RawMessage(`{"path":"latest"}`)}}},
		{Role: schema.RoleUser, ToolCallID: "latest", Content: latestOutput},
	}
	result, err := NewContextBuilder(ContextPolicy{MaxToolResultTokens: 500}).Build(msgs[0], msgs[1:], ModelContextSpec{})
	if err != nil {
		t.Fatal(err)
	}
	got := result.Messages
	if len(got) != len(msgs) || got[0].Content != msgs[0].Content || got[1].Content != msgs[1].Content {
		t.Fatalf("compaction altered message structure or instructions: %+v", got)
	}
	for i := range msgs {
		if got[i].Role != msgs[i].Role || got[i].ToolCallID != msgs[i].ToolCallID || !reflect.DeepEqual(got[i].ToolCalls, msgs[i].ToolCalls) {
			t.Fatalf("compaction broke tool linkage at message %d", i)
		}
	}
	if len(got[3].Content) >= len(oldOutput) || !strings.HasPrefix(got[5].Content, "HEADER") || !strings.HasSuffix(got[5].Content, "TRAILER") || len(got[5].Content) >= len(latestOutput) {
		t.Fatal("compaction did not reduce logs while preserving recent head/tail evidence")
	}
	if msgs[3].Content != oldOutput || msgs[5].Content != latestOutput {
		t.Fatal("compaction mutated durable session history")
	}
}

func TestPromptComposerRetainsWorkspaceInstructionsSkillsAndPlanMode(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("workspace-specific instruction"), 0600); err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(dir, ".claw", "skills", "verify")
	if err := os.MkdirAll(skillDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: verify\ndescription: verification tasks\n---\nUse the workspace verification procedure."), 0600); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	trustWorkspace(t, home, dir)
	for _, plan := range []bool{false, true} {
		state, err := (PromptService{Resources: ResourceLoader{Home: home}}).Build(dir, nil, plan, false)
		if err != nil {
			t.Fatal(err)
		}
		msg := state.Message()
		if msg.Role != schema.RoleSystem {
			t.Fatal("composer did not create a system prompt")
		}
		for _, expected := range []string{"workspace-specific instruction", "verification tasks", "<name>verify</name>"} {
			if !strings.Contains(msg.Content, expected) {
				t.Errorf("missing %q from prompt", expected)
			}
		}
		if strings.Contains(msg.Content, "Use the workspace verification procedure.") {
			t.Fatal("skill body was eagerly injected")
		}
		if strings.Contains(msg.Content, "Plan Mode: ON") != plan {
			t.Errorf("plan mode not reflected in prompt: %v", plan)
		}
	}
}
