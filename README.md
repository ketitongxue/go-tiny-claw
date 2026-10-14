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
| OpenAI-compatible Provider          | ✅ 配置扩展    |
| OpenAI Responses / API Key          | ✅           |
| ChatGPT OAuth / Dynamic Registration / PKCE / Refresh | ✅ 默认动态注册 |
| 机器级默认模型 / Model Registry          | ✅           |
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
| Token-aware Context                 | ✅           |
| Old Char Compactor                  | ❌ removed   |
| Feishu Auto Resume                  | ✅           |
| Tool Recovery                       | ✅           |
| 重复失败提醒                              | ✅           |
| Token 统计                            | ✅           |
| 调用耗时统计                              | ✅           |
| JSON Trace                          | ✅           |
| 自动化 Benchmark                       | ✅           |
| Session 跨进程持久化                      | ✅           |
| Session Resume CLI                  | ✅           |
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
              ContextBuilder
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
│   │   ├── service.go
│   │   ├── builder.go / policy.go / estimator.go
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
│   ├── app/                      # Provider/Auth/Config 装配入口
│   ├── auth/                     # Credential Store / PKCE / Refresh
│   ├── config/                   # Global Agent Home / config / custom models
│   ├── fileutil/                 # 原子 JSON 写入
│   ├── provider/
│   │   ├── interface.go
│   │   ├── manager.go
│   │   ├── registry.go
│   │   ├── model_registry.go
│   │   ├── openai/               # Responses
│   │   ├── zai/                  # Coding Plan China
│   │   └── compatible/           # Generic Chat Completions
│   ├── schema/
│   └── tools/
│
├── .gitignore
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
- 所选 Provider 的凭据（ZAI/OpenAI API Key，或 ChatGPT OAuth + Plan Usage 授权；本地 none Provider 不需要）
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
mkdir -p ~/Documents
cd ~/Documents
git clone https://github.com/ketitongxue/go-tiny-claw.git
cd go-tiny-claw

go mod download
```

### Git 更新与推送

建议将项目放在 `~/Documents/go-tiny-claw`（macOS 文稿目录）下，项目根目录是独立 Git 仓库，`main` 分支跟踪 `origin/main`。

开始修改前同步远端：

```bash
cd ~/Documents/go-tiny-claw
git pull --ff-only
```

修改完成后提交并推送：

```bash
git status
go test ./...
git add .
git commit -m "说明本次修改"
git push
```

远端地址为 `https://github.com/ketitongxue/go-tiny-claw.git`，可通过 `git remote -v` 查看。HTTPS 推送需要仓库写入权限和有效认证；使用 GitHub CLI 时，可通过 `gh auth login` 登录，使用 `gh auth setup-git` 配置 Git 认证，不要把 Token 或 API Key 写入代码。

`.gitignore` 排除了本地二进制、构建/测试产物、`.DS_Store`、`.env`、`auth.json`、`workspace/` 和 `.claw/` 配置/运行数据；示例环境配置和 `.claw/skills/` 仍可提交。全局 `~/.claw/` 本来就在仓库外，不要将其复制进项目。

---

## 2. Provider、Auth 与 Model

所有模型统一使用 **`provider/model`**。Provider、Credential、Model 和机器级默认选择互相独立：

```text
CLI → ModelRef → ProviderManager → LLMProvider → AgentEngine
                     ├ ProviderRegistry (definition + factory)
                     ├ ModelRegistry (API-key / account-bound OAuth / custom catalog)
                     └ AuthManager (store → environment → refresh)
```

内置 Provider：`openai`（Responses API，API Key / OAuth）和 `zai-coding-cn`（智谱 Coding Plan China，Chat Completions）。其他 OpenAI-compatible 服务通过配置扩展，不需要改 Go 代码。

### Global Agent Home 与 Workspace

