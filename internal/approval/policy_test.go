package approval

import (
	"context"
	"testing"

	"github.com/ketitongxue/go-tiny-claw/internal/schema"
)

func TestDangerousPolicyUnchanged(t *testing.T) {
	for _, tc := range []struct {
		tool, args string
		dangerous  bool
	}{
		{"bash", `{"command":"rm -rf target"}`, true},
		{"bash", `{"command":"sudo ls"}`, true},
		{"bash", `{"command":"drop table t"}`, true},
		{"bash", `{"command":"echo x > main.go"}`, true},
		{"bash", `{"command":"echo hi"}`, false},
		{"read_file", "rm -rf", false},
		{"write_file", "main.go", false},
		{"edit_file", "main.go", false},
	} {
		if got := IsDangerousCommand(tc.tool, tc.args); got != tc.dangerous {
			t.Fatalf("policy changed: %+v", tc)
		}
	}
	service := NewService(nil, "session", testRequest("session", "t").Route, nil)
	if allowed, _ := service.Check(context.Background(), schema.ToolCall{ID: "t", Name: "bash", Arguments: []byte(`{"command":"sudo ls"}`)}); allowed {
		t.Fatal("unconfigured approval channel allowed danger")
	}
}
