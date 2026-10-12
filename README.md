# go-tiny-claw

> 基于 Go 的轻量级 AI 编程 Agent，探索从大模型对话到工具执行的完整工程链路。

`go-tiny-claw` 将大模型、工具调用、会话上下文和执行反馈组织成一个任务循环，让模型不只生成回答，还能读取文件、编写代码、修改内容和执行命令。

项目围绕 Agent 的核心工程能力展开：通过 Provider 适配模型，通过 Registry 管理工具，通过 Session 维护会话，并逐步加入上下文压缩、项目规则、Skills、文件化任务进度、子智能体、飞书交互、运行追踪与自动化评测。

**当前定位：面向学习、实验和二次开发的 Agent 原型。默认入口运行固定任务演示，并非开箱即用的交互式 CLI 或生产服务。**

实现入口：[主程序](cmd/claw/main.go) · [Agent 引擎](internal/engine/loop.go) · [工具注册表](internal/tools/registry.go)

---

## 功能概览

“默认”指直接运行 `go run ./cmd/claw` 时的装配状态；存在实现不代表默认入口已经启用。

| 能力 | 说明 | 当前状态 |
| --- | --- | --- |
| Agent 执行循环 | 模型决策、工具执行、结果回传，循环处理任务 | 默认启用 |
| 并发工具调用 | 同一轮的多个工具调用使用 goroutine 并发执行 | 默认启用 |
| 文件与终端工具 | `read_file`、`write_file`、`edit_file`、`bash` | 默认注册 |
| 会话管理 | 按会话保存内存历史、工作目录和 Token 用量 | 默认启用，不跨进程持久化 |
| 上下文压缩 | 对较早或过长的内容进行掩码和截断 | 默认启用，规则式处理 |
| 项目规则与 Skills | 加载 `AGENTS.md` 和 `.claw/skills/**/SKILL.md` | 配置对应文件后自动加载 |
| 运行观测 | 记录调用耗时、Token 用量、估算费用和 JSON Trace | 默认启用 |
| Thinking / Plan Mode | 可选的独立模型调用阶段，以及文件化计划与进度 | 默认关闭，需修改代码 |
| 子智能体 | 使用单独的工具集合执行探索任务并返回结果 | 已有实现，默认未注册 |
| 飞书与人工审批 | 长连接收发消息、审批指令与工具拦截示例 | 实验性实现，默认未启用 |
| 自动化评测 | 准备测试环境、执行任务、运行验收脚本并汇总结果 | 独立入口 `cmd/bench` |

具体装配与实现见 [主程序](cmd/claw/main.go)、[上下文模块](internal/context)、[飞书模块](internal/feishu) 和 [评测模块](internal/eval/benchmark.go)。

## 快速开始

### 环境要求

- Go 工具链满足 [go.mod](go.mod) 的版本要求；当前声明为 **Go 1.26.2**。
- 可用的 Bash 环境。以下命令按 macOS / Linux 的 Shell 环境编写；Windows 原生终端不能直接假定兼容。
- 可用的智谱 API Key，以及对所配置模型和 API 端点的访问权限。

> **安全提醒：程序会让模型执行真实的文件操作与 Bash 命令。请在独立、低权限的测试环境中运行，不要使用生产目录或存放敏感数据的目录。当前实现不提供真正的操作系统级沙箱。**

### 获取代码与依赖

```bash
git clone https://github.com/ketitongxue/go-tiny-claw.git
cd go-tiny-claw

go mod download
mkdir -p workspace
```

默认工作区是**进程启动目录下的 `workspace`**。请从仓库根目录执行后续命令，并提前创建该目录；当前主程序不会在调用工具之前统一创建它。

### 配置模型密钥

```bash
export ZHIPU_API_KEY="替换为你的智谱 API Key"
```

默认模型名在 `cmd/claw/main.go` 中设为 `glm-5.2`。默认 Provider 使用智谱的 Coding API 端点，账户需要具备对应访问权限。

模型名与服务地址当前写在源码中，并非通过 `MODEL`、`BASE_URL` 等环境变量配置。实际可用性取决于服务端和账户权限；必要时同时修改模型名与 Provider 地址。

配置依据：[主程序](cmd/claw/main.go) · [默认 Provider](internal/provider/openai.go)

### 运行演示

```bash
go run ./cmd/claw
```

默认任务要求模型在同一轮中分别调用两个工具：执行一条包含 `sleep 2` 的 Bash 命令，以及创建 `trace_test.md`。

