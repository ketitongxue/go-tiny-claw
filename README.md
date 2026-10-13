# go-tiny-claw

> 一个用 Go 编写的轻量级 Coding Agent Harness，探索从大模型推理、工具执行、多会话交互到 Sandbox、安全审批、可观测与自动化评测的完整工程链路。

`go-tiny-claw` 将大模型、工具调用、会话上下文、执行环境和反馈循环组织成一个可扩展的 Agent Runtime。

它不只是让模型“回答问题”，而是允许模型在受控工作区中：

- 读取、创建和修改文件
- 执行 Bash 命令
- 并发调用多个工具
- 委派 Explorer Subagent 深入探索代码
- 通过飞书进行远程多会话交互
- 对部分高风险操作进行人工审批
- 通过 Docker Sandbox 隔离 Bash 执行
- 记录 Token、调用耗时和 Trace
- 使用 Benchmark 自动验证 Agent 是否真正完成任务

当前项目定位：

> **面向学习、实验和二次开发的软件工程 Agent Harness 原型。**

它已经具备较完整的 Agent 工程骨架，但仍不是 Codex、Claude Code、Pi 等成熟 Coding Agent 的替代品，也不应直接作为无人值守的生产级代码执行平台。

---

## 功能概览

| 能力                                  | 当前状态        |
| ----------------------------------- | ----------- |
| ReAct Agent Loop                    | ✅           |
| OpenAI-compatible Provider          | ✅           |
| 智谱 Coding API                       | ✅ 默认        |
| 文件读取 / 写入 / 编辑                      | ✅           |
| Bash                                | ✅           |
| 并发 Tool Calling                     | ✅           |
| Executor 抽象                         | ✅           |
| LocalExecutor                       | ✅，不安全       |
| DockerExecutor                      | ✅           |
| Docker Sandbox                      | ✅           |
| Coding / Explorer 独立 Sandbox Policy | ✅           |
| Explorer 只读工作区                      | ✅ Docker 模式 |
| Subagent                            | ✅ 默认启用      |
| 多会话                                 | ✅           |
| 飞书长连接                               | ✅ 默认入口      |
| 同会话顺序执行                             | ✅           |
| 不同飞书会话并行                            | ✅           |
| Human Approval                      | ✅           |
| Thinking Mode                       | ✅ 可选        |
| Plan Mode                           | ✅ 可选        |
| AGENTS.md                           | ✅           |
| Skills                              | ✅           |
| 上下文压缩                               | ✅           |
| Tool Recovery                       | ✅           |
| 重复失败提醒                              | ✅           |
| Token 统计                            | ✅           |
| 调用耗时统计                              | ✅           |
| JSON Trace                          | ✅           |
| 自动化 Benchmark                       | ✅           |
| Session 跨进程持久化                      | ❌           |
| Session Resume CLI                  | ❌           |
| Streaming LLM Response              | ❌           |
| Git Checkpoint / Rollback           | ❌           |
| 全局最大 Turn / Cost Budget             | ❌           |
| 整个 Agent 进程 Sandbox                 | ❌           |
| TUI / 交互式终端 REPL                    | ❌           |

---

# 架构

当前核心执行链路：

```text
                    User
                     │
             ┌───────┴───────┐
             │               │
          Feishu           Demo CLI
             │               │
             └───────┬───────┘
                     ▼
                  Session
                     │
                     ▼
              PromptComposer
             ┌───────┼────────┐
             │       │        │
         AGENTS.md  Skills   Plan
                     │
                     ▼
                 Compactor
                     │
                     ▼
                 Provider
                     │
               AgentEngine
                     │
              ┌──────┴──────┐
              │             │
          Response       Tool Calls
                            │
                            ▼
                        Registry
                            │
          ┌─────────┬───────┼─────────┬──────────┐
          │         │       │         │          │
        read      write    edit      bash    subagent
                                     │          │
                                     ▼          ▼
                                  Executor   Explorer
                              ┌──────┴──────┐
                              │             │
                           Docker         Local
```

