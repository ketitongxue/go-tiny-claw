# go-tiny-claw

> 基于 Go 的轻量级 AI 编程 Agent，探索从大模型对话到工具执行的完整工程链路。

`go-tiny-claw` 将大模型、工具调用、会话上下文和执行反馈组织成一个任务循环，让模型不只生成回答，还能读取文件、编写代码、修改内容和执行命令。

项目围绕 Agent 的核心工程能力展开：通过 Provider 适配模型，通过 Registry 管理工具，通过 Session 维护会话，并逐步加入上下文压缩、项目规则、Skills、文件化任务进度、子智能体、飞书交互、运行追踪与自动化评测。

**当前定位：面向学习、实验和二次开发的 Agent 原型。默认入口为飞书长连接，`-mode=demo` 运行终端任务；不是生产服务或交互式终端 CLI。默认 Bash 使用 Docker Sandbox，初始化失败不会回退到宿主执行。**

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
| Thinking / Plan Mode | 独立模型调用阶段，以及文件化计划与进度 | 默认关闭，使用 `-thinking` / `-plan` |
| Sandbox | 可插拔 Local / Docker Executor，失败不降级 | 默认 `auto`，仅选择 Docker |
| 子智能体 | Explorer 使用独立的 RO Sandbox 和工具集合 | 默认注册；Local 模式只开放 `read_file` |
| 飞书与人工审批 | 长连接收发消息和高危工具的人工审批 | 默认 `-mode=feishu`，仍属实验性 |
| 自动化评测 | 准备测试环境、执行任务、运行验收脚本并汇总结果 | 独立入口 `cmd/bench` |

具体装配与实现见 [主程序](cmd/claw/main.go)、[上下文模块](internal/context)、[飞书模块](internal/feishu) 和 [评测模块](internal/eval/benchmark.go)。

## 快速开始

### 环境要求

- Go 工具链满足 [go.mod](go.mod) 的版本要求；当前声明为 **Go 1.26.2**。
- 默认模式需要可用的本机 Docker daemon（Docker Engine 25+，支持 `bind-recursive=disabled`；macOS 可用 Docker Desktop）以及预先构建的 Sandbox 镜像。
- 显式 `-sandbox=local` 需要宿主 Bash，但**不提供 OS 级隔离**。以下命令按 macOS / Linux 编写。
- 可用的智谱 API Key，以及对所配置模型和 API 端点的访问权限；飞书模式另需应用凭据。

> **安全提醒：Coding Sandbox 中的 workspace 是可写的，模型仍能删除或修改工作区文件。请使用独立测试副本，不要在工作区中放置密钥、socket 或敏感数据。Docker 不是完整 VM 安全边界，详见 [Threat Model](docs/security.md)。**

### 获取代码与依赖

```bash
git clone https://github.com/ketitongxue/go-tiny-claw.git
cd go-tiny-claw

go mod download
mkdir -p workspace
```

默认工作区是**进程启动目录下的 `workspace`**，也可使用 `-workdir` 指定；正常启动会创建缺失目录。工作区内容不随源码发布。若在仓库内的工作区生成 Go 文件，请为工作区创建独立 `go.mod`，避免主项目测试扫描；这不是权限隔离。

构建基础镜像（仅构建阶段需要下载依赖）：

```bash
docker build -f Dockerfile.sandbox -t go-tiny-claw-sandbox:latest .
```

### 配置模型密钥

```bash
export ZHIPU_API_KEY="替换为你的智谱 API Key"
```

默认模型名为 `glm-5.3`，可用 `-model` 修改。默认 Provider 使用智谱 Coding API 端点，账户需要具备对应访问权限。

服务地址仍配置在 Provider 源码中，不通过 `BASE_URL` 环境变量修改。模型是否可用取决于服务端和账户权限。

配置依据：[主程序](cmd/claw/main.go) · [默认 Provider](internal/provider/openai.go)

### 运行演示

```bash
go run ./cmd/claw -mode=demo -sandbox=docker
```

默认演示任务要求模型在同一轮中分别调用两个工具：执行一条包含 `sleep 2` 的 Bash 命令，以及创建 `trace_test.md`。

