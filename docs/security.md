# Provider Credentials / Executor / Sandbox Threat Model

## Credential 与 Global Agent Home

- 全局 `~/.claw/config.json` 只保存普通设置，`models.json` 保存自定义 Provider/模型目录；API Key、OAuth access/refresh/ID Token 仅存放 `~/.claw/auth.json`。`host.json` 保存 installation Host ID，`openai-models.json` 保存无 token 的账号绑定模型缓存。可用 `CLAW_HOME` 显式更改全局位置，但不能放在工作区里。
- `<workspace>/.claw/` 保存项目 SYSTEM/APPEND_SYSTEM、skills/traces，**不是** Credential Store。启动入口拒绝工作区与 Global Agent Home 互相包含（包括可解析的 symlink 路径）；ResourceLoader 还拒绝 Global Agent Home 落在整个 Git Context Root 内，以免父级 AGENTS symlink 暴露全局资料。文件工具原有 workspace 相对路径检查保持，不为加载 Skill 放宽。
- 认证信息只由宿主 AuthManager 和 Provider transport 使用，不进入 Prompt、Tool Arguments、Session Message、Workspace、Trace 或 Sandbox Env。Trace 仅记录 provider/model、billing mode 和 auth type。
- `auth.json` 是**明文密码级资料**，文件权限必须为 Unix `0600`。保存使用同目录临时文件（0600）、fsync/close、rename；读取已有宽权限文件时自动修复为 0600，无法修复则报错；拒绝 symlink/非普通文件。不提交、不粘贴进日志/issue，不把它复制进 workspace，不烘焙进镜像。
- API Key 可来自 AuthStore 或明确配置的环境变量，AuthStore 优先。logout 只清除 Store，环境变量仍可能提供凭据。自定义配置仅接受 none 或 api_key + env，禁止明文 literal key、shell/command credentials、自定义 OAuth 和覆盖内置 Provider。
- OAuth 使用独立的 Authorization Code + PKCE(S256)、随机 state/nonce 和 `127.0.0.1` loopback callback。一次 attempt 的 authorization/token exchange 使用完全相同的 redirect URI。手动模式必须粘贴完整 callback URL；禁止裸 code 绕过 state/issued-client 校验。token exchange/refresh 不自动重试单次 grant、不跟随重定向、不打印 server body 或凭据。取消会关闭 callback listener。
- OpenAI 默认使用 Dynamic OSS Client Registration；`OPENAI_OAUTH_CLIENT_ID` 只保留为可选 static-client override，不是登录前置条件。项目不内置/冒用其他应用的 client ID，不读取任何外部 AI CLI 的凭据/缓存，也不执行外部 CLI 获取 token。服务端是否为某账号/Workspace 授予 Plan Usage，必须以真实授权结果为准。
- 每次调用模型前检查 token 到期时间，按 Provider 加锁刷新，使用已签发 client ID 和 resource，尊重 `earliest_refresh_at`，原子替换整套 tokens、过期信息和 scopes。显式 scope 缩减立即生效，先保存 rotation 再拒绝未授权推理。此锁覆盖**同一进程**的并发 Messaging Session；不提供跨进程 refresh/auth-file read-modify-write 锁，应避免多个进程同时登录/修改/刷新同一 Store。
- HTTP/SDK 原始 request/config/header 不写日志；Provider 对 HTTP 错误仅报告状态码，对 transport/JSON 错误报告脱敏信息，避免服务端反射 Authorization。环境变量名称、Provider ID 和配置路径可以输出，凭据值不可以。
- 自定义 Provider 可以读取发送给它的所有模型上下文，请仅使用可信网关。允许 HTTP localhost 是为了本地服务；远程含 Key 的连接应使用 HTTPS。

Messaging 凭据只从 `FEISHU_APP_ID` / `FEISHU_APP_SECRET` / `TELEGRAM_BOT_TOKEN` 环境变量读取；`channels` 配置只允许 enablement，拒绝 token / app_secret 等字段。Telegram API URL 包含 Bot Token，transport 错误和响应正文不原样输出，也不跟随重定向；不要启用会记录完整 URL 的 HTTP 代理日志。已有 webhook 不会被自动删除。

