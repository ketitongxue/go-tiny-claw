package main

import (
	"context"
	"log"
	"os"

	"github.com/ketitongxue/go-tiny-claw/internal/engine"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
)

// ==========================================
// 1. 伪造的大模型 Provider
// ==========================================
type mockProvider struct {
	turn int
}

// 模拟大模型的响应：第一轮请求执行 bash，第二轮输出最终结果
func (m *mockProvider) Generate(ctx context.Context, msgs []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
	m.turn++
	if m.turn == 1 {
		return &schema.Message{
			Role:    schema.RoleAssistant,
			Content: "让我来看看当前目录下有什么文件。",
			ToolCalls: []schema.ToolCall{
				{ID: "call_123", Name: "bash", Arguments: []byte(`{"command": "ls -la"}`)},
			},
		}, nil
	}

	return &schema.Message{
		Role:    schema.RoleAssistant,
		Content: "我看到了文件列表，里面包含 main.go，任务完成！",
	}, nil
}

// ==========================================
// 2. 伪造的 Tool Registry
// ==========================================
type mockRegistry struct{}

func (m *mockRegistry) GetAvailableTools() []schema.ToolDefinition { return nil }

func (m *mockRegistry) Execute(ctx context.Context, call schema.ToolCall) schema.ToolResult {
	// 直接返回一段伪造的终端输出
	return schema.ToolResult{
		ToolCallID: call.ID,
		Output:     "-rw-r--r--  1 user group  234 Oct 24 10:00 main.go\n",
		IsError:    false,
	}
}

// ==========================================
// 3. 组装运行
// ==========================================
func main() {
	// 获取当前执行目录作为 WorkDir 物理边界
	workDir, _ := os.Getwd()

	p := &mockProvider{}
	r := &mockRegistry{}

	// 实例化核心引擎
	eng := engine.NewAgentEngine(p, r, workDir)

	// 发起任务指令
	err := eng.Run(context.Background(), "帮我检查当前目录的文件")
	if err != nil {
		log.Fatalf("引擎崩溃: %v", err)
	}
}
