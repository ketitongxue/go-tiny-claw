package main

import (
	"context"
	"log"
	"os"

	ctxpkg "github.com/ketitongxue/go-tiny-claw/internal/context"
	"github.com/ketitongxue/go-tiny-claw/internal/engine"
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

	reporter := engine.NewTerminalReporter()

	// 3. 初始化真实的 Tool Registry
	// registry := tools.NewRegistry()

	// 4. 将真实的 ReadFile 工具挂载到注册表中
	// registry.Register(tools.NewReadFileTool(workDir))
	// registry.Register(tools.NewWriteFileTool(workDir))
	// registry.Register(tools.NewBashTool(workDir))
	// registry.Register(tools.NewEditFileTool(workDir))

	// 【防御沙箱】为子智能体准备受限的只读注册表
	readOnlyRegistry := tools.NewRegistry()
	readOnlyRegistry.Register(tools.NewReadFileTool(workDir))
	readOnlyRegistry.Register(tools.NewBashTool(workDir)) // 允许简单的 grep 等搜索操作

	// 为主智能体准备全功能注册表
	mainRegistry := tools.NewRegistry()
	mainRegistry.Register(tools.NewReadFileTool(workDir))
	mainRegistry.Register(tools.NewWriteFileTool(workDir))
	mainRegistry.Register(tools.NewBashTool(workDir))
	mainRegistry.Register(tools.NewEditFileTool(workDir))

	// 5. 实例化核心引擎，由于任务简单，我们关闭思考阶段 (EnableThinking = false) 以加快速度
	eng := engine.NewAgentEngine(llmProvider, mainRegistry, false, false)
	// 【注入新实现的终端输出器】
	// reporter := engine.NewTerminalReporter()

	// 【核心装配】：将带有 Engine 引用和只读 Registry 的 Subagent 工具注册进主线
	mainRegistry.Register(tools.NewSubagentTool(eng, readOnlyRegistry, reporter))

	// 我们使用一个固定的 SessionID，以便在多次运行之间共享基于内存的“短期工作记忆”。
	// (在真实的 CLI 中，如果进程重启，Session 的内存历史其实是丢失的。
	// 但这正是我们要演示的重点：即便短期内存丢失，只要 TODO.md 还在，任务就能继续！)
	sessionID := "test_subagent_001"
	sess := ctxpkg.GlobalSessionMgr.GetOrCreate(sessionID, workDir)
	sess.Append(schema.Message{Role: schema.RoleUser, Content: ""})

	prompt := `
    我需要你在这个遗留项目里，找到那个“核心密码”。
    为了防止污染主上下文，请你务必派出子智能体（spawn_subagent）去执行探索任务。
    你可以让子智能体使用 bash 去查找当前目录（及其所有子目录）下名为 config.txt 的文件。
    子智能体拿到密码向你汇报后，请你亲自使用 write_file 工具，将密码写在根目录的 answer.txt 里。
    `

	log.Println("\n>>> 🚀 启动多智能体协同测试...")
	sess.Append(schema.Message{Role: schema.RoleUser, Content: prompt})

	err := eng.Run(context.Background(), sess, reporter)
	if err != nil {
		log.Fatalf("引擎运行崩溃: %v", err)
	}

	// 6. 初始化飞书 Bot，并通过长连接接收事件
	// bot := feishu.NewFeishuBot(eng, sess)

	// 【核心注入】注册安全拦截 Middleware
	// registry.Use(func(ctx context.Context, call schema.ToolCall) (bool, string) {
	// 	argsStr := string(call.Arguments)

	// 	// 检查是否命中高危特征库
	// 	if feishu.IsDangerousCommand(call.Name, argsStr) {
	// 		taskID := call.ID // 使用大模型生成的唯一 ToolCallID 作为 TaskID

	// 		// 挂起当前协程，发送消息给飞书，死死等待人类的审批！
	// 		allowed, reason := feishu.GlobalApprovalMgr.WaitForApproval(taskID, call.Name, argsStr, bot.Reporter())

	// 		if !allowed {
	// 			return false, reason // 拒绝，将理由传回给大模型
	// 		}
	// 		return true, "" // 同意，放行底层工具
	// 	}

	// 	// 没命中黑名单，直接 YOLO 放行
	// 	return true, ""
	// })

	// log.Println("🚀 go-tiny-claw 正在启动飞书事件长连接")
	// if err := bot.StartLongConnection(context.Background()); err != nil {
	// 	log.Fatalf("飞书长连接崩溃: %v", err)
	// }
}