以上是应用的数据流边界，不是对同一 OS 用户的强制文件隔离保证。**LocalExecutor 是 UNSAFE**，可主动读取宿主 `~/.claw/auth.json`；如果要让凭据与不可信 Agent 命令隔离，必须使用默认 Docker 后端，并确保工作区没有凭据副本、hardlink 或敏感挂载。

## Session Store：Sensitive Local Data

完整对话、用户 Prompt、代码内容、Tool Output、Agent Response 与累计 Usage 存在全局 `~/.claw/sessions/`（或 `CLAW_HOME/sessions`），**不放在 workspace**，也不默认挂载到 Docker。Agent Home / sessions 为 0700，JSONL 文件为 0600；Session ID 经 SHA256 映射到文件名，真实 ID 只保存在事件中。禁止提交、分享或复制到 workspace。LocalExecutor 仍可按宿主权限读取这些文件，权限不隔离同一 OS 用户。

事件 schema v1 保存 session_created、完整 schema.Message、usage、metadata_updated；不保存 API Key 或 OAuth Access/Refresh/ID Token。模型 identity 固定，恢复只消费 ProviderManager / AuthManager 的解析与 refresh 能力。用户主动把秘密贴进 Prompt 或代码时，秘密仍可能进入对话记录；本模块不进行正文秘密检测或加密。

Append / Usage 持久化失败会返回错误，不假装成功。尾部损坏 JSONL 记录可恢复，中间损坏明确拒绝；重建合法 Tool Chain 失败时拒绝推理。创建采用私密临时文件 + sync + 原子发布，追加按 Session 独立 mutex 与 sync。Snapshot / Offset 仅在 Write、Sync、Close 成功后发布；失败后保留最后提交状态并恢复尾部，恢复本身失败则拒绝继续提交，等待后续恢复重试。任务 Context 传到 Persistence，取消不会刻意打断已经开始的文件提交。崩溃在 Tool Call 与结果之间可能留下未完成链，当前拒绝恢复推理，不自动重跑有副作用的工具。

M5.1 使用 **每 Session、单进程 writer ownership**：macOS / Linux 的 OS `flock` 作用于 SHA256(SessionID) 命名的 0600 `.lock` 文件。Demo 与 Messaging worker 在会话运行生命周期内持有锁；Create / Append / Usage / Delete 均受保护，活动 Session 的外部 mutation 会被拒绝。只读 List / Show 不获取 writer lock，读取完整事件前缀，按现有规则忽略尾部 torn event。OS 在进程退出或崩溃后自动释放锁；保留 `.lock` 文件是正常行为，**不要删除活动锁文件**，否则新 inode 可能绕过已有锁。

**Session writer lock 不是安全边界**，仅用于合作进程的一致性保护，不阻止同一 OS 用户、LocalExecutor 或其他绕过协议的程序直接修改文件。它不实现多进程同时写同一 Session，不覆盖 AuthStore / OAuth refresh，不提供分布式 / 网络文件系统锁保证。当前 writer lock 仅支持 macOS / Linux；其他平台的 mutation 会明确拒绝，而不是静默无锁运行。Session JSONL 仍是敏感本地明文数据，锁不提供保密性、磁盘加密、配额或安全擦除。

ContextBuilder 的省略与截断仅是当前 Provider 请求视图，绝不回写历史。Thinking 正文默认不进入 Conversation / Trace。`session delete` 只删除 Session 文件，不能删除 WorkDir、代码、Trace，也不保证底层存储上的安全擦除；备份和 Trace 需独立管理。

## Prompt Resources / Project Trust

Prompt 架构只改变模型指令的发现和组合，不改变模型 API、Session JSONL、Tool Schema、执行权限或审批。AgentEngine 只消费 `PromptSource → PromptState`；ResourceLoader 负责磁盘文件、信任与 diagnostics，Composer 仅做固定顺序的纯组装。

