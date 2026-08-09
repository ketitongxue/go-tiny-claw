package engine

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/ketitongxue/go-tiny-claw/internal/provider"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
	"github.com/ketitongxue/go-tiny-claw/internal/tools"
)

// AgentEngine 是微型 OS 的核心驱动
type AgentEngine struct {
	provider provider.LLMProvider
	registry tools.Registry

	// WorkDir (工作区): 借鉴 OpenClaw 的理念，Agent 必须有一个明确的物理边界
	WorkDir string

	// 思考模式开关
	EnableThinking bool
}

func NewAgentEngine(p provider.LLMProvider, r tools.Registry, workDir string, enableThinking bool) *AgentEngine {
	return &AgentEngine{
		provider:       p,
		registry:       r,
		WorkDir:        workDir,
		EnableThinking: enableThinking,
	}
}

// Run 启动 Agent 的生命周期
func (e *AgentEngine) Run(ctx context.Context, userPrompt string, reporter Reporter) error {
	log.Printf("[Engine] 引擎启动，锁定工作区: %s\n", e.WorkDir)
	log.Printf("[Engine] 慢思考模式 (Thinking Phase): %v\n", e.EnableThinking)

	// 1. 初始化会话的 Context (上下文内存)
	// 在真实的场景中，这里会由动态 Prompt 组装器加载 AGENTS.md。目前我们先硬编码。
	contextHistory := []schema.Message{
		{
			Role:    schema.RoleSystem,
			Content: "You are go-tiny-claw, an expert coding assistant. You have full access to tools in the workspace.",
		},
		{
			Role:    schema.RoleUser,
			Content: userPrompt,
		},
	}

	turnCount := 0

	// 2. The Main Loop: 心跳开始 (标准的 ReAct 循环)
	for {
		turnCount++
		log.Printf("========== [Turn %d] 开始 ==========\n", turnCount)

		// 获取当前挂载的所有工具定义
		availableTools := e.registry.GetAvailableTools()

		// ====================================================================
		// Phase 1: 慢思考阶段 (Thinking) - 剥夺工具，强制规划
		// ====================================================================

		if e.EnableThinking {
			if reporter != nil {
				// 【触发 Reporter】: 开始慢思考
				reporter.OnThinking(ctx)
			}
			log.Println("[Engine][Phase 1] 剥夺工具访问权，强制进入慢思考与规划阶段...")

			// 核心机制：传入的 availableTools 为 nil！
			// 大模型看不到任何 JSON Schema，被迫只能输出纯文本的思考过程。
			thinkResp, err := e.provider.Generate(ctx, contextHistory, nil)
			if err != nil {
				return fmt.Errorf("Thinking 阶段生成失败: %w", err)
			}

			// 如果模型输出了思考过程，我们将其作为 Assistant 消息追加到上下文中
			if thinkResp.Content != "" {
				fmt.Printf("🧠 [内部思考 Trace]: %s\n", thinkResp.Content)
				contextHistory = append(contextHistory, *thinkResp)
			}
		}

		// ====================================================================
		// Phase 2: 行动阶段 (Action) - 恢复工具，顺着规划执行
		// ====================================================================
		log.Println("[Engine][Phase 2] 恢复工具挂载，等待模型采取行动...")

		// 此时的 contextHistory 中已经包含了上一阶段模型自己的 Thinking Trace。
		// 模型会顺着自己的逻辑，结合恢复的 availableTools 发起精准的工具调用。
		actionResp, err := e.provider.Generate(ctx, contextHistory, availableTools)
		if err != nil {
			return fmt.Errorf("Action 阶段生成失败: %w", err)
		}

		contextHistory = append(contextHistory, *actionResp)

		if actionResp.Content != "" && reporter != nil {
			// 【触发 Reporter】: 输出阶段性总结或最终回复
			reporter.OnMessage(ctx, actionResp.Content)
			fmt.Printf("🤖 [对外回复]: %s\n", actionResp.Content)
		}

		// ====================================================================
		// 退出与执行逻辑
		// ====================================================================

		// 3. 退出条件判断
		// 如果模型没有请求任何工具调用，说明它认为任务已经完成，跳出循环。
		if len(actionResp.ToolCalls) == 0 {
			log.Println("[Engine] 模型未请求调用工具，任务宣告完成。")
			break
		}

		// 4. 执行行动 (Action) 与 获取观察结果 (Observation)
		log.Printf("[Engine] 模型请求调用 %d 个工具...\n", len(actionResp.ToolCalls))

		// 【核心改造开始】: 从串行 (Sequential) 演进为并行 (Parallel)

		// 1. 预分配一个固定长度的切片，用于安全地存放各个并发工具的执行结果（Observation）
		// 长度与 ToolCalls 的数量完全一致
		observationMsgs := make([]schema.Message, len(actionResp.ToolCalls))

		// 2. 声明 WaitGroup 用于阻塞等待所有协程完成
		var wg sync.WaitGroup

		for i, toolCall := range actionResp.ToolCalls {
			wg.Add(1) // 增加计数器

			// 开启协程。注意：一定要将索引 i 和 toolCall 作为参数传入匿名函数，防止闭包变量捕获陷阱！
			go func(idx int, call schema.ToolCall) {
				defer wg.Done() // 协程结束时计数器减一

				if reporter != nil {
					// 【触发 Reporter】: 报告即将在底层执行的工具
					reporter.OnToolCall(ctx, call.Name, string(call.Arguments))
				}

				// 调用底层 Registry 执行工具（物理操作）
				result := e.registry.Execute(ctx, call)

				if reporter != nil {
					// 为了防止大文件读取导致飞书消息过长被截断，我们仅汇报工具执行状态
					// 注意：传递给大模型的 observationMsgs 依然是完整数据，只是人类看到的 Reporter 是缩略版
					displayOutput := result.Output
					if len(displayOutput) > 200 {
						displayOutput = displayOutput[:200] + "... (已截断)"
					}
					// 【触发 Reporter】: 汇报工具物理执行的结果
					reporter.OnToolResult(ctx, call.Name, displayOutput, result.IsError)
				}

				// 将执行结果封装为一条用户消息 (RoleUser)
				obsMsg := schema.Message{
					Role:       schema.RoleUser,
					Content:    result.Output,
					ToolCallID: call.ID,
				}

				// 【线程安全】: 由于每个 Goroutine 操作的是预分配切片的不同索引，
				// 这里不需要加锁 (Mutex)，性能极高！
				observationMsgs[idx] = obsMsg

			}(i, toolCall) // 闭包传参
		}

		// 4. Join 阻塞等待：主循环挂起，直到所有的并发协程全部执行完毕
		wg.Wait()
		log.Println("[Engine] 所有并发工具执行完毕，开始聚合观察结果 (Observation)...")

		// 5. 聚合装填：将并行的结果，按照原本的顺序，一次性追加到上下文时间线中
		// 这等价于 contextHistory = append(contextHistory, observationMsgs...)
		for _, obs := range observationMsgs {
			contextHistory = append(contextHistory, obs)
		}

		// 循环回到开头，模型将带着新加入的 Observation 继续它的下一轮思考...
	}

	return nil
}