成功完成时，可以检查文件和追踪记录：

```bash
cat workspace/trace_test.md
ls -la workspace/.claw/traces
```

终端会输出模型消息、工具调用与执行结果，并在任务结束后打印会话 Token 用量和估算费用。模型是否在同一轮发起两个调用，以实际响应为准。

**当前程序完成预设任务后退出，不会持续等待终端输入。** 修改任务需要编辑 `cmd/claw/main.go` 中的 `prompt`。

### 构建二进制

```bash
mkdir -p bin
go build -o bin/claw ./cmd/claw
./bin/claw
```

构建不会改变工作区定位规则：从哪个目录启动进程，就使用该目录下的 `workspace`。

## 修改任务与配置

### 替换演示任务

编辑 `cmd/claw/main.go`，将现有 `prompt` 替换为自己的任务，例如：

```go
prompt := `
请先查看当前工作区中的文件。
创建一个 README_DEMO.md，用中文简要介绍这个目录中已有的内容。
不要修改其他文件，也不要读取当前任务不需要的敏感信息。
`
```

模型看到的“当前工作区”是工具绑定的目录，而不是自动指向本仓库源码。要处理特定项目，请使用该项目的测试副本，并同步调整工作区配置。

### 配置位置

| 配置项 | 当前默认值 / 行为 | 修改位置 |
| --- | --- | --- |
| 模型密钥 | `ZHIPU_API_KEY` | 环境变量 |
| 模型名 | `glm-5.2` | `cmd/claw/main.go`；评测入口需单独修改 |
| 默认模型适配 | `NewZhipuOpenAIProvider` | `cmd/claw/main.go` |
| 默认 API 地址 | 见下方端点说明 | `internal/provider/openai.go` |
| 工作区 | 启动目录下的 `workspace` | `cmd/claw/main.go` |
| 会话 ID | `test_trace_001` | `cmd/claw/main.go` |
| Thinking / Plan Mode | 均为 `false` | `NewAgentEngine` 的第 3、4 个参数 |
| 上下文压缩 | 阈值 `3000`，近期保护消息数 `6` | `internal/engine/loop.go` |
| Bash 超时 | `30s` | `internal/tools/bash.go` |
| 飞书凭据 | `FEISHU_APP_ID` / `FEISHU_APP_SECRET` | 环境变量，仅启用飞书时需要 |

默认 Provider 中配置的 API 地址是：

```text
https://open.bigmodel.cn/api/coding/paas/v4
```

上下文压缩器使用 Go 的 `len(string)` 估算长度，实际按字节计数，并非准确的字符数或 Token 数；阈值用于触发压缩，不保证压缩后的总量严格低于该阈值。

代码依据：[入口配置](cmd/claw/main.go) · [模型适配](internal/provider/openai.go) · [上下文压缩](internal/context/compactor.go) · [Bash 工具](internal/tools/bash.go)

## 架构与执行流程

```text
任务入口：演示程序 / 飞书消息
             │
             ▼
       Session 会话历史
             │
             ▼
    PromptComposer 组装提示词
    ├─ 核心规则与可选计划规则
    ├─ AGENTS.md 项目规范
    └─ SKILL.md 技能内容
             │
             ▼
     Compactor 裁剪上下文
             │
             ▼
      Provider 调用大模型
             │
       ┌─────┴─────┐
       │           │
    无工具调用   有工具调用
       │           │
       ▼           ▼
   结束本次任务   Registry 分发工具
                   │
                   ▼
             执行并收集结果
                   │
                   ▼
             写回 Session
                   │
                   └──→ 进入下一轮模型调用
```

模型适配、工具实现和输出通道相互分离。`Reporter` 将引擎事件输出到终端或飞书，`CostTracker` 包装 Provider 统计调用信息，Trace 记录任务、轮次、模型调用和工具执行的层级关系。

相关实现：[执行引擎](internal/engine/loop.go) · [Reporter](internal/engine/reporter.go) · [CostTracker](internal/observability/tracker.go) · [Trace](internal/observability/trace.go)

### 目录结构

