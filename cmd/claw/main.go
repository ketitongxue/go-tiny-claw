package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	ctxpkg "github.com/ketitongxue/go-tiny-claw/internal/context"
	"github.com/ketitongxue/go-tiny-claw/internal/engine"
	"github.com/ketitongxue/go-tiny-claw/internal/provider"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
	"github.com/ketitongxue/go-tiny-claw/internal/tools"
)

func main() {
	// 通过命令行参数接收用户的 prompt
	promptPtr := flag.String("prompt", "", "要交给 Agent 执行的任务描述")
	flag.Parse()

	if *promptPtr == "" {
		fmt.Println("用法: go run cmd/claw/main.go -prompt \"你的任务指令\"")
		os.Exit(1)
	}

	// 确保已设置 ZHIPU_API_KEY
	if os.Getenv("ZHIPU_API_KEY") == "" {
		log.Fatal("请先导出 ZHIPU_API_KEY 环境变量")
	}

	// 1. 获取当前执行目录作为 WorkDir 物理边界
	workDir, _ := os.Getwd()
	workDir += "/workspace"

	// 2. 初始化真实的 Provider大脑 (指向智谱 GLM-5.2)
	// 这里你可以任意切换 NewZhipuClaudeProvider 或 NewZhipuOpenAIProvider，效果完全一致！
	llmProvider := provider.NewZhipuOpenAIProvider("glm-5.2")

	// 3. 初始化真实的 Tool Registry
	registry := tools.NewRegistry()

	// 4. 将真实的 ReadFile 工具挂载到注册表中
	registry.Register(tools.NewReadFileTool(workDir))
	registry.Register(tools.NewWriteFileTool(workDir))
	registry.Register(tools.NewBashTool(workDir))
	registry.Register(tools.NewEditFileTool(workDir))

	// 5. 实例化核心引擎，由于任务简单，我们关闭思考阶段 (EnableThinking = false) 以加快速度
	eng := engine.NewAgentEngine(llmProvider, registry, false, true)
	// 【注入新实现的终端输出器】
	reporter := engine.NewTerminalReporter()

	// 我们使用一个固定的 SessionID，以便在多次运行之间共享基于内存的“短期工作记忆”。
	// (在真实的 CLI 中，如果进程重启，Session 的内存历史其实是丢失的。
	// 但这正是我们要演示的重点：即便短期内存丢失，只要 TODO.md 还在，任务就能继续！)
	sessionID := "task_web_server_01"
	sess := ctxpkg.GlobalSessionMgr.GetOrCreate(sessionID, workDir)

	log.Printf("\n>>> 🚀 收到指令: %s\n", *promptPtr)

	// 将用户的 Prompt 压入 Session
	sess.Append(schema.Message{Role: schema.RoleUser, Content: *promptPtr})

	// 唤醒引擎执行
	err := eng.Run(context.Background(), sess, reporter)
	if err != nil {
		log.Fatalf("引擎运行崩溃: %v", err)
	}

	// 6. 初始化飞书 Bot，并通过长连接接收事件
	// bot := feishu.NewFeishuBot(eng)
	// log.Println("🚀 go-tiny-claw 正在启动飞书事件长连接")
	// if err := bot.StartLongConnection(context.Background()); err != nil {
	// 	log.Fatalf("飞书长连接崩溃: %v", err)
	// }
}
