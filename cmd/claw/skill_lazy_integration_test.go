package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	ctxpkg "github.com/ketitongxue/go-tiny-claw/internal/context"
	"github.com/ketitongxue/go-tiny-claw/internal/project"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
)

// Scripted model checks the real tool/loader integration. Actual model skill
// selection remains a human acceptance check, not something a fake can prove.
func TestSkillLazyLoadingUsesExistingReadToolAndRelativeReference(t *testing.T) {
	workspace, home := t.TempDir(), t.TempDir()
	goDir := filepath.Join(workspace, ".claw", "skills", "go-testing")
	putPromptFile(t, filepath.Join(goDir, "SKILL.md"), "---\nname: go-testing\ndescription: Go unit testing\n---\nGO BODY: read ./assets/example.txt relative to this skill.")
	putPromptFile(t, filepath.Join(goDir, "assets", "example.txt"), "GO EXAMPLE")
	putPromptFile(t, filepath.Join(workspace, ".claw", "skills", "docker", "SKILL.md"), "---\nname: docker\ndescription: Docker deployment\n---\nDOCKER BODY must not load.")
	if err := project.NewTrustStore(home).Trust(workspace); err != nil {
		t.Fatal(err)
	}
	turn := 0
	p := providerFunc(func(_ context.Context, msgs []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		turn++
		sys := msgs[0].Content
		if strings.Contains(sys, "GO BODY") || strings.Contains(sys, "DOCKER BODY") || !strings.Contains(sys, "<name>go-testing</name>") || !strings.Contains(sys, "<name>docker</name>") {
			t.Fatal(sys)
		}
		var path string
		switch turn {
		case 1:
			path = ".claw/skills/go-testing/SKILL.md"
		case 2:
			if !strings.Contains(msgs[len(msgs)-1].Content, "GO BODY") {
				t.Fatal("skill not read", msgs)
			}
			path = ".claw/skills/go-testing/assets/example.txt"
		case 3:
			if msgs[len(msgs)-1].Content != "GO EXAMPLE" {
				t.Fatal("relative reference not readable")
			}
			return &schema.Message{Role: schema.RoleAssistant, Content: "go skill loaded"}, nil
		default:
			t.Fatal("unexpected turn")
		}
		args, _ := json.Marshal(map[string]string{"path": path})
		return &schema.Message{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: path, Name: "read_file", Arguments: args}}}, nil
	})
	session := ctxpkg.NewSession("lazy-skill", workspace)
	if err := session.Append(context.Background(), schema.Message{Role: schema.RoleUser, Content: "帮我补 Go 单元测试"}); err != nil {
		t.Fatal(err)
	}
	eng := newSessionEngine(p, session, nil, options{mode: "demo", promptHome: home}, sessionExecutors{}, nil)
	if err := eng.Run(context.Background(), session, nil); err != nil {
		t.Fatal(err)
	}
	if turn != 3 {
		t.Fatal(turn)
	}
	for _, m := range session.GetWorkingMemory(0) {
		if strings.Contains(m.Content, "DOCKER BODY") {
			t.Fatal("unrelated skill read")
		}
	}
}