**Project Trust ≠ Security Sandbox。** `AGENTS.md`、`AGENTS.override.md`、`SYSTEM.md`、`APPEND_SYSTEM.md`、`SKILL.md` 全部只是 Model Instructions，不能作为 Security Boundary。真实安全机制仍是 Tool Registry、Executor、SandboxPolicy、Human Approval 和 Path Boundary。不要因为某项目已经 trust 就启用 LocalExecutor 或放宽网络/挂载；Prompt Injection 不会因存在 Sandbox 或 Trust 消失。

当前采用 normal-like 单一规则（未实现 strict 模式）：

- 用户级 `<CLAW_HOME>/AGENTS.md`、`SYSTEM.md`、`APPEND_SYSTEM.md` 视为用户配置。Global Agent Home 默认 `~/.claw`，必须独立于项目上下文根目录，请保护其写权限。
- 项目未信任时，普通 AGENTS/同层 override 仍可加载并影响模型；这意味着**未信任不等于不接受仓库指令**。项目 `.claw/SYSTEM.md`、`.claw/APPEND_SYSTEM.md`、`.claw/skills/` 不读取、不解析，发现时明确告警，并提示 `claw trust`。
- 信任时加载上述项目资源。Project SYSTEM > User SYSTEM > Builtin Core；Append 为 User → Project。SYSTEM 仅替换 core，动态 tools/rules/runtime/plan 不被替换；Explorer 的只读运行时角色保留，但任意自定义指令仍可能语义冲突，实际只读由 Registry/Executor 强制。
- `claw trust [status|revoke] [-workdir path]` 按 canonical Git root 精确授权；非 Git 使用 canonical WorkDir。Linked Worktree 的 Git file 仅作为本 worktree 边界，不跳转或执行 Git 配置/hook，不隐式授权其他 worktree。没有按字符串前缀继承的 trust，也没有内容签名、逐文件批准或变更后自动撤销；同路径换仓库仍会保留授权，删除/替换项目应先 revoke。
- `<CLAW_HOME>/trusted-projects.json` 为 0600 普通文件，拒绝 symlink/非普通文件，损坏/不可读时 fail closed 并告警，绝不默认为 trusted。保存采用现有私密临时文件、sync/close、原子 rename。更新通过 `.lock` 目录串行 read-modify-write，锁冲突拒绝；进程崩溃可遗留 lock，仅确认无 trust 命令运行后清理。此锁是合作进程的一致性保护，不是防同一 OS 用户篡改的安全边界。

Context 从 WorkDir 向上止于最近 `.git` 标记，无标记仅加载 WorkDir。用户规则另行加载，不从 `/` 的任意父级读取 AGENTS。相同目录 override 优先，损坏不 fallback；canonical path 去重，Context file symlink 必须留在用户 Home / Git Root 对应作用域内。Skills 只发现当前 WorkDir 的 `.claw/skills/`，canonical location 必须落在 WorkDir 内；内部文件 symlink 可用，外部拒绝，嵌套目录 symlink 不遍历。Skill location 是宿主路径，额外 `read_path` 是现有 `read_file` 可用的工作区相对路径；相对引用仍不得越过工具安全边界。没有额外的 workspace 外读取白名单，也不添加 load_skill 工具。

AGENTS/SYSTEM/APPEND 单文件读取限制为 1 MiB；AGENTS 失败告警并跳过，存在的 SYSTEM/APPEND 失败为 Error，阻止继续用默认 Core 假装成功。Skill 只解析至多 16 KiB 的 YAML header，name/description 必填且限长；坏 Skill 跳过、同名先有效路径优先，后续告警，不中止整个 Agent。正文按需读入后仍属于完整 Tool Output，可能消耗 Token Budget/Session 磁盘；lazy loading 不是 IO/磁盘 quota。canonicalization 也不是原子文件系统隔离，不能抵御恶意并发 symlink/rename/hardlink 操纵。

Section 使用固定名称与边界，资源文本转义防止**格式层面**跨 Section 拼接，保留 Markdown 换行；转义不能阻止语义 Prompt Injection。每次 Run/Resume 重新检查资源与 Trust；单次运行内不热更新，revoke 不取消正在执行的任务，也不从已有历史删除已读取的 Skill 内容。