成功完成时，可以检查文件和追踪记录：

```bash
cat workspace/trace_test.md
ls -la workspace/.claw/traces
```

终端会输出模型消息、工具调用与执行结果，并在任务结束后打印会话 Token 用量和估算费用。模型是否在同一轮发起两个调用，以实际响应为准。

**demo 完成任务后退出，不会持续等待终端输入。** 使用 `-prompt` 修改任务。不传 `-mode=demo` 时会启动飞书，需要 `FEISHU_APP_ID` / `FEISHU_APP_SECRET`。

离线检查参数和凭据是否已设置（不会启动模型、Docker 或飞书，也不创建工作区）：

```bash
go run ./cmd/claw -mode=demo -check-config
```

### 构建二进制

```bash
mkdir -p bin
go build -o bin/claw ./cmd/claw
./bin/claw -mode=demo -sandbox=docker
```

构建不会改变工作区定位规则：从哪个目录启动进程，就使用该目录下的 `workspace`。

## 修改任务与配置

### 替换演示任务

通过 CLI 指定自己的任务，例如：

```bash
go run ./cmd/claw -mode=demo -sandbox=docker -workdir=workspace \
  -prompt='先查看工作区，再创建 README_DEMO.md 简要介绍已有内容。不要修改其他文件。'
```

模型看到的“当前工作区”是工具绑定的目录，而不是自动指向本仓库源码。要处理特定项目，请使用该项目的测试副本，并同步调整工作区配置。

### 配置位置

| 配置项 | 当前默认值 / 行为 | 修改位置 |
| --- | --- | --- |
| 模型密钥 | `ZHIPU_API_KEY` | 环境变量 |
| 模型名 | `glm-5.3` | `-model`；评测入口独立配置 |
| 默认模型适配 | `NewZhipuOpenAIProvider` | `cmd/claw/main.go` |
| 默认 API 地址 | 见下方端点说明 | `internal/provider/openai.go` |
| 工作区 | 启动目录下的 `workspace` | `-workdir` |
| 会话 ID | demo 为 `test_trace_001`；飞书按聊天隔离 | `cmd/claw/main.go` / `internal/feishu` |
| Thinking / Plan Mode | 均为 `false` | `-thinking` / `-plan` |
| 上下文压缩 | 阈值 `3000`，近期保护消息数 `6` | `internal/engine/loop.go` |
| Bash 后端 | `auto`（仅 Docker，失败拒绝启动） | `-sandbox=auto/docker/local` |
| Sandbox 镜像 | `go-tiny-claw-sandbox:latest` | `-sandbox-image` |
| Bash 超时与权限 | `30s`，Coding RW / Explorer RO，网络 OFF | `internal/executor/policy.go` |
| 飞书凭据 | `FEISHU_APP_ID` / `FEISHU_APP_SECRET` | 环境变量，仅启用飞书时需要 |

默认 Provider 中配置的 API 地址是：

```text
https://open.bigmodel.cn/api/coding/paas/v4
```

上下文压缩器使用 Go 的 `len(string)` 估算长度，实际按字节计数，并非准确的字符数或 Token 数；阈值用于触发压缩，不保证压缩后的总量严格低于该阈值。

代码依据：[入口配置](cmd/claw/main.go) · [模型适配](internal/provider/openai.go) · [上下文压缩](internal/context/compactor.go) · [Bash 工具](internal/tools/bash.go)

## Sandbox

```text
AgentEngine → Registry → BashTool → Executor
                                  ├ LocalExecutor  → Host（UNSAFE）
                                  └ DockerExecutor → disposable Container + SandboxPolicy
```

BashTool 仅解析参数、整理输出和标记失败；Executor 负责 cwd、环境、进程、超时、取消和隔离。Sandbox 权限由装配时的 Policy 决定，模型不能通过 Bash 参数更改。

### 选择执行后端

```bash
# 推荐：必须有可用 daemon 和预先构建的镜像
./bin/claw -mode=demo -sandbox=docker

# 默认：Docker 可用则使用 Docker；不可用或镜像缺失则报错退出，绝不回退 Local
./bin/claw -mode=demo -sandbox=auto

# 仅用于开发、调试和明确受信任的环境；启动会打印醒目警告
./bin/claw -mode=demo -sandbox=local

# 自定义可信镜像
./bin/claw -mode=demo -sandbox=docker -sandbox-image=my-claw-sandbox:v1
```