```text
~/.claw/                         # 全局配置，不是 Agent 工作区
├── config.json                  # 普通配置：default_model
├── auth.json                    # API Key / OAuth / ID Token，0600，明文凭据
├── host.json                    # 稳定 OpenAI installation Host ID，非凭据
├── openai-models.json            # 绑定 OAuth 账号/注册的模型缓存，无 token
└── models.json                  # 自定义 Provider / 模型目录

<workspace>/.claw/               # 工作区资料，可被 Agent 访问
├── skills/
└── traces/
```

可用 `CLAW_HOME=/absolute/path` 显式指定全局目录（测试使用临时目录）。全局目录与工作区不能互相包含，启动时会拒绝这种配置；不要把凭据放进工作区或 Sandbox。详见 [Security](docs/security.md)。

### ZAI Coding Plan China

```bash
export ZAI_CODING_CN_API_KEY="..."
claw auth login zai-coding-cn --from-env   # 可选：保存到 AuthStore
claw model set zai-coding-cn/glm-5.3
claw -mode demo -model zai-coding-cn/glm-5.3
```

Endpoint 为 `https://open.bigmodel.cn/api/coding/paas/v4`，由 `internal/provider/zai/` 管理。

解析优先级：AuthStore → `ZAI_CODING_CN_API_KEY` → `ZHIPU_API_KEY`。`ZHIPU_API_KEY` 仅为 **legacy compatibility**；旧用户未创建配置时仍默认使用 `zai-coding-cn/glm-5.3`。临时兼容 `-model glm-5.3`，但会打印弃用警告。

### OpenAI API Key

```bash
export OPENAI_API_KEY="..."
claw auth login openai --method api-key --from-env
# 也可在终端隐藏输入：claw auth login openai --method api-key
claw models
claw model set openai/gpt-5.2
```

API Key 使用 Platform Responses API，计费模式为 `api`。飞书模式只额外要求飞书环境变量，不再强制要求智谱 Key。

### OpenAI / Sign in with ChatGPT

本地开源客户端默认走 **Dynamic Client Registration**，不需要手动配置 client ID，也不借用 Codex 或其他 CLI 的凭据。若尚未安装命令，可运行 `go install ./cmd/claw` 并将 Go bin 目录加入 PATH，或用 `go run ./cmd/claw` 替代 `claw`。

```bash
claw auth login openai                   # 默认 Sign in with ChatGPT
# 在浏览器登录 ChatGPT，选择账号/Workspace，授权 ChatGPT Plan Usage
claw models                             # 在线刷新此账号实际授权的模型目录
claw model set openai/<actual-model>     # 用列表中的实际 slug，不要照抄占位符
claw -mode demo -sandbox=docker -prompt "读取当前工作区文件，并总结目录结构"
```

Docker 镜像需要预先构建（见下文）。也支持显式 `claw auth login openai --method oauth`。

首次请求 `https://auth.openai.com/api/accounts/authorize`，使用 `dynamic_agent_client`、固定应用名 `go-tiny-claw`、全局持久 Host ID、PKCE(S256)、独立 state/nonce，以及 `resource.invoke` / `chatgpt.tokens.use.direct` 等 scopes。callback 绑定 `127.0.0.1` 的动态端口和 `/auth/callback`；验证 state 后必须收到 issued client ID。Token exchange 使用 issued ID 和完全相同的 redirect URI，不发送 client secret。ID Token 经 OpenAI JWKS 验证签名、issuer、issued-client audience、过期时间和 nonce 后才保存身份。

重复登录复用已保存的 issued client ID、Host ID 和 ID Token hint；callback 换 client ID 或 ID Token 换 subject 都会拒绝覆盖。要主动切换账号，请先 `claw auth logout openai`（保留 Host ID）。`OPENAI_OAUTH_CLIENT_ID` 仅作为可选的预注册 **static-client override**，不是默认登录的前置条件；已有注册优先于 override。

若未授予 `chatgpt.tokens.use.direct`，仍保存有效账号身份，但显示 **ChatGPT Plan Usage: disabled**，不发现模型、不进行订阅推理。重新登录授权，或显式改用 `--method api-key`；不会自动 fallback 到环境中的 API Key。

