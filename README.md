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
| Docker Sandbox | ✅ |
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
                         ┌────┴────┐
                         │         │
                      Docker     Local
```

一个 Conversation 对应一个独立 Session。同一 Conversation 串行执行，不同 Conversation 可以并行；不同 Channel / Account / Chat 的 Session 互相隔离。

---

## 环境要求

推荐环境：

- macOS / Linux
- Go 1.26.2
- Docker / Docker Desktop
- Git
- 对应模型 Provider 的凭据
- 按启用的通道准备飞书应用凭据 / Telegram Bot Token

检查：

```bash
go version
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

## 2. 构建 Sandbox

推荐使用 Docker Sandbox：

```bash
docker build \
  -f Dockerfile.sandbox \
  -t go-tiny-claw-sandbox:latest \
  .
```

默认 Bash 不访问宿主机，而是在一次性 Docker Container 中执行。

主要限制包括：

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
Docker:
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

# Sandbox

推荐：

```bash
-sandbox docker
```

或默认：

```bash
-sandbox auto
```

不会自动降级到 LocalExecutor。

如果明确使用：

```bash
-sandbox local
```

Bash 将直接以当前操作系统用户权限运行。

> 不建议将 `Messaging Gateway + LocalExecutor` 用于无人值守远程执行。

详细安全说明见：

[docs/security.md](docs/security.md)

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