**LocalExecutor 不提供 OS 级隔离。workdir 不是安全边界。** Local 命令拥有当前用户权限，可读取宿主文件、访问网络或修改目录外内容；飞书 Local 模式会额外警告远程执行风险。

### 默认策略

| 策略 | Workspace | 网络 | 内存 | CPU | PIDs | 超时 |
| --- | --- | --- | --- | --- | --- | --- |
| Coding | RW | OFF | 1024 MB | 2 | 128 | 30s |
| Explorer | RO | OFF | 512 MB | 1 | 64 | 30s |

每条命令使用独立的 `docker run --rm`，RootFS 为只读，`/tmp` 是 128 MB tmpfs，drop ALL capabilities，禁止提权，不使用 privileged、host network/PID 或 Docker socket。仅绑定 canonical WorkDir 到 `/workspace`，不包含递归子挂载；Unix 上使用宿主 UID/GID，避免产生 root 所有的工作区文件。

不允许挂载 `/`、整个 HOME、HOME 的祖先、`.ssh`、`.aws`、`.config` 或 Docker socket；含宿主 socket/设备的工作区也会被拒绝。正常的 `$HOME/project` 可以使用。路径中的空格、逗号和引号使用 args slice 与 CSV mount 编码处理。

**Explorer 使用独立 DockerExecutor + ExplorerPolicy，操作系统拒绝 `echo hacked > main.go`、`rm main.go` 等写入。** 它仍可写临时 `/tmp`，但不能修改 workspace。Local 无法强制只读，因此 Local 模式的 Explorer 仅有 `read_file`，不提供 Bash。

### 环境、输出与生命周期

- Docker 不自动继承宿主变量；默认白名单仅允许显式 `Request.Env` 中的 `PATH/LANG/LC_ALL/TERM`。镜像自身提供 Go 的 PATH 和临时缓存配置。API Key / Token 留在主进程，不会自动传给容器。
- 基础镜像含 Bash、Git、curl、CA、ripgrep、Go。网络关闭时不能下载 Go modules；请预先准备 workspace 的 vendor 依赖或可信离线镜像。
- 每次执行结束即删除容器，`/tmp` 和缓存不跨命令持久化。后台服务、持久会话容器暂未实现。
- stdout/stderr、ExitCode、Duration 结构化保存；普通非零退出不是 Executor 基础设施错误，但 BashTool 将其标记为 `IsError=true`。失败输出会进入 Recovery → Observation → 下一轮模型。
- 模型侧 Bash 输出最多 8000 字节（含截断标记），失败时保留退出码及两种输出的预算。Executor 暂保存完整输出；这不是宿主输出内存的硬限制。
- `OutputHandler` 预留流式回调，可为 nil；`context.Cancel()` 在 Unix 杀本地进程组，Docker 则在取消 CLI 后通过独立 Context 强制清理容器。清理失败会报告错误，不降级。

详见 [安全边界与限制](docs/security.md)。

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
│   ├── executor/            # Request/Result、Local/Docker 执行、SandboxPolicy
│   ├── feishu/              # 飞书长连接、消息反馈和审批逻辑
│   ├── observability/       # Token 统计、费用估算和 Trace
│   ├── provider/            # 模型协议适配
│   ├── schema/              # 消息、工具调用等公共数据结构
│   └── tools/               # 工具实现、注册表与中间件
├── Dockerfile.sandbox       # Go + 基础命令镜像
├── docs/security.md         # Threat Model
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

CLI 支持 `-thinking` / `-plan`；以下 Go 示例用于二次开发中的自定义装配。

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

默认已注册 Explorer 子智能体。主引擎提供 `RunSub` 子任务循环；二次开发时可按如下方式装配独立的 RO Docker Executor：