```text
go-tiny-claw/
├── cmd/
│   ├── claw/                # Agent 演示入口
│   └── bench/               # 自动化评测入口
├── internal/
│   ├── context/             # Session、提示词、上下文压缩、Skills、恢复规则
│   ├── engine/              # 主循环、子任务循环、提醒机制与 Reporter
│   ├── eval/                # 任务评测与脚本验收
│   ├── feishu/              # 飞书长连接、消息反馈和审批逻辑
│   ├── observability/       # Token 统计、费用估算和 Trace
│   ├── provider/            # 模型协议适配
│   ├── schema/              # 消息、工具调用等公共数据结构
│   └── tools/               # 工具实现、注册表与中间件
├── go.mod
└── go.sum
```

运行产生的 `workspace/`、`bin/` 不属于上述源码目录；工作区中的文件应根据任务和敏感性决定是否纳入版本控制。

## 项目规则与技能

### 使用 AGENTS.md

在工作区根目录创建 `AGENTS.md`，写入项目约定。例如：

```markdown
# 项目规范

使用中文解释关键修改。
编辑文件之前先读取现有内容。
优先进行小范围修改，不要重写与任务无关的代码。
完成代码修改后运行相应测试；未运行的测试应明确说明。
不要将密钥、访问令牌或敏感数据写入代码与日志。
```

默认演示入口对应的路径是 `workspace/AGENTS.md`，而不是仓库根目录下的 `AGENTS.md`。

### 使用 SKILL.md

技能文件放在工作区的 `.claw/skills` 下，例如：

```text
workspace/
├── AGENTS.md
└── .claw/
    └── skills/
        └── go-test/
            └── SKILL.md
```

`SKILL.md` 示例：

```markdown
---
name: go-test
description: 当任务涉及编写或补充 Go 单元测试时使用。
---

先阅读被测函数及已有测试，确认输入、输出与边界条件。
遵循已有项目的测试风格，优先使用表驱动测试。
至少覆盖正常输入和与函数相关的边界情况。
使用可用工具运行测试，并根据实际输出说明结果。
```

当前加载器会递归读取匹配的 `SKILL.md`，将内容一起注入系统提示词。它不是按需下载的插件系统，也不会仅凭 Frontmatter 自动执行脚本。`name` 和 `description` 使用简单的逐行解析，建议保持单行文本。

规则与技能在每次 `AgentEngine.Run` 开始时组装；执行过程中修改文件，不会自动重建本轮运行已经生成的系统提示词。

实现依据：[PromptComposer](internal/context/composer.go) · [SkillLoader](internal/context/skill.go) · [引擎初始化](internal/engine/loop.go)

## 可选模式与扩展

以下示例用于修改现有入口的装配代码，不是当前版本已经支持的命令行参数。

### Thinking 模式

将初始化代码调整为：

```go
eng := engine.NewAgentEngine(trackedProvider, registry, true, false)
```

开启后，引擎在行动阶段之前增加一次不提供工具定义的模型调用，并将其返回内容加入后续上下文。这里的 Thinking 是项目自行组织的额外调用阶段，不等同于模型服务商的原生推理模式开关，会增加调用次数和相应用量。

### Plan Mode

```go
eng := engine.NewAgentEngine(trackedProvider, registry, false, true)
```

计划模式通过提示词引导模型使用 `PLAN.md` 保存方案、使用 `TODO.md` 保存任务清单，并在完成步骤后更新勾选状态。已有文件存在时，提示词要求先读取，再继续未完成的任务。

**这是一种文件化任务状态约定，而不是确定性的工作流引擎或完整的会话恢复功能。** Session 历史仍保存在进程内存中，计划文件的创建与更新依赖模型执行。

实现依据：[模式开关与执行流程](internal/engine/loop.go) · [计划模式提示词](internal/context/composer.go)

### 子智能体

主引擎提供 `RunSub` 子任务循环。需要启用时，可在创建 `eng` 之后、调用 `eng.Run` 之前注册子智能体工具：

```go
readOnlyRegistry := tools.NewRegistry()
readOnlyRegistry.Register(tools.NewReadFileTool(workDir))

registry.Register(tools.NewSubagentTool(eng, readOnlyRegistry, reporter))
```

子任务使用独立的消息历史，最多执行 10 轮，结束后向主智能体返回文本结果。工具能力取决于传入的注册表；上例只提供读取已知路径文件的能力。

**不要将通用 Bash 工具加入“只读”注册表后就认为获得了只读隔离。** 工具白名单只是应用层能力限制，不是操作系统级权限隔离。

实现依据：[子智能体工具](internal/tools/subagent.go) · [RunSub](internal/engine/loop.go)

### 飞书交互与人工审批

飞书相关代码位于 `internal/feishu`，包含消息接收、执行过程反馈和审批指令处理。启用前需要准备：

