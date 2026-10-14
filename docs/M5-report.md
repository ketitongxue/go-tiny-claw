# M5 完成报告：Durable Session & Context Budget

## 1. 开发前代码基线

工作树干净，HEAD `8ae002d`。

最近五次提交：`8ae002d` Provider/Auth、`7b25b13` 独立仓库、`9069f84` GLM pricing、`6d25c0b` README、`a4c8043` Executor/Sandbox。

开发前重新读取 Session、Feishu、Runtime、CLI、Provider Manager/Model、Config、Tracker 等当前代码。确认飞书直接 NewSession、Demo 固定 `test_trace_001`、共享启动 Provider，以及主/子 Agent 旧 Compactor 路径。基线 test/race/vet 全通过。

## 2. 实际架构变更

```text
CLI / Feishu worker
  → SessionService → MemoryStore / FileStore
  → Session.ModelRef → ProviderManager → AuthManager
  → session-scoped Provider / CostTracker / AgentEngine
  → PromptComposer + full history
  → ContextBuilder (Policy + Estimator + ModelContextSpec)
  → bounded Provider request view
```

History 保存完整事实；请求视图与磁盘存储分离。移除 GlobalSessionMgr，没有修改 Executor、SandboxPolicy、Explorer RO 或 Auth 设计。

## 3. 新增 / 修改文件

新增生产文件：
- `internal/sessionstore/{store,file,sync}.go`
- `internal/context/{service,builder,policy,estimator}.go`
- `internal/engine/context.go`

修改生产文件：
- `cmd/claw/{main,commands}.go`
- `internal/config/{config,paths}.go`
- `internal/context/session.go`
- `internal/engine/loop.go`
- `internal/feishu/bot.go`
- `internal/observability/tracker.go`
- `internal/provider/wire/http.go`
- `internal/provider/openai/responses.go`
- `internal/tools/{bash,read_file,registry}.go`

删除：`internal/context/compactor.go`。

文档：`README.md`、`docs/security.md`、本报告。测试文件见第 14 节。

## 4. SessionStore 设计

Store 提供 Create、Append、Load、List、Delete。MemoryStore 使用每个 Session 的锁；FileStore 按绝对文件路径使用独立锁，同进程多个 Store 实例共享对应锁，不串行所有聊天。

路径：`~/.claw/sessions/`，复用 `config.Paths.Sessions` / `CLAW_HOME`。Agent Home 与 sessions 0700、文件 0600，读取会修复宽文件权限。文件名为 SHA256(ID)，真实 ID 仅在记录中。拒绝 symlink / 非普通 Session 文件。

创建使用同目录私密临时文件、fsync、原子 hard-link 发布并同步目录；追加 JSONL 后 fsync。尾部损坏记录可忽略，下一次追加前移除尾部残片；中间损坏、未知版本/类型明确返回 SessionCorrupted。单进程拥有 Store，不提供跨进程写锁。

## 5. Event Schema

v1：`version`、`type`、`timestamp`，可选 `metadata` / `message` / `usage`。

- session_created：ID、WorkDir、CreatedAt、UpdatedAt、ProviderID、ModelID、BillingMode、AuthType 与初始 Usage。
- message：直接复用完整 `schema.Message`，包括 ToolCalls、ToolCallID、Usage。
- usage：累计 Prompt/Completion Tokens、EstimatedCost、CostKnown、Currency、BillingMode。
- metadata_updated：完整 Metadata 更新事件，Store 可重放。

不保存 API Key 或 OAuth Token；不会复制另一套 Message schema。

## 6. SessionService 设计

Create / Load / Resume / GetOrCreate / List / Delete，按 Session 同步生命周期并缓存已加载对象。Session.ModelRef 暴露固定模型 identity。

Append、RecordUsage、MarkCostUnknown 返回 error；Mutation 单入口先持久化再提交内存，失败传播到 Engine / Tracker / CLI / Feishu worker。不会要求业务层同时调用 Session 与 Store 两次。Usage 恢复后继续累计。

模型元数据在创建时固定，Tracker 不重新覆盖 Durable Session identity。没有引入 Session Fork 或模型切换命令。

