package main

import (
	"context"
	"log"
	"os"

	"github.com/ketitongxue/go-tiny-claw/internal/engine"
	"github.com/ketitongxue/go-tiny-claw/internal/provider"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
)

// ==========================================
// 2. 伪造的 Tool Registry
// ==========================================
type mockRegistry struct{}

func (m *mockRegistry) GetAvailableTools() []schema.ToolDefinition {
	return []schema.ToolDefinition{
		{
			Name:        "get_weather",
			Description: "获取指定城市的当前天气情况。",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"city": map[string]interface{}{
						"type": "string",
					},
				},
				"required": []string{"city"},
			},
		},
	}
}

func (m *mockRegistry) Execute(ctx context.Context, call schema.ToolCall) schema.ToolResult {
	log.Printf("  -> [Mock 工具执行] 获取 %s 的天气中...\n", call.Name)
	return schema.ToolResult{
		ToolCallID: call.ID,
		Output:     "API 返回：今天是晴天，气温 25 度。",
		IsError:    false,
	}
}

// ==========================================
// 3. 组装运行
// ==========================================
func main() {
	// 确保已设置 ZHIPU_API_KEY
	if os.Getenv("ZHIPU_API_KEY") == "" {
		log.Fatal("请先导出 ZHIPU_API_KEY 环境变量")
	}

	// 获取当前执行目录作为 WorkDir 物理边界
	workDir, _ := os.Getwd()

	// 1. 初始化真实的 Provider大脑 (指向智谱 GLM-5.2)
	// 这里你可以任意切换 NewZhipuClaudeProvider 或 NewZhipuOpenAIProvider，效果完全一致！
	llmProvider := provider.NewZhipuOpenAIProvider("glm-5.2")

	r := &mockRegistry{}

	// 实例化核心引擎
	eng := engine.NewAgentEngine(llmProvider, r, workDir, false)

	// 设定测试任务
	prompt := "我想去北京跑步，帮我查查天气适合吗？"

	// 发起任务指令
	err := eng.Run(context.Background(), prompt)
	if err != nil {
		log.Fatalf("引擎崩溃: %v", err)
	}
}
