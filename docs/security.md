# Executor / Sandbox Threat Model

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

Docker 不复制 `os.Environ()` 到容器。默认 `AllowedEnv` 是显式 Request.Env 的精确白名单：`PATH/LANG/LC_ALL/TERM`；未授权键被拒绝。宿主 `ZHIPU_API_KEY/GITHUB_TOKEN/AWS_*/SSH_*/FEISHU_APP_SECRET` 不会自动传入。Docker CLI 自身仍在受信任宿主上运行，按用户配置连接 daemon；这不等于把 CLI 的环境传给容器。

镜像自身的 ENV 与入口文件也必须可信。不要把凭据烘焙进镜像，不要在 workspace 里放密钥、宿主 socket、敏感日志或业务数据。`.dockerignore` 避免构建基础镜像时发送项目和 workspace 内容；使用自定义镜像前需自行审查。

## Docker Sandbox 不能保护什么

- **Coding workspace 的完整性与保密性**：RW workspace 可被删除、覆盖或恶意修改；RO workspace 的内容也仍可被读取并返回给模型。
- **显式挂进去的宿主文件**：挂载内容本身即对容器开放。选择 WorkDir 时必须使用干净的项目副本，而不是宽泛的系统目录。
- **显式允许的环境变量**：扩大 AllowedEnv、传入凭据或把凭据写入文件会主动扩大暴露范围。
- **显式启用的网络**：程序化 `NetworkEnabled=true` 使用 bridge，不具备域名白名单、代理或凭据保护。本版 CLI 没有网络开关。
- **Docker runtime / 内核漏洞**：容器不是独立内核的 VM，不能承诺抵御容器逃逸。需维护和升级宿主、daemon 与可信镜像。
- **整个 Agent 主进程**：Provider、read/write/edit 文件工具、会话、Trace 等仍在宿主执行；文件工具有 workspace 路径检查，但不是 OS Sandbox。此改动不将整个 Agent 容器化。
- **宿主文件竞态与特殊文件关系**：canonicalization 和扫描不能原子锁定挂载源；并发重命名、symlink 变化、hardlink 与其他宿主进程可能扩大风险。不要允许不可信宿主用户并发操纵挂载源，也不要把宿主敏感文件 hardlink 到 workspace。
- **宿主磁盘和完整输出内存**：workspace 的 RW 写入没有磁盘 quota；Executor 暂在主进程内保存完整 stdout/stderr。模型侧 8000 字节截断不等于宿主内存上限，大量输出仍可能造成主进程资源耗尽。
- **任务总成本与全局资源**：单命令限制不等于整个 Agent 的预算；多条并发命令叠加资源，主循环没有全局最大轮数和费用上限。
- **数据外传到模型服务**：读取内容会进入 Provider 上下文，也可能留存在日志与 Trace；网络关闭只限制容器网络，不关闭主进程调用模型的网络。

## LocalExecutor = UNSAFE

LocalExecutor 只适用于开发、调试或明确受信任环境。它仅设置 cwd 和超时，不阻止命令 `cd /`、读取 `~/.ssh`、联网或修改工作区外文件。命令拥有宿主当前用户权限。

Local 默认只继承少量基础变量（PATH、LANG、LC_ALL、TERM、HOME、TMPDIR），但这**不构成秘密隔离**：宿主文件、进程和用户权限仍可访问。

必须显式 `-sandbox=local` 才能启用；启动时打印警告，飞书模式额外指出远程宿主执行风险。Local 无法强制只读，因此该模式的 Explorer 不注册 Bash，仅开放 `read_file`。不要把 Local + RO Policy 当作隔离环境。

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

Prompt、AGENTS.md 和 Skills 同样不是安全边界。飞书远程接入还需要身份与访问控制、聊天作用域、审批权限和数据管理，不应在公开远程服务中启用 Local。

## 验证与后续范围

```bash
docker build -f Dockerfile.sandbox -t go-tiny-claw-sandbox:latest .
go test ./...
go vet ./...
go test -count=1 -v ./internal/executor -run DockerIntegration
go test -count=1 -v ./cmd/claw -run DockerSessionFactoryIntegration
```

integration tests 验证 workspace 可访问/Coding 可写/Explorer 不可写、RootFS 只读、无外部路由与连接、宿主文件和凭证不可见，以及取消/超时后无新增遗留容器。网络测试使用 TEST-NET 地址，不依赖公网服务。Engine 测试使用 fake Provider + 真实 Docker，验证所有现有工具与失败 Recovery，不调用模型。

daemon 或测试镜像不可用时 integration tests 可 Skip；**生产 auto 不允许安全降级**。可以通过 `CLAW_SANDBOX_TEST_IMAGE` 选择可信测试镜像。真实模型与飞书消息链路需另行验证。

本阶段不实现 Kubernetes、gVisor、Firecracker、VM、bwrap、macOS sandbox-exec、网络白名单/credential proxy、SSH/远程 Executor 或长生命周期容器。现有 Executor 接口保留 streaming/cancel 扩展点，后续 Session Sandbox 不要求修改 AgentEngine。