## 7. Feishu Resume 流程

ChatID → Chat worker → SessionService.Resume；不存在才检查新 Session 的默认模型并 Create → 按原 ModelRef 创建 Engine → Append 新 User → Build Context → Run。

磁盘和凭据解析位于各 worker，不在 Bot 的全局队列锁下执行。恢复错误、凭据错误、持久化失败会明确向聊天反馈，不假装继续。已有 Session 的恢复不会因为新的默认模型不可用而被默认模型检查拦截。

## 8. Session-scoped Provider 流程

Composition factory 从 Session.ModelRef 调用现有 ProviderManager.Create，每个 Session 独立创建 Provider、CostTracker、AgentEngine、Registry/审批闭包。

凭据仍由 AuthManager 获取；既有 managedProvider 继续每次调用解析/刷新凭据。没有修改 OAuth refresh 责任，也没有保存 Token 到 Session。修改 default_model 仅影响新 Session。

## 9. Context Budget 算法

优先模型 ContextWindow / MaxOutput，未知时统一 fallback：32768 window、4096 reserved output、10% margin。全局 config.context 可覆盖默认策略；custom models.json 元数据继续有效。

InputBudget = Window − ReservedOutput − ceil(Window × Margin)。Tool Schema 也计入输入预算。

验证工具链，按普通 User 起点构建完整轮次；保留 System 与当前轮次，从最新向过去选择连续完整轮次。System / 当前 User 不偷偷截断。预算不合法、当前完整轮次放不下、工具链非法均明确失败。

Provider 明确 context_length_exceeded 时，以 75% window 重建并只重试一次；transport 只暴露白名单错误 code，不泄露原始服务端正文。

## 10. Token Estimator

保守 heuristic：ASCII 字母/数字/空白约 2 chars/token；JSON/操作符标点约 1/token；CJK 与其他非 ASCII 更保守估计。包含 message envelope、Content、ToolCall Name、Arguments、ID 与 ToolCallID，不仅计算 Content。

不是官方 tokenizer，也不保证所有 Provider 的精确计数。

## 11. Tool Output 截断策略

请求视图按 MaxToolResultTokens 截断 Tool Result：rune-safe Head + Marker + Tail，二分搜索可容纳长度，估算包含 marker/envelope。

删除 read_file / bash 的旧 8000-byte 工具层截断，Session 现在可收到完整成功/失败 stdout/stderr 和文件内容。Executor 的运行、隔离、取消、超时行为没有改变。Reporter 与 Trace preview 也改成 rune-safe。

## 12. Thinking Mode 修改

Thinking 仅用于当前 Action 的临时历史；Action 前重新 Build 检查 Budget。Durable Conversation 只保存 Action Content、ToolCalls、Usage，不再合并 Thinking 正文。

Thinking Trace 记录 status、duration 与 token usage 元数据，不保存正文。Thinking 也支持最多一次明确 context limit 重试。

## 13. 旧 Compactor 删除情况

删除 Compactor 实现；主/子 Agent 全部使用 ContextBuilder，不再使用 GetWorkingMemory(20)、3000 字符阈值或 500+500 bytes。

正常裁剪只写 Trace：window_tokens、input_budget_tokens、estimated_tokens、total_messages、included_messages、omitted_messages、truncated_tool_results，均带 `context.` 前缀。无旧内存告警/压缩完成日志。

保留旧 GetWorkingMemory(limit) 兼容 API，但生产 Engine 仅获取完整历史（limit=0），预算由 Builder 决定。

## 14. 新增测试