```bash
export FEISHU_APP_ID="你的飞书应用 ID"
export FEISHU_APP_SECRET="你的飞书应用 Secret"
```

仅设置环境变量不会启动飞书。需要在入口中导入飞书模块、装配 Bot，并调用 `StartLongConnection`；还需按实际应用配置完成消息权限和事件订阅。

若启用人工审批，应额外通过 `registry.Use(...)` 注册审批中间件，并正确关联当前任务、聊天、Reporter 和允许审批的用户。代码中的审批指令格式为：

```text
approve <tool_call_id>
reject <tool_call_id>
```

当前入口中的飞书和审批装配示例均处于注释状态，相关代码仍应视为实验性实现。尤其需要验证多消息并发、聊天与工作区对应关系、审批身份校验和超时行为，不能仅取消注释便视为完成生产接入。

实现依据：[入口装配示例](cmd/claw/main.go) · [飞书 Bot](internal/feishu/bot.go) · [审批模块](internal/feishu/approval.go)

## 模型与工具扩展

### 接入其他模型

Provider 将内部消息与工具定义转换为具体 SDK 的请求，并将响应转回项目内部数据结构。新增模型接入时，需要实现 `LLMProvider`，并在入口替换 Provider 的实例化代码。

仓库已有 OpenAI SDK 与 Anthropic SDK 两种适配实现，**但当前两个构造函数都面向智谱端点**，并不代表默认同时接入 OpenAI 和 Claude 官方模型。切换协议或服务商时，需要验证 Base URL、鉴权、模型名、工具调用消息格式和 Token 用量字段，不能假定只换构造函数就完全兼容。

源码：[Provider 接口](internal/provider/interface.go) · [OpenAI 协议实现](internal/provider/openai.go) · [Anthropic 协议实现](internal/provider/claude.go)

### 增加自定义工具

新工具需要实现注册表要求的接口：

```go
type BaseTool interface {
    Name() string
    Definition() schema.ToolDefinition
    Execute(ctx context.Context, args json.RawMessage) (string, error)
}
```

在模型执行任务前，通过 `registry.Register(...)` 注册工具。业务参数解析与校验由工具自身负责；涉及写入、网络访问或命令执行时，应同时考虑权限、超时、输出大小和敏感信息处理。

工具与中间件应在启动任务前完成注册，避免运行期间修改注册表。还需注意，同轮工具调用会并发执行，对同一文件有依赖的操作应通过串行策略或文件级锁控制。

源码：[Registry 与中间件](internal/tools/registry.go) · [并发执行逻辑](internal/engine/loop.go)

## 测试与自动化评测

### 运行单元测试

```bash
go test ./...
```

单元测试和 Agent 真实任务评测是不同入口。执行测试命令不代表所有扩展模块已完成端到端验证。

### 运行 Agent 评测

```bash
go run ./cmd/bench
```

评测入口会调用真实模型，因此需要有效的 `ZHIPU_API_KEY`，并可能消耗 API 额度。当前内置两个任务：

| 任务 | 验收方式 |
| --- | --- |
| 将 JSON 配置版本从 `v1.0.0` 修改为 `v2.0.0` | 通过脚本检查目标内容 |
| 阅读 `Multiply` 函数并生成单元测试 | 初始化临时 Go 模块并执行 `go test` |

每个用例使用单独的工作目录，由 `SetupScript` 准备文件，再由 Agent 执行 `TaskPrompt`，最后通过 `ValidateScript` 的退出状态验收。评测结果会汇总成功率、耗时和估算费用；独立目录不等于真正的执行沙箱。

当前 `TestCase` 虽然声明了 `MaxTurns`，评测器尚未将其用于限制主 Agent 的执行轮数。批量评测前建议补充总超时、轮数与费用预算，避免任务长时间运行。

> 若代码生成用例中的 `math.go` 出现字面量 `\n`，请检查 `SetupScript` 使用的 `echo` 是否按预期解释转义。可将该准备脚本改用 `printf 'package math\n\nfunc Multiply(a, b int) int {\n\treturn a * b\n}\n' > math.go`，再进行评测。

源码：[评测用例](cmd/bench/main.go) · [BenchmarkRunner](internal/eval/benchmark.go)

## 运行追踪与费用统计

Trace 文件保存在当前工作区下：

```text
.claw/traces/trace_<session_id>_<timestamp>.json
```