观测仅记录 Section 名称、SHA256 和 count/token metrics，不记录完整 System Prompt。`claw prompt show` 仅列来源路径与大小，仍可能泄露项目路径；`claw prompt render` 明确警告并显式打印正文，可能包含项目/用户敏感信息，不要上传日志或分享。资源按需读取后依然可能进入 Session 与现有 Tool Trace preview；上述隐私边界不提供加密、自动秘密检测或日志净化。

## Dynamic OpenAI Registration / Host ID

首次请求 `https://auth.openai.com/api/accounts/authorize` 使用入口 `dynamic_agent_client`、固定 `agent_name_hint=go-tiny-claw`、Host ID、PKCE、state、nonce，以及 OpenID/离线刷新/资源调用/Plan Usage scopes。先校验 callback state，再提取 code 和 issued client ID；缺少 issued ID 或返回入口 ID 即失败，**不会交换 token，也不能保存入口 ID**。Token exchange 为 `https://auth.openai.com/api/accounts/oauth/token`，使用 issued client ID，不发送 client secret。

`ext_agent_host_id` 是全局 `host.json` 中持久化的 `urn:uuid:<UUIDv4>`。首次创建采用跨进程创建锁及 0600 原子写入，以后登录重复使用；logout 不删除它。不同 Agent Home 代表不同 installation。Host ID **不是 API Key、token、identity proof**，但具备关联性，不能写普通业务日志、Trace 或模型输入。不要复制其他安装的 `host.json`；坏文件不自动重生成。创建进程崩溃留下 `host.json.lock` 且尚无 host 文件时，确认没有登录进程后才可清理残留锁。

Returning login 复用已保存的 issued client ID 和同一 Host ID，可发送保存的 `id_token_hint` / 签名验证后的 email hint；callback 改 client ID 或新 subject/issuer 与既有身份不一致时拒绝覆盖。主动换账号需要先 logout。缺少验证身份的 legacy OAuth 记录仍可读取/删除，但不能直接用于推理。

## ID Token / OAuth Credential

`internal/auth/oidc.go` 通过 go-oidc/go-jose 从 `https://auth.openai.com/.well-known/jwks.json` 获取公钥并验签，缓存密钥、支持 key rotation；只允许 RS256/PS256/ES256，不接受 unsigned/HMAC token。严格匹配 issuer `https://auth.openai.com`、issued-client audience、expiration 和当前 attempt 的 nonce；多个 audiences 还要求匹配的 authorized party。未知 key、错误签名/issuer/audience/nonce、过期或缺失 ID Token 都不能完成登录。identity 仅取自已验证 ID Token，不解码 access token 来伪造身份；issuer + subject 是稳定账号绑定，email 只是属性。

`auth.json` 的 `openai` OAuth record 保存 `type`、issued `client_id`、`ext_agent_host_id`、`issuer`、`subject`、`email`、`id_token`、`access_token`、`refresh_token`、`token_type`、`expires_in`、`expires_at`、可选 `earliest_refresh_at` 与 `scopes`。ID Token 是敏感凭据，保留供后续 reauthorization hint 使用；refresh 返回新 ID Token 时也验证签名/身份（refresh 不要求原 attempt nonce），缺省则保留旧值。

**OAuth credentials 永不进入 Workspace、Sandbox、Prompt、Tool 参数、Session、Trace 或普通日志。** 主进程只在认证 transport 使用 token。自动登录只显示状态，不打印授权 URL；manual 私密展示移除 `id_token_hint` 和 `login_hint`，不支持日志记录该交互。HTTP 错误仅输出状态或固定白名单错误码；库/服务器原始错误也不输出，防止 token/Authorization 被反射。OAuth credential/session/config 的常规格式化输出均脱敏。不要使用 HTTP debug proxy 或日志钩子记录这些 transport。

logout 至少清除本地凭据与 OAuth 模型缓存，保留 Host ID；目前不调用远程 revoke endpoint，会明确说明 remote revocation 未尝试。删除本地文件不保证服务端授权被撤销；需要时通过账号端管理授权。环境 API Key 仍可能生效。应用不会把 OAuth 失败自动 fallback 到 API Key。

## ChatGPT Plan Usage / Model Discovery / Responses

