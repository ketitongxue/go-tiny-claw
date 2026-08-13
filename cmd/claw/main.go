package main

import (
	"context"
	"log"
	"os"

	ctxpkg "github.com/ketitongxue/go-tiny-claw/internal/context"
	"github.com/ketitongxue/go-tiny-claw/internal/engine"
	"github.com/ketitongxue/go-tiny-claw/internal/feishu"
	"github.com/ketitongxue/go-tiny-claw/internal/provider"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
	"github.com/ketitongxue/go-tiny-claw/internal/tools"
)

func main() {
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
	eng := engine.NewAgentEngine(llmProvider, registry, false, false)
	// 【注入新实现的终端输出器】
	// reporter := engine.NewTerminalReporter()

	// 我们使用一个固定的 SessionID，以便在多次运行之间共享基于内存的“短期工作记忆”。
	// (在真实的 CLI 中，如果进程重启，Session 的内存历史其实是丢失的。
	// 但这正是我们要演示的重点：即便短期内存丢失，只要 TODO.md 还在，任务就能继续！)
	sessionID := "test_command_intercept_001"
	sess := ctxpkg.GlobalSessionMgr.GetOrCreate(sessionID, workDir)
	sess.Append(schema.Message{Role: schema.RoleUser, Content: ""})

	// 6. 初始化飞书 Bot，并通过长连接接收事件
	bot := feishu.NewFeishuBot(eng, sess)

	// 【核心注入】注册安全拦截 Middleware
	registry.Use(func(ctx context.Context, call schema.ToolCall) (bool, string) {
		argsStr := string(call.Arguments)

		// 检查是否命中高危特征库
		if feishu.IsDangerousCommand(call.Name, argsStr) {
			taskID := call.ID // 使用大模型生成的唯一 ToolCallID 作为 TaskID

			// 挂起当前协程，发送消息给飞书，死死等待人类的审批！
			allowed, reason := feishu.GlobalApprovalMgr.WaitForApproval(taskID, call.Name, argsStr, bot.Reporter())

			if !allowed {
				return false, reason // 拒绝，将理由传回给大模型
			}
			return true, "" // 同意，放行底层工具
		}

		// 没命中黑名单，直接 YOLO 放行
		return true, ""
	})

	log.Println("🚀 go-tiny-claw 正在启动飞书事件长连接")
	if err := bot.StartLongConnection(context.Background()); err != nil {
		log.Fatalf("飞书长连接崩溃: %v", err)
	}
}
