# go-tiny-claw

> 一个用 Go 编写的轻量级 Coding Agent Harness。

`go-tiny-claw` 用于探索 Coding Agent 的核心工程能力，包括模型调用、工具执行、Durable Session、Sandbox、Subagent、Messaging Gateway、Human Approval、Prompt 管理和可观测性。

当前定位：

> **面向学习、实验和二次开发的软件工程 Agent Harness 原型。**

它已经具备较完整的 Agent Runtime，但仍不是 Codex、Claude Code、Pi 等成熟 Coding Agent 的替代品，也不建议直接用于无人值守的生产代码执行。

---

## 核心能力

| 能力 | 状态 |
| --- | --- |
| ReAct Agent Loop | ✅ |
| OpenAI / ZAI / OpenAI-compatible Provider | ✅ |
| API Key / ChatGPT OAuth | ✅ |
| Tool Calling / 并发 Tool | ✅ |
| read / write / edit / bash | ✅ |
| Explorer Subagent | ✅ |
| Native（Anthropic SRT）/ Docker Sandbox | ✅（macOS 实机验证通过） |
| Main / Explorer 独立 Sandbox Policy | ✅ |
| Durable Session / Resume | ✅ |
| Token-aware Context | ✅ |
| Structured Prompt | ✅ |
| AGENTS / SYSTEM / Skills | ✅ |
| Project Trust | ✅ |
| Messaging Gateway | ✅ |
| 飞书长连接 / Telegram Long Polling（文本） | ✅ |
| 多通道同时在线 / 独立重连 | ✅ |
| 多会话并行 | ✅ |
| Human Approval | ✅ |
| quiet / compact / verbose 消息模式 | ✅ |
| Trace / Token / Cost 统计 | ✅ |
| Benchmark | ✅ |
| Git Checkpoint / Rollback | 🚧 |
| Streaming / Steering | 🚧 |

---

## 架构

```text
                         User
                          │
               ┌──────────┴──────────┐
               │                     │
          Messaging Gateway        Demo CLI
               │
     Feishu / Telegram Adapters
               │
               ▼
          Conversation
               │
         Durable Session
               │
               ▼
          PromptService
               │
          ContextBuilder
               │
               ▼
            Provider
               │
               ▼
          AgentEngine
               │
       ┌───────┴────────┐
       │                │
   Final Response    Tool Calls
                        │
                 Tool Registry
         ┌────────┬─────┼──────┬──────────┐
         │        │     │      │          │
       read     write  edit   bash    subagent
                              │          │
                           Executor   Explorer
                    ┌─────────┼─────────┐
                    │         │         │
                  Native    Docker    Local
                   SRT      strict    UNSAFE
```

一个 Conversation 对应一个独立 Session。同一 Conversation 串行执行，不同 Conversation 可以并行；不同 Channel / Account / Chat 的 Session 互相隔离。

---

## 环境要求

推荐环境：

- macOS / Linux
- Go 1.26.2
- Anthropic SRT（Native），或 Docker / Docker Desktop（严格资源后端）
- Git
- 对应模型 Provider 的凭据
- 按启用的通道准备飞书应用凭据 / Telegram Bot Token

检查：

```bash
go version
srt --version    # Native
# 或严格资源后端：
docker version
```

---

# 快速开始

## 1. 获取代码

```bash
git clone https://github.com/ketitongxue/go-tiny-claw.git
cd go-tiny-claw

go mod download
go install ./cmd/claw
```

---

## 2. 安装 / 构建 Sandbox

日常本地 Coding 默认 `-sandbox=auto`，按应用 Policy 和 backend capability 优先选择 OS-native Sandbox：

```bash
npm install -g @anthropic-ai/sandbox-runtime
srt --version
```

SRT 在 macOS 使用 Seatbelt / sandbox-exec，Linux 使用 bubblewrap（另需系统安装 bubblewrap、socat、ripgrep 等 SRT 依赖）。程序不自动安装，缺少依赖或无法验证隔离时不会运行 Native。Windows Native 未验证，本项目目前明确拒绝。

Docker 仍完整保留，用于必须强制 CPU / Memory / PID 限制的场景；也可在 Native 不可用、且策略兼容时由 auto 选择：

```bash
docker build \
  -f Dockerfile.sandbox \
  -t go-tiny-claw-sandbox:latest \
  .
```

两种安全后端均限制 Bash 的文件和网络访问；Native 使用每命令独立的临时 HOME/TMPDIR，默认保护真实 HOME、`~/.claw`、SSH/AWS/Kube 和 sibling projects，不继承凭据环境变量。Native 不提供 CPU/Memory/PID 强制上限。

Docker 额外保持以下限制：

```text
Network:      OFF
RootFS:       ReadOnly
Capabilities: DROP ALL
Workspace:    /workspace
Resource:     CPU / Memory / PID / Timeout
```