```go
explorerPolicy := executor.ExplorerPolicy()
explorerExec, err := executor.NewConfigured(ctx, "docker", image, explorerPolicy)
if err != nil {
    return err // 不回退到宿主 Bash
}
readOnlyRegistry := tools.NewRegistry()
readOnlyRegistry.Register(tools.NewReadFileTool(workDir))
readOnlyRegistry.Register(tools.NewBashTool(workDir, explorerExec, explorerPolicy))
registry.Register(tools.NewSubagentTool(eng, readOnlyRegistry, reporter))
```

子任务使用独立的消息历史，最多执行 10 轮，结束后向主智能体返回文本结果。上例支持读取和只读 workspace 的 Bash；不可与主 Agent 共用 RW Executor。

**不要将通用 Bash 工具加入“只读”注册表后就认为获得了只读隔离。** 工具白名单只是应用层能力限制，不是操作系统级权限隔离。

实现依据：[子智能体工具](internal/tools/subagent.go) · [RunSub](internal/engine/loop.go)

### 飞书交互与人工审批

飞书相关代码位于 `internal/feishu`，包含消息接收、执行过程反馈和审批指令处理。启用前需要准备：

```bash
export FEISHU_APP_ID="你的飞书应用 ID"
export FEISHU_APP_SECRET="你的飞书应用 Secret"
```

完成消息权限和事件订阅配置后启动（默认模式也是 feishu）：

```bash
./bin/claw -mode=feishu -sandbox=docker
```

入口通过 `registry.Use(...)` 接入高危命令审批，并按聊天分别创建引擎、会话与审批闭包。代码中的审批指令格式为：

```text
approve <tool_call_id>
reject <tool_call_id>
```