项目将 Agent 推理、工具定义和实际命令执行解耦。

其中 Bash 不再直接调用宿主机 `os/exec`，而是统一经过：

```text
BashTool
   │
   ▼
Executor
 ├── DockerExecutor
 └── LocalExecutor
```

这为未来增加 Native Sandbox、VM、SSH 或 Remote Executor 保留了扩展空间。

---

# 目录结构

```text
go-tiny-claw/
├── cmd/
│   ├── claw/
│   │   ├── main.go
│   │   └── ...
│   └── bench/
│       └── main.go
│
├── docs/
│   └── security.md
│
├── internal/
│   ├── context/
│   │   ├── session.go
│   │   ├── composer.go
│   │   ├── compactor.go
│   │   ├── recovery.go
│   │   └── skill.go
│   │
│   ├── engine/
│   │   ├── loop.go
│   │   ├── reminder.go
│   │   └── reporter.go
│   │
│   ├── executor/
│   │   ├── executor.go
│   │   ├── local.go
│   │   ├── docker.go
│   │   ├── policy.go
│   │   └── config.go
│   │
│   ├── feishu/
│   │   ├── bot.go
│   │   └── approval.go
│   │
│   ├── eval/
│   │   └── benchmark.go
│   │
│   ├── observability/
│   │   ├── trace.go
│   │   └── tracker.go
│   │
│   ├── provider/
│   ├── schema/
│   └── tools/
│
├── Dockerfile.sandbox
├── go.mod
├── go.sum
└── README.md
```

---

# 环境要求

当前项目主要面向 macOS / Linux 环境。

需要：

- Go 1.26.2
- Bash
- 智谱 API Key
- Docker / Docker Desktop，推荐
- 飞书应用凭据，使用 Feishu 模式时需要

检查 Go：

```bash
go version
```

检查 Docker：

```bash
docker version
```

> LocalExecutor 可以在没有 Docker 的情况下运行，但它不提供操作系统级安全隔离。

---

# 快速开始

## 1. 获取代码

```bash
git clone https://github.com/ketitongxue/go-tiny-claw.git
cd go-tiny-claw

go mod download
```

---

## 2. 配置模型

当前默认 Provider 使用智谱 OpenAI-compatible Coding API：

```text
https://open.bigmodel.cn/api/coding/paas/v4
```

配置 API Key：

```bash
export ZHIPU_API_KEY="your-api-key"
```

当前 `claw` 默认模型：

```text
glm-5.3
```

也可以启动时指定：

```bash
-model glm-5.3
```

---

# Docker Sandbox

推荐首先构建 Sandbox 镜像：

```bash
docker build \
  -f Dockerfile.sandbox \
  -t go-tiny-claw-sandbox:latest \
  .
```

当前基础镜像包含：

- Go
- Bash
- Git
- curl
- ca-certificates
- ripgrep

默认 Sandbox 镜像名称：

```text
go-tiny-claw-sandbox:latest
```

也可以指定：

```bash
-sandbox-image my-claw-sandbox:latest
```

---

# 运行 Demo

当前程序默认运行模式是：

```text
feishu
```

因此如果只是本地体验，请显式指定：

```bash
go run ./cmd/claw \
  -mode demo
```

默认 Demo 会要求 Agent 并发完成：

1. 执行 Bash 命令
2. 创建 `trace_test.md`

默认工作区：

```text
./workspace
```

程序会在不存在时自动创建。

完成后可以查看：

```bash
cat workspace/trace_test.md

ls -la workspace/.claw/traces
```

---

## 自定义 Demo Prompt

```bash
go run ./cmd/claw \
  -mode demo \
  -prompt "阅读当前项目代码，并告诉我主要模块的职责"
```

指定工作区：

```bash
go run ./cmd/claw \
  -mode demo \
  -workdir ./my-project \
  -prompt "分析这个项目并运行测试"
```

---

# Sandbox 模式

支持：

```text
auto
docker
local
```

例如：