默认演示对应 `workspace/.claw/traces/`。Trace 包含任务与调用的层级、时间信息，以及部分工具参数和输出预览，可用于排查执行过程；它不是可直接重放任务的完整状态快照。

`CostTracker` 根据模型响应中的 Usage 统计输入、输出 Token，并结合代码中的价格表进行估算。**当前单价为演示假设，代码中的币种注释与输出标识也存在不一致，不能将其作为真实账单或准确的人民币费用。**

发布运行日志或 Trace 前，请检查是否包含源码片段、路径、凭据或业务数据。

源码：[Trace 导出](internal/observability/trace.go) · [费用统计](internal/observability/tracker.go) · [工具追踪属性](internal/tools/registry.go)

## 使用边界与安全建议

**工作目录不是安全沙箱。** Bash 工具直接调用 `bash -c`，设置 `cmd.Dir` 仅指定初始目录，不阻止命令访问目录外文件、网络或进程环境。请使用低权限账户或额外隔离环境，避免挂载敏感目录、宿主控制接口和不必要的凭据。

**超时与并发控制仍需完善。** Bash 工具有单次超时，但主 Agent 没有统一的最大轮数与费用上限，也不应假定单个命令的超时可以清理所有派生进程。同轮工具的并发执行可能产生文件写入冲突。

**规则与审批不能替代权限体系。** `AGENTS.md`、Skills 和 Plan Mode 依赖模型遵循指令；人工审批默认没有接入主程序。安全边界应由独立的权限、隔离与校验机制落实。

**工作区内容可能发送给模型服务。** Agent 读取的文件内容和命令结果会进入后续模型上下文。请先确认任务所涉及数据允许发送到配置的模型端点，并检查日志与 Trace 的留存范围。

实现依据：[Bash 执行](internal/tools/bash.go) · [主循环与上下文](internal/engine/loop.go) · [默认装配](cmd/claw/main.go) · [Provider 请求](internal/provider/openai.go)

## 常见问题

### 为什么设置了 API Key 仍无法调用模型？

检查模型名、账户权限与 Provider 地址是否匹配。当前默认使用 Coding API 端点，不能假定任意智谱 Key 都有对应权限。先查看实际 API 错误，再调整配置；不要将完整密钥贴到日志或 Issue 中。

### 为什么运行后没有出现可输入问题的终端界面？

当前 `cmd/claw` 是固定任务演示程序。修改任务请编辑入口的 `prompt`；交互式 CLI 尚未实现。

### 为什么重启后不记得之前的对话？

Session 历史保存在进程内存中。固定 Session ID 不会让历史跨进程保留。Plan Mode 可引导模型读取 `PLAN.md` 和 `TODO.md`，但不会恢复完整聊天记录。

### 为什么工作目录报错，或没有加载规则与技能？

确认启动目录正确且 `workspace` 已创建。默认入口读取 `workspace/AGENTS.md` 和 `workspace/.claw/skills/`，而不是自动读取仓库根目录的同名文件。

以上行为对应 [入口](cmd/claw/main.go)、[上下文管理](internal/context) 与 [默认 Provider](internal/provider/openai.go)。

## 后续完善方向

以下为可继续推进的方向，不代表已实现能力或发布承诺：

- 配置文件与交互式 CLI：将模型、端点、工作区、任务等参数从入口代码中抽离。
- 执行安全与资源预算：补充隔离、权限校验、总超时、最大轮数、费用预算和写入冲突控制。
- 会话与上下文可靠性：增加持久化、按完整工具交互裁剪历史，以及更准确的 Token 预算管理。
- 扩展能力验证：完善飞书多会话、审批身份校验、子智能体权限和错误恢复链路。
- 工程质量：补充自动化测试、持续集成、可复现评测，以及正确的计费与 Trace 统计。

## 参与贡献

欢迎通过 Issue 讨论问题和改进方向，或通过 Pull Request 提交实现。

提交问题时，请描述预期行为、实际结果、复现步骤和经过脱敏的日志；提交代码时，请说明变更范围及实际执行过的测试，不要将未运行的测试描述为通过。

## 许可证

当前仓库尚未包含 `LICENSE` 文件。许可方式待维护者明确；本 README 不额外声明 MIT、Apache-2.0 等许可证。

---

文档依据：2026-09-27 查阅的 `main` 分支源码。示例按当前入口组织，未在本次文档编写过程中调用真实模型进行端到端验证；服务可用性、账户权限与任务结果以实际运行情况为准。