OAuth 模型来自 Bearer token 认证的 `GET https://api.openai.com/v1/models`，只保留 `visibility=list`，以 `slug` 为 ID、`display_name` 为展示名；不会套用 API Key 静态目录。登录和 `claw models` 刷新全局账号绑定缓存，`model set`、启动和 `-check-config` 离线读取缓存。账号权限变化后请重新 `claw models`；缓存不能保证实时配额。

订阅推理使用 `POST https://api.openai.com/v1/responses`，明确发送 `store=false`、`stream=true`，不使用 ChatGPT backend-api、`previous_response_id` 或 system input message。System Prompt 映射到 `instructions`，Working Memory 完整映射为 input/tool history。Provider 内部消费 SSE、聚合文本/工具调用，只有 `response.completed` 才成功；失败、未完成、断流或取消均报错。订阅限额/不可用错误明确提示，不自动花费 API Key 额度。当前不提供终端/飞书逐 token UI。

兼容真实订阅接口省略 `Content-Type` 的 SSE：按有界正文格式检测，而非仅依赖响应头。若 `response.completed` 仅返回 `output: []` 摘要，则使用此前 `response.output_item.done` 的已完成文本/工具调用；不把未完成增量当成最终输出。非空终态输出优先，不重复拼接。

SSH/headless 或浏览器打开失败时：

```bash
export OPENAI_OAUTH_REDIRECT_URI="http://127.0.0.1:1455/auth/callback"
claw auth login openai --method oauth --manual
```

在私密交互中打开显示的授权地址，完成后粘贴**完整 final redirect URL**（即使浏览器无法连接该地址），不能只粘贴 code。手动模式移除 token/login hints，自动模式从不打印授权 URL。不要将授权 URL、callback URL 或凭据粘贴进日志/issue。

每次模型请求前检查 token，按 Provider 串行刷新，尊重 `earliest_refresh_at`，原子保存 access/refresh token rotation、过期信息和 scopes。此锁仅覆盖同一进程；避免多个 claw 进程同时刷新同一账号。旧的仅含 AccountID/token 的 OAuth 记录可读取，但必须 logout 后重新完成验证注册。

**真实服务状态说明：** mock OAuth/JWKS/模型/SSE 和 Docker 工具链有自动测试；真实浏览器授权、账号/Workspace 权限及订阅配额需要人工验收，不能仅凭 mock 测试保证 OpenAI 对每个账号开放此流程。

### 机器级默认模型

```bash
claw auth login                         # 查看可登录 Provider
claw auth logout openai                 # 仅删除 AuthStore；环境变量仍可能生效
claw models                             # OAuth 在线账号目录；其他 Provider 使用现有静态目录
claw model current
claw model set zai-coding-cn/glm-5.3
```

启动选择优先级：`-model provider/model` → `config.json.default_model` → `zai-coding-cn/glm-5.3`。默认模型缺少凭据时明确报错，**不静默切换 Provider**。`model set` 在保存前离线校验模型、Provider、凭据和 ToolCalling；不可调用工具的模型不能作为主 Agent。

```json
{"default_model": "zai-coding-cn/glm-5.3"}
```

OpenAI OAuth 列表在线发现；OpenAI API Key、ZAI 和自定义 Provider 保持内置/配置目录。实际调用和配额仍由服务端决定。本阶段所有会话使用同一启动模型，没有飞书 `/model` 或 Session 级切换。

### Custom OpenAI-compatible Providers

编辑 `~/.claw/models.json`：

```json
{
  "providers": {
    "ollama": {
      "name": "Local Ollama",
      "api": "openai-compatible",
      "base_url": "http://localhost:11434/v1",
      "auth": {"type": "none"},
      "models": [
        {"id": "qwen3-coder", "tool_calling": true, "reasoning": false, "billing": "unknown"}
      ]
    },
    "company-ai": {
      "name": "Company AI Gateway",
      "api": "openai-compatible",
      "base_url": "https://ai.example.com/v1",
      "auth": {"type": "api_key", "env": "COMPANY_AI_API_KEY"},
      "models": [{"id": "coder-large", "tool_calling": true}]
    }
  }
}
```