```bash
go run ./cmd/claw \
  -mode demo \
  -sandbox=docker
```

---

## auto

默认：

```text
-sandbox=auto
```

当前 `auto` 是安全优先模式：

```text
auto
 │
 ├── Docker 可用
 │      ↓
 │ DockerExecutor
 │
 └── Docker 不可用
        ↓
      启动失败
```

**不会自动回退到 LocalExecutor。**

如果 Docker CLI、Daemon 或 Sandbox 镜像不可用，程序会拒绝启动。

---

## docker

强制使用：

```bash
-sandbox=docker
```

同样要求：

- Docker CLI 可用
- Docker Daemon 可用
- Sandbox 镜像已经存在

程序不会自动执行 `docker pull`。

---

## local

显式使用：

```bash
-sandbox=local
```

该模式直接在宿主机执行 Bash。

启动时会打印安全警告。

LocalExecutor：

```text
❌ 不是 Sandbox
❌ workdir 不是安全边界
❌ 无法阻止访问工作区外文件
❌ 无法阻止网络访问
❌ 无法阻止修改宿主其他文件
```

它拥有启动 `claw` 的当前操作系统用户权限。

因此：

> **只应在明确可信的本地开发环境使用 LocalExecutor。**

特别不建议：

```text
Feishu + LocalExecutor
```

用于无人值守远程执行。

---

# Docker Sandbox 安全策略

DockerExecutor 每次 Bash 调用都会创建一个一次性容器。

当前主要限制包括：

```text
--network none
--read-only
--cap-drop ALL
--security-opt no-new-privileges=true
--memory
--memory-swap
--cpus
--pids-limit
--tmpfs /tmp
```

只挂载当前工作区：

```text
Host WorkDir
     │
     ▼
/workspace
```

不会自动挂载：

- `$HOME`
- `~/.ssh`
- `~/.aws`
- `~/.config`
- Docker Socket
- 其他宿主目录

---

## CodingPolicy

主 Agent：

```text
Workspace: RW
Network:   OFF
Memory:    1024 MB
CPU:       2
PIDs:      128
Timeout:   30s
```

---

## ExplorerPolicy

Explorer Subagent：

```text
Workspace: RO
Network:   OFF
Memory:    512 MB
CPU:       1
PIDs:      64
Timeout:   30s
```

因此 Explorer 即使执行：

```bash
rm file.go
```

或者：

```bash
echo hacked > main.go
```

也会由操作系统级只读挂载拒绝。

它的只读能力不依赖 Prompt 自觉。

---

# Main Agent Tools

主 Agent 默认注册：

```text
read_file
write_file
edit_file
bash
spawn_subagent
```

文件工具要求使用工作区内相对路径。

路径解析会检查：

- `..` 路径逃逸
- 绝对路径
- Symlink Escape
- 写入不存在文件时的父目录逃逸

---

# Explorer Subagent

主 Agent 可以通过：

```text
spawn_subagent
```

委派代码探索任务。

适合：

- 跨文件搜索
- 阅读大型代码库
- 查找函数调用链
- 定位实现
- 收集上下文后返回摘要

Docker Sandbox 下 Explorer 获得：

```text
read_file
bash
```

但 Bash 工作区是只读挂载。

LocalExecutor 无法强制 Bash 只读，因此：

```text
-sandbox=local
```

时 Explorer 不注册 Bash，只保留：

```text
read_file
```

Explorer 单次任务最多执行：

```text
10 Turns
```

防止子智能体无限探索。

---

# 并发 Tool Calling

当模型在同一轮返回多个 Tool Calls 时，AgentEngine 使用 goroutine 并发执行。

例如模型可以同时：

```text
├── read_file
├── bash
└── spawn_subagent
```

完成后统一将 Observation 写回 Session，再进入下一轮推理。

---

# Feishu 模式

Feishu 是当前默认入口。

需要：

```bash
export ZHIPU_API_KEY="..."

export FEISHU_APP_ID="..."

export FEISHU_APP_SECRET="..."
```

启动：