身份连接和订阅权限分开处理。Token response 的实际 granted scopes 必须包含 `chatgpt.tokens.use.direct`，不能把请求 scope 或 callback scope 当成订阅授权。缺失时保存有效身份并显示 Plan Usage disabled；拒绝模型发现/推理，提示重新授权或显式配置 API Key。

OAuth 目录由 Bearer token 认证的 `GET https://api.openai.com/v1/models` 获取，仅注册 `visibility=list` 的 slug。无 token 的全局缓存绑定 auth type、issuer、subject、client ID、Host ID；不复用 API Key 静态目录或其他账号缓存。登录/`claw models` 在线刷新，启动、`model set` 与 `-check-config` 离线读取。缓存不是实时 entitlement/配额证明；目录未提供 tool capability 时按 Responses 工具契约处理，调用能力最终仍由服务端决定。

OAuth 推理仅用 `POST https://api.openai.com/v1/responses` + Bearer，强制 `store=false`、`stream=true`。不使用 ChatGPT backend-api、previous_response_id 或 system input role；系统指令映射到 instructions，历史和工具结果来自 Working Memory。SSE 只有 `response.completed` 才成功，failed/incomplete/断流/取消均丢弃部分成功结果。订阅共享 usage limit exceeded/unavailable 使用固定清晰错误，不自动改用 API Key。SSE 在 Provider 内聚合，不改变 AgentEngine，也不实现实时 UI streaming。

真实订阅路由可能省略 Content-Type，Provider 通过有界正文格式检测 SSE，不接受普通 JSON/HTML 作为成功。终态 output 为空数组时，仅采用此前 output_item.done 已完成项；非空终态输出优先。上述聚合仍要求 response.completed；错误诊断仅包含 HTTP 状态、白名单媒体类型和正文格式分类，不输出原始响应正文/未知响应头。

## 范围与信任边界

本版本为 **Bash 命令**提供可插拔的执行后端：

```text
Agent → Human Approval Policy → BashTool → Executor → OS
                                               ├ LocalExecutor（UNSAFE）
                                               └ DockerExecutor + SandboxPolicy
```

Tool 不决定权限。主 Agent 使用 CodingPolicy（workspace RW），Explorer 使用独立的 ExplorerPolicy（workspace RO）。默认 `-sandbox=auto` 仅选择 Docker；CLI、daemon、镜像或参数检查失败均报错退出，运行中失败也不会切换 Local。

受信任组件包括：主进程与其配置、宿主 OS、Docker CLI/daemon/runtime、所选镜像，以及批准任务的用户。模型生成的命令与代码不应被视为可信。支持本机 Docker / Docker Desktop 的 Linux 容器；远程 Docker context、VM 与远程 Executor 不在本阶段支持范围内。

## Docker Sandbox 能保护什么

在 runtime 没有漏洞、镜像与挂载配置可信的前提下：

- **未挂载的宿主文件**：仅 WorkDir 绑定到 `/workspace`。宿主 HOME、SSH Key、云凭证等不会因容器中的同名路径而可见。
- **宿主大部分进程**：使用 Docker 默认私有 PID namespace；不挂载宿主 proc，不使用 host PID 或 privileged。
- **RootFS**：`--read-only`；临时文件和 Go 缓存使用 128 MB `/tmp` tmpfs。`touch /root/test` 失败。
- **默认网络**：`--network none`，无外部网络路由；模型不能通过 Bash 参数打开网络。curl 外部连接失败。
- **资源**：memory 与 memory-swap 均限制为 Policy 的内存，CPU quota、PID 数量和命令超时有界。Coding 为 1024 MB / 2 CPU / 128 PIDs / 30s；Explorer 为 512 MB / 1 CPU / 64 PIDs / 30s。
- **提权能力**：drop ALL capabilities，`no-new-privileges=true`，保留 Docker 默认 seccomp，不授予额外权限。
- **Explorer workspace 写入**：readonly bind mount 由 OS 拒绝重定向、删除、mkdir、sed -i 等写操作，不依赖 Prompt 或命令正则。

Explorer 的“只读”指 workspace 与 RootFS；允许写临时 `/tmp`。它不是文件系统快照，主 Agent 或其他宿主进程仍可能并发修改它正在阅读的文件。

