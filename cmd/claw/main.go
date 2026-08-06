package main

import (
	"context"
	"log"
	"os"

	"github.com/ketitongxue/go-tiny-claw/internal/engine"
	"github.com/ketitongxue/go-tiny-claw/internal/provider"
	"github.com/ketitongxue/go-tiny-claw/internal/tools"
)

func main() {
	// 确保已设置 ZHIPU_API_KEY
	if os.Getenv("ZHIPU_API_KEY") == "" {
		log.Fatal("请先导出 ZHIPU_API_KEY 环境变量")
	}

	// 1. 获取当前执行目录作为 WorkDir 物理边界
	workDir, _ := os.Getwd()

	// 2. 初始化真实的 Provider大脑 (指向智谱 GLM-5.2)
	// 这里你可以任意切换 NewZhipuClaudeProvider 或 NewZhipuOpenAIProvider，效果完全一致！
	llmProvider := provider.NewZhipuOpenAIProvider("glm-5.2")

	// 3. 初始化真实的 Tool Registry
	registry := tools.NewRegistry()

	// 4. 将真实的 ReadFile 工具挂载到注册表中
	registry.Register(tools.NewReadFileTool(workDir))
	registry.Register(tools.NewWriteFileTool(workDir))
	registry.Register(tools.NewBashTool(workDir))

	// 5. 实例化核心引擎，由于任务简单，我们关闭思考阶段 (EnableThinking = false) 以加快速度
	eng := engine.NewAgentEngine(llmProvider, registry, workDir, false)

	// 6. 下发一个必须通过真实工具才能完成的任务
	prompt := `
    请帮我执行以下操作：
    1. 用 bash 查看一下我当前电脑的 Go 版本。
    2. 帮我写一个简单的 helloworld.go 文件，输出 "Hello, go-tiny-claw!"。
    3. 用 bash 编译并运行这个 go 文件，确认它能正常工作。
    `

	// 发起任务指令
	err := eng.Run(context.Background(), prompt)
	if err != nil {
		log.Fatalf("引擎崩溃: %v", err)
	}
}