```bash
go run ./cmd/claw
```

等价于：

```bash
go run ./cmd/claw \
  -mode feishu \
  -sandbox auto
```

启动后程序通过飞书长连接等待消息。

---

## 多会话

每个飞书 Chat 都拥有独立：

```text
Session
AgentEngine
Reporter
CostTracker
Conversation Queue
```

结构：

```text
Chat A
 └── Session A
     └── AgentEngine A

Chat B
 └── Session B
     └── AgentEngine B
```

不同 Chat 可以独立工作。

同一个 Chat 内的任务按照队列顺序执行，避免上下文竞争。

当前每个 Chat 的等待队列容量：

```text
64
```

---

## 群聊行为

群聊中只有明确 `@机器人` 的普通任务才会触发 Agent。

其他群聊消息默认忽略。

私聊可以直接发送任务。

---

# Human Approval

Feishu 模式包含 Human Approval Layer。

部分高风险 Bash 操作会暂停执行并发送审批请求：

```text
approve <taskID>
```

批准。

```text
reject <taskID>
```

拒绝。

审批请求：

- 与 Chat ID 绑定
- 不允许其他聊天批准
- 默认最长等待 5 分钟
- 超时自动拒绝
- Cancel 后自动拒绝
- 审批消息绕过普通会话队列

---

## Approval 不是 Sandbox

当前危险命令识别采用启发式规则。

它只能作为：

```text
Human Approval Layer
```

而不是：

```text
Security Boundary
```

最终权限仍由：

```text
SandboxPolicy
```

强制控制。

即使人工批准一个命令，它也不能绕过：

- Explorer ReadOnly
- Network None
- Resource Limit
- RootFS ReadOnly
- Linux Capability 限制

---

# AGENTS.md

可以在 WorkDir 根目录增加：

```text
AGENTS.md
```

例如：

```markdown
# Project Rules

- 修改代码前先阅读现有实现
- 优先进行最小修改
- 所有 Go 修改完成后运行 gofmt
- 完成任务后运行 go test ./...
- 不要修改与当前任务无关的文件
```

AgentEngine 每次 `Run` 开始时都会重新构建 System Prompt，并加载该文件。

---

# Skills

Skills 放在：

```text
.claw/skills/
```

例如：

```text
workspace/
├── AGENTS.md
└── .claw/
    └── skills/
        └── go-test/
            └── SKILL.md
```

示例：

```markdown
---
name: go-test
description: 编写 Go 单元测试
---

读取目标函数和现有测试。

优先使用表驱动测试。

完成后执行：

go test ./...
```

SkillLoader 会递归查找：

```text
.claw/skills/**/SKILL.md
```

并注入当前 Agent Run 的 System Prompt。

当前 Skills 是提示词扩展机制，不是独立执行插件系统。

---

# Thinking Mode

开启：

```bash
-thinking
```

例如：

```bash
go run ./cmd/claw \
  -mode demo \
  -thinking \
  -prompt "分析当前项目"
```

开启后，每个 Turn 在 Action 前增加一次不携带 Tools 的模型调用：

```text
Context
   │
   ▼
Thinking
   │
   ▼
Action + Tools
```

注意：

> Thinking Mode 会增加 API 调用次数和 Token 消耗。

它是 Harness 自己实现的两阶段推理流程，不等于模型服务商的原生 Reasoning 参数。

---

# Plan Mode

开启：

```bash
-plan
```

例如：

```bash
go run ./cmd/claw \
  -mode demo \
  -plan \
  -prompt "完成这个项目的重构任务"
```

Plan Mode 会要求 Agent 使用：

```text
PLAN.md
TODO.md
```

外部化长任务状态。

典型流程：

```text
检查 PLAN.md / TODO.md
       │
       ├── 不存在
       │     ├── 创建 PLAN.md
       │     └── 创建 TODO.md
       │
       └── 已存在
             └── 从第一个未完成任务继续
```

每完成一个步骤需要更新：

```markdown
- [ ] TODO
```

为：