```bash
claw model set ollama/qwen3-coder
# 企业网关：export COMPANY_AI_API_KEY="..."
# 可选持久化：claw auth login company-ai --from-env
```

仅支持 `none` / `api_key` + env；AuthStore 中的 Key 优先于 env。不支持 literal key、custom OAuth 或 command credentials。Provider ID 必须匹配 `[a-z0-9][a-z0-9_-]*`；重复 ID/模型、覆盖内置 Provider、非法 env 名与非 HTTP(S) BaseURL 会报错。模型 ID 可包含 `/`，按第一个 `/` 拆分模型引用。`billing` 可为 `api`、`subscription`、`unknown`，省略即 unknown。修改后重启加载，不支持热加载。

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
# 配置所选 Provider 的凭据；此处以 ZAI 为例，也可改用 OpenAI 或自定义 Provider
export ZAI_CODING_CN_API_KEY="..."

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

> Session 现在独立持久化；Plan Mode 仍是文件级任务续接，不替代完整对话历史。

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

完整历史与累计 Usage 保存在全局 `~/.claw/sessions/<SHA256(SessionID)>.jsonl`，支持 `CLAW_HOME`。目录权限 0700，文件 0600，绝不默认放在 workspace 中。文件包含用户 Prompt、代码、Tool Output 和 Agent Response，属于 **Sensitive Local Data**，不要提交、公开或挂载给 Agent。

```bash
claw -mode demo -prompt "阅读当前项目并记住 package 数量"
claw sessions
claw session show <session-id>  # 仅 Metadata / Usage，不打印聊天正文
claw -mode demo -resume <session-id> -prompt "继续刚才的任务"
claw session delete <session-id> # 只删 Session，不删代码、WorkDir 或 Trace
```

新 Demo 使用随机 ID；飞书按 ChatID 自动恢复。Resume 使用保存的 provider/model，不随默认模型改变。WorkDir 不存在会报错；凭据由现有 AuthManager 重新解析/刷新，Session 不保存任何认证 Token。一个 Agent Home 只应由一个运行进程使用（暂不支持跨进程并发修改同一 Session）。

---

# Working Memory

Agent 不会每轮无条件把完整 Session 全部发送给模型。

ContextBuilder 按 `context window - reserved output - safety margin` 计算 Input Budget，并计入 Tool Schema。优先使用模型元数据；未知时集中 fallback 为 32768 window / 4096 reserved output / 10% margin。保守 heuristic 区分 ASCII、CJK 和 JSON，计算 Content、Tool Name、Arguments 与 Call ID。

只选择完整近期用户轮次；System 与当前 User 不截断，超预算明确报错。Tool Output 在请求视图内按 token 上限做 Unicode-safe Head + Marker + Tail，完整磁盘历史不变。单个当前轮次（包括完整工具链/临时 Thinking）仍超预算时明确失败，不生成非法输入。Thinking 仅用于当前 Action，不进入 Conversation 或 Trace 正文。

`~/.claw/config.json` 可以增加：

```json
{"context":{"default_context_window":32768,"default_reserved_output":4096,"safety_margin_percent":10,"max_tool_result_tokens":4096}}
```

正常裁剪只记录 Trace 的 `context.*` 指标，不打印内存告警。明确的 Provider context limit 最多以更保守窗口重建并重试一次。Main 与 Explorer 使用同一模型与策略；没有 Summary / Vector Memory。

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

工具完整输出进入 Session History；发送给模型的请求视图由 ContextBuilder 的 `max_tool_result_tokens` 限制，不再在工具内部按 8000 bytes 截断。