- `internal/sessionstore/store_test.go`：两种 Store CRUD、Model/Auth/Usage/Message Usage、Tool Chain、metadata_updated、尾行恢复/追加修复、中间损坏、并行 Session、0700/0600。
- `internal/context/service_test.go`：恢复/Pinning/累计 Usage、WorkDir 缺失、Demo 唯一 ID、持久化失败不提交内存。
- `internal/context/builder_test.go`：短/长 Session、近期完整轮次、System、Tool Chain、巨大输出、中文/emoji、Arguments 估算、fallback、预算错误、100 条完整磁盘历史与有限请求视图联合测试。
- `internal/context/provider_resume_test.go`：OpenAI OAuth / ZAI / custom identity 的模拟恢复；AuthManager refresh、凭据/模型缺失、Session 文件无 Token。OAuth transport 是 fake，不代表真实 OpenAI 登录验收。
- `internal/feishu/resume_test.go`：Bot A/B worker 重启、历史恢复、旧模型与新默认模型分离。
- `internal/engine/context_test.go`：32 轮并行 read_file/bash、无 Compactor 日志、完整历史、Trace metrics、只重试一次、Explorer 长输出。
- `internal/engine/loop_test.go`：更新 Thinking 断言，验证不持久化并保留并行 Tool Chain。
- `cmd/claw/{sessions_test,resume_integration_test}.go`：CLI metadata-only/list/delete，真实 HTTP fake OpenAI-compatible Provider 的 Demo 恢复、默认模型改变、唯一 ID、WorkDir 缺失。
- `internal/tools/durable_output_test.go`、更新 bash tests：工具完整 Unicode 输出不再提前截断。
- `internal/context/session_test.go`：旧 Compactor 测试迁移到 Builder。

## 15. 自动验收结果

M5A 独立阶段：`go test ./...`、`go test -race ./...`、`go vet ./...` 全通过，然后才进入 M5B。

最终：

```text
gofmt                       完成
go test -count=1 ./...      PASS
go test -race -count=1 ./... PASS
go vet ./...               PASS
git diff --check           PASS
```

没有调用真实付费模型、真实 OAuth 登录或飞书远程消息。相关真实服务验收仍需用户执行。未创建 Git commit。

## 16. 人工验收步骤

先构建 `go build -o claw ./cmd/claw`，使用现有可信 Docker 镜像和凭据。

A. `./claw -mode demo -workdir <project> -prompt "阅读当前项目并记住 package 数量"`；运行 `./claw sessions` 获取 ID，再运行 `./claw -mode demo -resume <id> -prompt "刚才你分析了什么？继续分析"`。验证原 WorkDir 和历史延续。

B. Model A 创建 Session，`./claw model set <provider/model-b>`，恢复 A。终端 Session/Model 应仍为 A，Trace 的 provider_id/model_id 与实际请求也应为 A；新 Demo 应使用 B。

C. 飞书 Chat 多轮任务后停止进程，再启动，同 Chat 发送“继续刚才的任务”，验证此前历史与原模型恢复。OAuth / ZAI 各做一次；OAuth refresh 继续由现有 AuthManager 处理。

D. 包含大量 read_file/bash 的多轮任务，确认没有旧 Compactor 告警，Trace 包含七个 context.* 指标；正常 omission/truncation 不属于告警。

E. 通过本地可信编辑器查看 Agent Home 下的 JSONL，确认模型视图省略的历史与截断工具结果仍完整存在。不要把这些敏感记录复制到 workspace 或公开分享。

F. `session show` 不应显示正文；`session delete` 后 WorkDir、代码、Trace 应仍存在。

## 17. 已知限制与停止范围

- 单进程拥有 Session Store，不支持多进程同时写同一 Session。
- Full History 明文且无 quota；完整 Tool Output 会增长宿主内存与磁盘。
- FileStore 追加前重放文件以检测损坏/修复尾行；超长 Session 的 IO/CPU 可继续优化，本轮不加索引/数据库。
- Estimator 是 heuristic，不是官方 tokenizer；保留完整当前轮次可能超预算，此时明确报错而非切断工具链。没有 Summary / Retrieval fallback。
- 崩溃可能留下完整 Assistant ToolCall 但尚无结果：允许加载事实，但 Builder 明确拒绝不完整链，不自动执行有副作用的工具。
- Token/Session 加密、秘密正文检测、跨进程锁、云同步、Fork、SQLite 不在本轮范围。
- Resume 沿用创建时的模型/认证/计费元数据；不能把更改凭据/订阅方案视为会话内模型切换功能。
- 模拟 OAuth/Feishu 测试不能替代真实账号权限、网络、refresh rotation 和飞书端到端人工验收。

M5A + M5B 自动验收通过后停止，等待上述人工验收，不进入 M6。