```markdown
- [x] DONE
```

这可以为长程任务提供基础的文件级状态续接。

但需要注意：

> 当前 Session 本身仍然只保存在内存中，Plan Mode 不等于完整的 Session Persistence。

---

# Session 与上下文

Session 当前记录：

- Session ID
- WorkDir
- 创建时间
- 更新时间
- Message History
- Prompt Tokens
- Completion Tokens
- Cost

Session History 当前仅存在内存。

进程退出后不会自动恢复：

```text
.claw/sessions/*.jsonl
```

这类持久化目前尚未实现。

---

# Working Memory

Agent 不会每轮无条件把完整 Session 全部发送给模型。

当前会从最近历史构建 Working Memory，并避免把 Tool Call 链从中间截断。

同时使用 Compactor 对较旧或过长内容进行压缩和截断。

这主要用于限制：

- 大型 Tool Output
- 历史日志
- 长上下文增长

当前 Compactor 是规则式处理，并非真正的 Token-aware Memory System。

---

# Recovery

当 Tool 执行失败时：

```text
Tool Error
    │
    ▼
RecoveryManager
    │
    ▼
Diagnostic Hint
    │
    ▼
Observation
    │
    ▼
下一轮 LLM
```

例如 Bash：

```text
exit code != 0
```

会被保留为真实失败：

```text
ToolResult.IsError = true
```

同时 stdout / stderr 会继续返回给模型。

这样模型可以根据实际错误继续自我修复，而不是把失败命令误认为执行成功。

---

# Bash 输出

BashTool 分别保留：

```text
STDOUT
STDERR
ExitCode
Duration
```

发送给模型的输出最大约：

```text
8000 bytes
```

过长内容会截断。

需要注意：

> Executor 当前仍会在主进程内保存完整 stdout/stderr，因此模型侧 8 KB 截断不代表宿主内存也受到同样限制。

---

# Executor

核心接口：

```go
type Executor interface {
    Execute(
        ctx context.Context,
        req Request,
        onOutput OutputHandler,
    ) (Result, error)
}
```

Request 包含：

```text
Command
WorkDir
Env
Timeout
```

Result 包含：

```text
Stdout
Stderr
ExitCode
Duration
```

普通：

```text
exit code != 0
```

属于“命令执行失败”。

Docker Daemon 不可用、工作目录异常、Context Cancel 等属于：

```text
Infrastructure Error
```

两类错误不会再混在一起。

接口同时预留：

```text
OutputHandler
```

供未来 Streaming 使用。

---

# DockerExecutor 生命周期

当前 DockerExecutor：

```text
每一次 Bash Tool Call
        │
        ▼
docker run --rm
        │
        ▼
Disposable Container
        │
        ▼
Command Complete
        │
        ▼
Container Remove
```

目前还不是：

```text
一个 Session 一个长生命周期 Container
```

这种 Session Sandbox 可以在后续增加，而不需要修改 AgentEngine。

---

# 环境变量

DockerExecutor 不会把：

```go
os.Environ()
```

整体传入容器。

默认 Policy 只允许显式传入：

```text
PATH
LANG
LC_ALL
TERM
```

例如下面这些不会自动暴露：

```text
ZHIPU_API_KEY
FEISHU_APP_SECRET
GITHUB_TOKEN
AWS_*
SSH_*
```

模型 Provider 的 API Key 保留在 go-tiny-claw 主进程。

---

# Sandbox 网络

当前默认：

```text
Network = OFF
```

Docker 使用：

```text
--network none
```

因此 Sandbox 内：

```bash
curl https://example.com
```

默认无法连接。

这也意味着需要联网下载依赖的命令可能失败，例如首次下载：

```text
Go Modules
npm packages
pip packages
```

如果任务需要额外依赖，推荐构建经过审查的自定义 Sandbox 镜像，将依赖预先放入镜像。

当前 CLI 暂未开放网络开关。

---

# Trace

每次 Agent Run 会生成 JSON Trace：

```text
<workdir>/.claw/traces/
```