> Executor 与 Session 仍在主进程内保存完整 stdout/stderr；Token Budget 不等于宿主内存或磁盘 quota。

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
OPENAI_API_KEY
ZAI_CODING_CN_API_KEY
ZHIPU_API_KEY（legacy）
FEISHU_APP_SECRET
GITHUB_TOKEN
AWS_*
SSH_*
```

Provider 的 API Key / OAuth token 只由 go-tiny-claw 宿主主进程持有，不传入 Sandbox；全局 Credential Store 不挂载到工作区。Local 模式没有这种宿主文件隔离能力，不能用于不可信任务。

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

Root Span 还包含 `provider_id`、`model_id`、`billing_mode`、`auth_type`；不包含 Key、token 或 Authorization header。

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

Session 保存 ProviderID、ModelID，以及输入/输出 Token、BillingMode、CostKnown 和货币。CostTracker 与 Benchmark 共用 `ModelInfo` 和 `UsageBilling`，不再按裸模型名套用 GLM 价格。

- **api**：只有 `ModelInfo.Pricing` 存在且注明货币时才估算费用；未知价格显示 `Cost: unknown`。当前内置 OpenAI 模型未设置单价。
- **subscription**：OpenAI OAuth、ZAI Coding Plan 显示 `计费模式: Subscription` 和 Token 使用量，CostKnown=false，**不显示 ¥0.000000**。API Key 不一定意味着按 token 计费，Coding Plan endpoint 仍属于订阅。
- **unknown**：自定义 Provider 未声明计费模式时显示 `Cost: unknown`，不能解释为免费。

已知 API 价格的估算公式：`((输入总量 - 缓存命中) × 输入单价 + 缓存命中 × 缓存单价 + 输出量 × 输出单价) / 1,000,000`。Chat Completions 的 `prompt_tokens_details.cached_tokens` 与 Responses 的 `input_tokens_details.cached_tokens` 都会适配到内部 Usage；缓存命中包含在输入总量中，不能重复计费。

这些统计不是正式账单或订阅配额验证。Harness Thinking 仍是独立的 Thinking + Action 两次调用，与 ModelInfo.Reasoning 描述的原生能力不同。

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

Benchmark 与 `claw` 共用默认模型配置、凭据解析和计费；也可单次覆盖：

```bash
go run ./cmd/bench -model zai-coding-cn/glm-5.3
```

没有独立 Benchmark Pricing 表，也不会自动切换 Provider。

---

# 配置检查

可以执行：

```bash
go run ./cmd/claw \
  -check-config
```

它只检查本地启动配置，例如：

- mode
- Selected Model / Provider / ToolCalling
- Credential 类型与来源（不输出值）、OAuth 是否需要刷新/受 earliest_refresh_at 限制
- 选择 OpenAI 时：动态注册支持、Host ID 可用性（不显示 ID）、已有 OAuth 身份与 Plan Usage 状态
- Global config 路径 / workspace 分离
- sandbox 参数
- 飞书模式的必需环境变量

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
    provider/model
    默认：全局 config.json → zai-coding-cn/glm-5.3

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

OAuth/JWKS/模型发现/SSE 测试默认全为 mock，不打开浏览器。显式人工 OAuth 集成测试（使用临时 Agent Home，不覆盖正常凭据）：

```bash
CLAW_OPENAI_OAUTH_INTEGRATION=1 go test -count=1 -v ./cmd/claw -run '^TestOpenAIOAuthHumanIntegration$'
# 若也允许一次真实订阅推理（消耗配额），额外设置 CLAW_OPENAI_OAUTH_INFERENCE=1
```

离线 OAuth → 模型发现 → Responses Tool Calling → Docker → Trace 集成：

```bash
go test -count=1 -v ./cmd/claw -run '^TestOpenAIOAuthMockDockerAgentIntegration$'
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

## Session / Context 限制

JSONL Session Store、List、Resume 与飞书自动恢复已实现。尚不支持 Fork、跨进程并发写入、云同步、磁盘加密、Summary Memory。Heuristic Token Estimator 不是官方 tokenizer；单个完整当前轮次无法装入 Budget 时会报错，未完成工具链不会自动重跑。

---

## Streaming

OpenAI OAuth Provider 已消费并聚合 Responses SSE；Executor 已预留 OutputHandler。当前完整链路尚未提供终端/飞书逐 token 的实时 Streaming 体验。

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
1. Session JSONL Persistence + Resume（M5 已完成）
2. Git Checkpoint / Diff / Rollback（等待人工验收，不自动开始）
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