主 Agent 工作区可写，Explorer 工作区只读。

---

## 3. 配置模型

所有模型统一使用：

```text
provider/model
```

### ZAI Coding Plan

```bash
export ZAI_CODING_CN_API_KEY="..."

claw auth login zai-coding-cn --from-env
claw model set zai-coding-cn/glm-5.3
```

### OpenAI API Key

```bash
export OPENAI_API_KEY="..."

claw auth login openai --method api-key --from-env
claw models
claw model set openai/<model>
```

### Sign in with ChatGPT

```bash
claw auth login openai

claw models
claw model set openai/<actual-model>
```

OAuth 使用浏览器授权、PKCE 和 Dynamic Client Registration。

查看当前模型：

```bash
claw model current
```

---

## 4. 本地运行

```bash
claw \
  -mode demo \
  -workdir ./workspace \
  -prompt "分析当前项目，并总结主要模块"
```

恢复已有 Session：

```bash
claw \
  -mode demo \
  -resume <session-id> \
  -prompt "继续刚才的任务"
```

查看 Session：

```bash
claw sessions
claw session show <session-id>
claw session delete <session-id>
```

---

# Messaging Gateway：飞书 + Telegram

在 `~/.claw/config.json` 中合并以下配置（保留已有模型等设置）：

```json
{
  "channels": {
    "feishu": { "enabled": true },
    "telegram": { "enabled": true }
  },
  "messaging": { "report_mode": "compact" }
}
```

只要求已启用通道的环境变量；凭据不能写入 config.json 或 Workspace：

```bash
export FEISHU_APP_ID="..."
export FEISHU_APP_SECRET="..."
export TELEGRAM_BOT_TOKEN="..."

claw gateway -workdir ./workspace
```

可仅启用其中一个通道。未配置 / 未启用任何通道时，`claw gateway` 明确报错，不自动猜测。旧的 `claw -mode feishu` 暂时保留并打印弃用警告，始终只启动飞书；`claw -mode demo` 不变。

- 两个平台同时在线，私聊直接对话；群聊 `@bot`，Telegram 也支持回复 bot 的消息。
- 不同 Channel / Chat 独立 Durable Session，重启自动恢复，不跨平台绑定身份或共享历史。
- 同 Chat 顺序执行、不同 Chat 并行；共享审批与消息展示策略，Adapter 自行切分长文本。
- 一个通道临时失败会独立退避重连，不停止另一个；一个永久失败也不影响另一个，全部永久失败才退出。Ctrl+C 统一取消。

Telegram 使用 BotFather 创建的 Bot Token，仅 Long Polling + 纯文本。已有 webhook 会明确拒绝启动，**不会自动 deleteWebhook**；请自行移除 webhook，并确保同一 token 只有一个 update consumer。群聊开启 Privacy Mode 时，请通过 `@bot` 或回复 bot 交互；审批文本也应回复 bot 的消息。

机器人仅供可信用户使用；当前没有用户 / Chat allowlist 或管理员身份校验，审批作用域是会话，不是用户权限。

---

## 消息展示模式

`~/.claw/config.json`：

```json
{
  "messaging": {
    "report_mode": "compact"
  }
}
```

支持：

| 模式 | 行为 |
| --- | --- |
| `quiet` | 只显示最终结果、任务错误和审批 |
| `compact` | 默认。显示“正在处理” + 最终结果 |
| `verbose` | 显示 Thinking、Tool Call 和 Tool Result |

默认推荐：

```text
compact
```

因此聊天中通常只会看到：

```text
🤖 已收到，正在处理……

最终回答……
```

Tool Error 属于 Agent 内部执行过程，只有最终任务失败才会向用户显示错误。

---

# Human Approval

部分高风险操作会暂停并等待用户确认：

```text
approve <task-id>
```

或：

```text
reject <task-id>
```

审批：

- 与 Session 绑定
- 其他 Conversation 不能代为批准
- 默认超时拒绝
- Cancel 后自动拒绝
- 不经过普通任务队列

> Human Approval 不是安全边界。

最终权限仍由 Sandbox 和 Executor Policy 控制。

---

# Prompt 与项目指令

Prompt 由运行时动态组合：

```text
Core
Tools
Rules
User Context
Project Context
Skills
Runtime
Plan
Addendum
```

支持：

```text
~/.claw/AGENTS.md
~/.claw/SYSTEM.md
~/.claw/APPEND_SYSTEM.md

<project>/AGENTS.md
<project>/AGENTS.override.md

<project>/.claw/SYSTEM.md
<project>/.claw/APPEND_SYSTEM.md
<project>/.claw/skills/
```

项目级 `SYSTEM`、`APPEND_SYSTEM` 和 Skills 需要先信任项目：

```bash
claw trust
claw trust status
claw trust revoke
```

查看 Prompt：

```bash
claw prompt show
claw prompt render
```

`prompt render` 可能包含项目敏感信息，请勿直接分享。