文件类似：

```text
trace_<session-id>_<random>.json
```

Trace 当前包括：

```text
Agent.Run
└── Turn
    ├── LLM.Thinking
    └── LLM.Action
```

以及部分运行属性。

用于：

- Agent 行为回放
- 调用耗时分析
- Context 调试
- Tool 链路分析

---

# Token 与成本统计

CostTracker 会记录：

- Prompt Tokens（包含缓存命中）
- Completion Tokens
- Cached Prompt Tokens（缓存命中）
- API Latency

输入、输出 Token 总量与人民币估算费用累加到 Session，缓存命中数量显示在单次调用日志中。

`internal/observability/tracker.go` 中的价格表按官方价格截图配置，单位为**元/百万 Tokens（CNY）**：

| 模型 | 输入单价 | 输出单价 | 缓存命中单价 | 缓存存储 |
| --- | ---: | ---: | ---: | --- |
| GLM-5.3 | 8 | 28 | 2 | 限时免费 |
| GLM-5.3-Flash | 0.8 | 2.8 | 0.23 | 限时免费 |
| GLM-5.3-FlashX | 2 | 7 | 0.57 | 限时免费 |

默认 `claw` 使用 `glm-5.3`，费用计算为：

```text
费用（元）= ((输入总量 - 缓存命中量) × 输入单价
           + 缓存命中量 × 缓存命中单价
           + 输出量 × 输出单价) / 1,000,000
```

OpenAI-compatible Provider 从 `usage.prompt_tokens_details.cached_tokens` 提取缓存命中数量；未返回该字段时按无缓存命中估算。缓存存储当前不计费，限时免费结束后需重新更新配置与计费逻辑。

未配置价格的模型（包括截图未列出的 `glm-5.2`）仍统计 Token，但会输出警告，费用为 0 **不代表免费**，不再使用旧的假定单价。

以上是按量 API 价格估算，不代表 Coding Plan 套餐的实际扣费，也不应作为正式账单依据。

---

# Benchmark

项目包含独立自动化评测入口：

```bash
go run ./cmd/bench
```

默认同样使用：

```text
-sandbox=auto
```

需要提前构建 Docker Sandbox。

Benchmark 当前通过：

```text
Setup Script
     │
     ▼
Agent Task
     │
     ▼
Validation Script
     │
     ▼
PASS / FAIL
```

验证 Agent 是否真正完成任务，而不是仅仅声称：

> “任务已经完成。”

当前内置测试包括：

- edit_file 修改准确性
- 阅读 Go 代码并生成单元测试

Benchmark 当前模型单独配置为：

```text
glm-5.2
```

---

# 配置检查

可以执行：

```bash
go run ./cmd/claw \
  -check-config
```

它只检查本地启动配置，例如：

- mode
- model
- workspace
- sandbox 参数
- 必需环境变量

不会：

- 调用模型
- 连接飞书
- 验证 API Key 是否有效
- 验证 Docker Daemon
- 验证 Sandbox 镜像
- 发送网络请求

因此它只是静态配置检查，不是完整健康检查。

---

# CLI 参数

```text
-mode
    feishu | demo
    默认：feishu

-workdir
    Agent 工作区
    默认：workspace

-model
    智谱模型
    默认：glm-5.3

-prompt
    demo 模式执行的任务

-sandbox
    auto | docker | local
    默认：auto

-sandbox-image
    Docker Sandbox 镜像
    默认：go-tiny-claw-sandbox:latest

-thinking
    开启 Thinking 阶段

-plan
    开启 PLAN.md / TODO.md 模式

-check-config
    仅检查本地配置
```

---

# 测试

运行全部测试：

```bash
go test ./...
```

静态检查：

```bash
go vet ./...
```

Docker Executor Integration Test：

```bash
go test \
  -count=1 \
  -v \
  ./internal/executor \
  -run DockerIntegration
```

Agent + Docker 集成：

```bash
go test \
  -count=1 \
  -v \
  ./cmd/claw \
  -run DockerSessionFactoryIntegration
```