## 挂载与环境变量

执行前 WorkDir 必须非空、存在且为目录，通过 absolute path + symlink canonicalization。禁止挂载 `/`、整个 HOME 及其祖先、HOME 的 `.ssh/.aws/.config` 目录及其后代、Docker socket 或其父目录。工作区中存在宿主 Unix socket/设备时拒绝执行。正常的 HOME 下项目目录可用。

bind mount 禁用递归子挂载并采用 rprivate propagation，避免继承 workspace 内已有的其他挂载。Docker 参数通过 `exec.CommandContext("docker", args...)` 传递；mount 采用 CSV 编码，路径里的空格、逗号和引号不能注入额外挂载参数。禁止挂载 Docker socket，尤其是 `/var/run/docker.sock`。**network=none 不能阻止宿主 Unix socket 的访问**，所以 socket 不能作为普通项目文件暴露。

Docker 不复制 `os.Environ()` 到容器。默认 `AllowedEnv` 是显式 Request.Env 的精确白名单：`PATH/LANG/LC_ALL/TERM`；未授权键被拒绝。宿主 `OPENAI_API_KEY/ZAI_CODING_CN_API_KEY/ZHIPU_API_KEY/GITHUB_TOKEN/AWS_*/SSH_*/FEISHU_APP_SECRET/TELEGRAM_BOT_TOKEN` 不会自动传入。Docker CLI 自身仍在受信任宿主上运行，按用户配置连接 daemon；这不等于把 CLI 的环境传给容器。

镜像自身的 ENV 与入口文件也必须可信。不要把凭据烘焙进镜像，不要在 workspace 里放密钥、宿主 socket、敏感日志或业务数据。`.dockerignore` 避免构建基础镜像时发送项目和 workspace 内容；使用自定义镜像前需自行审查。

## Docker Sandbox 不能保护什么

- **Coding workspace 的完整性与保密性**：RW workspace 可被删除、覆盖或恶意修改；RO workspace 的内容也仍可被读取并返回给模型。
- **显式挂进去的宿主文件**：挂载内容本身即对容器开放。选择 WorkDir 时必须使用干净的项目副本，而不是宽泛的系统目录。
- **显式允许的环境变量**：扩大 AllowedEnv、传入凭据或把凭据写入文件会主动扩大暴露范围。
- **显式启用的网络**：程序化 `NetworkEnabled=true` 使用 bridge，不具备域名白名单、代理或凭据保护。本版 CLI 没有网络开关。
- **Docker runtime / 内核漏洞**：容器不是独立内核的 VM，不能承诺抵御容器逃逸。需维护和升级宿主、daemon 与可信镜像。
- **整个 Agent 主进程**：Provider、read/write/edit 文件工具、会话、Trace 等仍在宿主执行；文件工具有 workspace 路径检查，但不是 OS Sandbox。此改动不将整个 Agent 容器化。
- **宿主文件竞态与特殊文件关系**：canonicalization 和扫描不能原子锁定挂载源；并发重命名、symlink 变化、hardlink 与其他宿主进程可能扩大风险。不要允许不可信宿主用户并发操纵挂载源，也不要把宿主敏感文件 hardlink 到 workspace。
- **宿主磁盘和完整输出内存**：workspace 的 RW 写入没有磁盘 quota；Executor 暂在主进程内保存完整 stdout/stderr。ContextBuilder 模型请求视图的 Token 截断不等于宿主内存上限，大量输出仍可能造成主进程资源耗尽。
- **任务总成本与全局资源**：单命令限制不等于整个 Agent 的预算；多条并发命令叠加资源，主循环没有全局最大轮数和费用上限。
- **数据外传到模型服务**：读取内容会进入 Provider 上下文，也可能留存在日志与 Trace；网络关闭只限制容器网络，不关闭主进程调用模型的网络。

## LocalExecutor = UNSAFE

LocalExecutor 只适用于开发、调试或明确受信任环境。它仅设置 cwd 和超时，不阻止命令 `cd /`、读取 `~/.ssh`、联网或修改工作区外文件。命令拥有宿主当前用户权限。