---

# 全局目录

默认：

```text
~/.claw/
├── config.json
├── auth.json
├── models.json
├── AGENTS.md
├── SYSTEM.md
├── APPEND_SYSTEM.md
├── trusted-projects.json
└── sessions/
```

工作区：

```text
<workspace>/.claw/
├── SYSTEM.md
├── APPEND_SYSTEM.md
├── skills/
└── traces/
```

可以通过：

```bash
CLAW_HOME=/absolute/path
```

修改全局目录。

凭据不要放入 Workspace。

---

# Tools

Main Agent：

```text
read_file
write_file
edit_file
bash
spawn_subagent
```

Explorer：

```text
Native / Docker:
  read_file
  bash (ReadOnly Workspace)

Local:
  read_file
```

文件工具会检查：

- `..` Path Escape
- Absolute Path
- Symlink Escape
- Workspace Boundary

---

# Sandbox Modes

| 模式 | 行为 |
| --- | --- |
| `auto`（默认） | 根据 Policy / Capabilities：Native 优先，再检查 Docker；没有满足者则 fail closed；永不选择 Local |
| `native` | Anthropic SRT / OS-native 文件与网络隔离；资源 best-effort（CPU/Memory/PID 不强制） |
| `docker` | 一次性 Container、RO rootfs、cap drop、no-new-privileges、网络与 CPU/Memory/PID 强制限制 |
| `local` | **UNSAFE**：显式宿主当前用户执行，能读取凭据、联网和修改 workspace 外文件，仅限可信代码调试 |

```bash
# 日常 Coding：普通策略允许 Native
claw -mode demo -sandbox=auto -workdir ./workspace -prompt '分析代码'

# 严格资源：auto 只能路由 Docker
claw -mode demo -sandbox=auto -sandbox-policy=strict -workdir ./workspace -prompt '运行测试'

# Native 受控网络（应用配置，不是模型 Bash 参数）
claw -mode demo -sandbox=native -sandbox-network=allowlist \
  -sandbox-allowed-domains='github.com,*.githubusercontent.com,proxy.golang.org,sum.golang.org' \
  -workdir ./workspace -prompt '检查项目依赖'
```

网络默认 denied；allowlist 必须非空。Docker 暂不支持域名 allowlist，会拒绝该策略；Native 不支持 unrestricted 网络语义，也会拒绝。strict resources + domain allowlist 目前没有同时满足的后端，必须报错。启动打印实际 backend、filesystem、network 与资源 enforcement 状态。

**选择只发生在构建 Executor 时。Native / Docker 执行失败从不跨后端重试，不会重放已可能产生副作用的命令，也不会自动降级 Local。** 显式 native + strict 必须失败。Explorer 使用单独 RO executor；Local 下不注册 Explorer Bash。

无需模型或凭据即可直接验收：

```bash
claw sandbox -sandbox=auto -workdir . -command 'pwd; ls; git status'
claw sandbox -sandbox=auto -policy=strict -workdir . -command 'echo strict-docker'
claw sandbox -sandbox=native -policy=explorer -workdir . -command 'echo forbidden > new-file' # 必须失败
```

Native 是严格 allow-read 模型，HOME 下的 module cache/nvm/pyenv 等不会自动开放；使用系统工具链、workspace vendoring/cache 或 Docker。Docker 保留 `/tmp` noexec，Go 测试二进制使用 RW workspace 的 GOTMPDIR；不会为兼容而放松 tmpfs。

> Project Trust ≠ Sandbox；Prompt Instructions ≠ Security Boundary；Human Approval ≠ OS Sandbox。不要将 Messaging Gateway + Local 用于无人值守远程执行。

安全说明：[docs/security.md](docs/security.md) · 完整验收：[docs/sandbox-verification.md](docs/sandbox-verification.md) · 性能：[docs/sandbox-benchmark.md](docs/sandbox-benchmark.md)。

本次 macOS 已安装并实测 SRT 0.0.80：Native / Docker contract、安全、网络、取消与清理回归均通过，auto 普通策略实际选择 Native，strict 实际选择 Docker；Native / Docker benchmark 均已采样。Linux / macOS Native 与 Docker 的跨平台验证结果以 [GitHub Actions](https://github.com/ketitongxue/go-tiny-claw/actions/workflows/sandbox.yml) 为准；Windows Native 不支持。不要把缺依赖的 SKIP 当作 PASS。

---

# 常用命令

```bash
# Messaging
claw gateway -workdir ./workspace
claw gateway -check-config  # 仅本地检查，不联网验证通道

# 模型
claw models
claw model current
claw model set <provider/model>

# 登录
claw auth login
claw auth login openai
claw auth logout openai

# Session
claw sessions
claw session show <id>
claw session delete <id>

# Prompt
claw prompt show
claw prompt render

# Trust
claw trust
claw trust status
claw trust revoke
```