如果 Docker Daemon 或测试镜像不存在，部分 Docker Integration Test 会 Skip。

---

# 安全边界

当前 Sandbox 主要保护：

- Bash Tool 对未挂载宿主文件的访问
- RootFS 修改
- 默认网络访问
- Linux Capability
- PID 数量
- CPU
- Memory
- 单命令执行时间
- Explorer 对 Workspace 的写入

但它不能保护一切。

---

## 当前不能保护

### 主 Agent 的 RW Workspace

CodingPolicy 可以修改整个工作区。

Agent 仍然可以：

```text
删除代码
覆盖文件
产生错误修改
```

因此建议对重要项目使用：

```text
Git
Backup
Test Copy
```

---

### 整个 go-tiny-claw 主进程

当前只有 Bash 通过 Executor Sandbox。

下面这些组件仍运行在 Host：

```text
AgentEngine
Provider
read_file
write_file
edit_file
Session
Trace
Feishu
```

文件工具自身有工作区路径和 Symlink Escape 检查，但这不是完整的 OS Sandbox。

---

### Docker / Kernel 漏洞

Container 和 VM 不是同一种安全边界。

Docker Sandbox 不能承诺抵御：

```text
Container Escape
Kernel Vulnerability
Docker Runtime Vulnerability
```

---

### 模型侧数据外传

如果 Agent 使用：

```text
read_file
```

读取了文件内容，该内容可能进入模型上下文。

`--network none` 只限制 Sandbox 内程序的网络，不会关闭 go-tiny-claw 主进程与模型 Provider 的网络连接。

因此不要将敏感数据放入 Agent 工作区。

完整 Threat Model：

```text
docs/security.md
```

---

# 当前限制

目前仍有一些明确未完成的能力。

## Session Persistence

Session 仅驻留内存。

尚未实现：

```text
JSONL Session Store
Session List
Resume
Fork
```

---

## Streaming

Executor 已经预留 OutputHandler，但当前完整链路尚未提供真正的实时 Streaming 体验。

---

## Git Safety

尚未实现：

```text
Git Checkpoint
Diff Review
Automatic Rollback
Task Commit
```

因此 Sandbox 保护 Host，并不等于保护 Workspace 本身。

---

## Verifier

Benchmark 已经拥有验证脚本，但日常 Agent Run 尚未形成通用：

```text
Agent
 ↓
Modify
 ↓
Test
 ↓
Verifier
 ↓
PASS
```

闭环。

---

## Global Budget

当前主 Agent Loop 没有统一的：

```text
Max Turns
Max Tokens
Max Cost
Task Deadline
```

因此不要把单命令的 30 秒 Timeout 理解为整个 Agent Task 的全局预算。

---

# Roadmap

建议后续演进方向：

```text
1. Session JSONL Persistence + Resume
2. Git Checkpoint / Diff / Rollback
3. Task Verifier
4. Streaming / Cancel / Steering
5. Session-scoped Sandbox Container
6. Extension / Executor 插件机制
7. Multi-Agent Orchestration
8. RPC / Server / Durable Runtime
```

Sandbox Executor 已经为：

```text
NativeSandboxExecutor
VMExecutor
SSHExecutor
RemoteExecutor
```

等后端留下扩展空间。

---

# 当前项目定位

`go-tiny-claw` 目前已经从：

```text
LLM + Tool Calling Demo
```

演进为：

```text
Coding Agent Harness
├── Agent Loop
├── Context
├── Tools
├── Executor
├── Sandbox
├── Subagent
├── Feishu
├── Approval
├── Observability
└── Evaluation
```

它最重要的目标不是堆积功能，而是逐步探索：

> **如何构建一个可控、可观测、可恢复、可验证，并且具备真实执行安全边界的软件工程 Agent。**

---

## License

当前仓库尚未提供明确的 License 文件。

在添加开源 License 前，请不要默认仓库代码拥有 MIT、Apache-2.0 等开源许可。