飞书仍应视为实验性接入。**`IsDangerousCommand` 是 Human Approval Layer 的启发式规则，不是 Security Boundary。** 规则漏判也必须由 Docker Sandbox 限制；人工批准不会解除 Sandbox 的权限限制。远程服务还需要正确的身份、访问和数据权限配置。

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
go vet ./...
```

Local、Docker args、Policy、CLI 和 Recovery 单元测试不依赖真实模型。真实 Docker integration tests 在 daemon 或测试镜像不可用时 Skip；生产 `auto` 模式不会因此降级。

```bash
docker build -f Dockerfile.sandbox -t go-tiny-claw-sandbox:latest .
go test -count=1 -v ./internal/executor -run DockerIntegration
go test -count=1 -v ./cmd/claw -run DockerSessionFactoryIntegration
# 可通过 CLAW_SANDBOX_TEST_IMAGE 指定自定义测试镜像
```

第二条 integration 流程使用 fake Provider + 真实 Docker，覆盖 read/write/edit/bash/subagent、Explorer 写入被拒绝及 Recovery，不需要模型 API Key。测试通过不代表真实模型凭据或飞书网络已经验证。

### 运行 Agent 评测

```bash
go run ./cmd/bench -sandbox=docker
```

评测入口会调用真实模型，因此需要有效的 `ZHIPU_API_KEY`，并可能消耗 API 额度。当前内置两个任务：

| 任务 | 验收方式 |
| --- | --- |
| 将 JSON 配置版本从 `v1.0.0` 修改为 `v2.0.0` | 通过脚本检查目标内容 |
| 阅读 `Multiply` 函数并生成单元测试 | 初始化临时 Go 模块并执行 `go test` |

每个用例使用单独的工作目录，由 `SetupScript` 准备文件，再由 Agent 执行 `TaskPrompt`，最后通过 `ValidateScript` 的退出状态验收。准备、Agent Bash 和验收脚本统一使用注入的 Executor。评测入口同样默认 fail-closed `auto`，支持 `-sandbox` / `-sandbox-image`；只有显式 Local 才会宿主执行并打印警告。评测结果汇总成功率、耗时和估算费用。

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

**工作目录不是安全沙箱。** LocalExecutor 仅设置初始 cwd；DockerExecutor 才提供 OS 级命令隔离。Coding 的 workspace RW、宿主侧文件工具、可信镜像、Docker runtime 和配置仍是重要信任边界，详见 [docs/security.md](docs/security.md)。

**预算与并发控制仍需完善。** Bash 有单次超时和取消清理，但主 Agent 没有统一的最大轮数与费用上限。Local 子进程可逃离进程组；Docker daemon 不可用、主进程崩溃或 SIGKILL 也可能阻止清理。同轮文件操作可能产生冲突，完整输出仍可能消耗大量宿主内存。

**规则与审批不能替代 Sandbox。** Prompt、`AGENTS.md`、Skills、Plan Mode 和人工审批都不是 OS 安全边界。审批规则漏判不能导致脱离 Sandbox。

**工作区内容可能发送给模型服务。** Agent 读取的文件内容和命令结果会进入后续模型上下文。请先确认任务所涉及数据允许发送到配置的模型端点，并检查日志与 Trace 的留存范围。

实现依据：[Bash 执行](internal/tools/bash.go) · [主循环与上下文](internal/engine/loop.go) · [默认装配](cmd/claw/main.go) · [Provider 请求](internal/provider/openai.go)

## 常见问题

### 为什么设置了 API Key 仍无法调用模型？

检查模型名、账户权限与 Provider 地址是否匹配。当前默认使用 Coding API 端点，不能假定任意智谱 Key 都有对应权限。先查看实际 API 错误，再调整配置；不要将完整密钥贴到日志或 Issue 中。

### 为什么运行后没有出现可输入问题的终端界面？

`-mode=demo` 执行一次任务后退出，用 `-prompt` 修改任务；交互式终端 CLI 尚未实现。默认 `-mode=feishu` 等待飞书消息。

### 为什么重启后不记得之前的对话？

Session 历史保存在进程内存中。固定 Session ID 不会让历史跨进程保留。Plan Mode 可引导模型读取 `PLAN.md` 和 `TODO.md`，但不会恢复完整聊天记录。

### 为什么工作目录报错，或没有加载规则与技能？

确认 `-workdir` 指向正确的项目目录。正常启动会创建缺失目录，Docker 执行前还会校验 canonical 路径和挂载安全性。默认读取 `workspace/AGENTS.md` 和 `workspace/.claw/skills/`，不是仓库根目录的同名文件。

### 为什么 auto 模式在没有 Docker 时退出？

这是有意的 fail-closed 行为。启动 Docker daemon 并构建 `Dockerfile.sandbox`；仅在明确可信的开发环境中显式设置 `-sandbox=local`，不会自动回退。

### 为什么 Sandbox 中 go test 不能下载依赖？

默认网络关闭，且每条命令使用独立容器和临时缓存。请事先准备 vendor 依赖或可信离线镜像，不要让模型绕过 Sandbox 获取宿主凭证或网络。

以上行为对应 [入口](cmd/claw/main.go)、[上下文管理](internal/context) 与 [默认 Provider](internal/provider/openai.go)。

## 后续完善方向

以下为可继续推进的方向，不代表已实现能力或发布承诺：

- 配置文件与交互式 CLI：CLI 已支持模型、工作区、任务和 Sandbox 参数；继续完善端点配置与交互。
- 执行安全与资源预算：总超时、最大轮数、费用预算、输出文件化、写入冲突控制，以及后续可选 Executor。
- Session Sandbox：未来可增加会话级容器与连续缓存；本版本不实现持久容器、bwrap、VM 或远程执行。
- 会话与上下文可靠性：增加持久化、按完整工具交互裁剪历史，以及更准确的 Token 预算管理。
- 扩展能力验证：完善飞书多会话、审批身份校验、子智能体权限和错误恢复链路。
- 工程质量：补充自动化测试、持续集成、可复现评测，以及正确的计费与 Trace 统计。

## 参与贡献

欢迎通过 Issue 讨论问题和改进方向，或通过 Pull Request 提交实现。

提交问题时，请描述预期行为、实际结果、复现步骤和经过脱敏的日志；提交代码时，请说明变更范围及实际执行过的测试，不要将未运行的测试描述为通过。

## 许可证

当前仓库尚未包含 `LICENSE` 文件。许可方式待维护者明确；本 README 不额外声明 MIT、Apache-2.0 等许可证。

---

文档依据：当前 Executor/Sandbox 实现及 CLI 装配。单元测试与真实 Docker 隔离/工具流程已验证；真实模型和飞书服务可用性、账户权限与任务结果仍以实际运行为准。