Local 默认只继承少量基础变量（PATH、LANG、LC_ALL、TERM、HOME、TMPDIR），但这**不构成秘密隔离**：宿主文件、进程和用户权限仍可访问。

必须显式 `-sandbox=local` 才能启用；启动时打印警告，Messaging Gateway / 旧飞书模式额外指出远程宿主执行风险。Local 无法强制只读，因此该模式的 Explorer 不注册 Bash，仅开放 `read_file`。不要把 Local + RO Policy 当作隔离环境。

## 失败、取消与清理

- Executor 中普通非零命令退出返回结构化 ExitCode；启动/daemon/工作目录/取消等错误返回 InfrastructureError，保留部分输出和 `errors.Is` 语义。
- BashTool 对基础设施错误、超时、取消、非零退出均返回错误；Registry 保留 stdout/stderr 与退出状态，设置 `ToolResult.IsError=true`，用于 Recovery、Reminder 和下一轮 Observation。
- Unix Local 使用独立进程组，在取消时 SIGKILL 进程组，正常结束也尽可能清理子进程；主动 setsid/逃离进程组的宿主子进程不能保证清理。非 Unix 平台目前只能保证直接进程终止。
- Docker 每次创建带随机名称和 `go-tiny-claw.sandbox=true` label 的容器，不复用持久会话。取消时杀 CLI 后，使用独立的、最多 5s 的 Context 执行 `docker rm --force`，并短暂重试容器创建竞态。
- daemon 不可用或强制删除失败会返回错误，包含容器名称。主进程遭 SIGKILL、崩溃或宿主掉电时清理代码可能无法执行；请检查遗留容器，不要假定 `--rm` 在这些情况下自动停止正在运行的容器。

定位遗留容器（确认归属后，仅清理对应容器，不影响其他会话）：

```bash
docker ps -a --filter label=go-tiny-claw.sandbox=true
docker rm --force <container-name>
```

## Approval 不是 Sandbox

`IsDangerousCommand` 是 **Human Approval Layer** 的启发式规则，不是 **Security Boundary**。复杂 shell 表达式可能绕过正则。人工审批允许的是“在既定 Sandbox 权限内执行”，不是解除 RO、网络或资源限制。

Prompt、AGENTS.md、SYSTEM.md、APPEND_SYSTEM.md 和 Skills 同样不是安全边界，Project Trust 也不授予任何 Sandbox 之外的权限。飞书 / Telegram 远程接入还需要身份与访问控制、聊天作用域、审批权限和数据管理，不应在公开远程服务中启用 Local。当前没有用户 / Chat allowlist 或管理员角色校验；任何能向 bot 私聊或在群中触发它的人都可能运行任务，同一会话内的成员可以审批。Session 与 Approval 按 Channel / Account / Chat 隔离，不合并跨平台身份；这不隔离共享 Workspace 中的文件，也不构成用户授权。

## 验证与后续范围

```bash
docker build -f Dockerfile.sandbox -t go-tiny-claw-sandbox:latest .
go test ./...
go vet ./...
go test -count=1 -v ./internal/executor -run DockerIntegration
go test -count=1 -v ./cmd/claw -run DockerSessionFactoryIntegration
```

integration tests 验证 workspace 可访问/Coding 可写/Explorer 不可写、RootFS 只读、无外部路由与连接、宿主文件和凭证不可见，以及取消/超时后无新增遗留容器。网络测试使用 TEST-NET 地址，不依赖公网服务。Engine 测试使用 fake Provider + 真实 Docker，验证所有现有工具与失败 Recovery，不调用模型。

daemon 或测试镜像不可用时 integration tests 可 Skip；**生产 auto 不允许安全降级**。可以通过 `CLAW_SANDBOX_TEST_IMAGE` 选择可信测试镜像。真实模型与飞书 / Telegram 消息链路需另行验证。

本阶段不实现 Kubernetes、gVisor、Firecracker、VM、bwrap、macOS sandbox-exec、网络白名单/credential proxy、SSH/远程 Executor 或长生命周期容器。现有 Executor 接口保留 streaming/cancel 扩展点，后续 Session Sandbox 不要求修改 AgentEngine。
