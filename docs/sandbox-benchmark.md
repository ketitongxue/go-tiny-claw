# Sandbox startup benchmark

运行：

```bash
go run ./cmd/sandbox-bench
# 或独立运行，不会跨后端 fallback：
go run ./cmd/sandbox-bench -sandbox=native
go run ./cmd/sandbox-bench -sandbox=docker
```

使用一次性生成的 Git repo 和无第三方依赖的小 Go module，不读取用户项目/凭据，不访问公网。每后端分别执行 30 次 `echo hello`、30 次 `git status --porcelain`、10 次小型 `go test ./...`。每次独立 sandbox/runtime/container。

测量 `Execute.Result.Duration`（包含 workspace 检查、Docker daemon resource-capability check、runtime/container 创建、进程启动、命令与清理）；构造时 version/image/enforcement preflight 不计入。报告 median（偶数取中间两项平均）与 nearest-rank p95。无预热；每命令新的 HOME/cache，所以 Go 项主要是冷编译，不能视为纯启动 overhead。Docker 镜像已预构建，不含拉取/构建镜像时间。

## 本次实测

环境：本机 Apple Silicon macOS Darwin 27 arm64，Go 1.26.2，Node 26.8.2，SRT 0.0.80，Docker Desktop daemon 29.8.0，已有 `go-tiny-claw-sandbox:latest`（sha256 前缀 `7c836b7a4e51`）。两者网络关闭；Docker 为 1024 MB / 2 CPU / 128 PIDs，Native 不强制这些资源上限。非专用 benchmark 主机，只有一次完整采样，不设 SLA。

| Backend | Command | n | median | p95 |
| --- | --- | ---: | ---: | ---: |
| Native | echo | 30 | 114.27 ms | 117.43 ms |
| Native | git status | 30 | 122.37 ms | 124.76 ms |
| Native | small go test | 10 | 2895.54 ms | 3137.94 ms |
| Docker | echo | 30 | 235.24 ms | 264.23 ms |
| Docker | git status | 30 | 246.81 ms | 298.43 ms |
| Docker | small go test | 10 | 5031.88 ms | 5169.10 ms |

本次 Native 的 echo / git status 中位耗时约为 Docker 的一半。这个结果只代表当前机器与实现（Docker 每命令额外检查 daemon resource capabilities），不是跨平台 SLA。Go 测试还受到 CPU enforcement、冷编译、宿主/容器 SDK 和文件系统差异影响，**不能把全部差值解释为启动开销**。

实测还发现并修复了以下兼容性问题，随后重跑测试与完整采样：

- Docker Desktop bind 的 UID 与 Git dubious ownership：仅信任 `/workspace` 为 Git safe.directory。
- Docker `/tmp` noexec 阻止执行 Go test 二进制：GOTMPDIR 使用已可执行的 workspace，没有去掉 noexec 或资源限制。
- macOS 宿主 TMPDIR 过长导致 SRT mux Unix socket `listen EINVAL`：Native runtime 固定使用短 `/tmp` 路径，每次仍是独立 0700 目录，新增长宿主 TMPDIR 回归。
- 系统 curl 与 Apple git launcher：精确允许已审查的公开 SSL config 单文件，并设置指向已允许 SDK 的 DEVELOPER_DIR；没有重新开放 HOME、整个 `/etc` 或 `/var`。
