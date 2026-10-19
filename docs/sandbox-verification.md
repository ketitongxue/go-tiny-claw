# Sandbox 人工验收

所有命令从仓库根目录执行。`claw sandbox` 不加载 Provider/Auth/Session、不调用模型；命令仅执行一次，不跨后端重放。期望拒绝的命令应返回非零。不要用真实凭据正文作为测试证据。

## 1. 准备环境

```bash
# 用户明确安装；claw 不会执行安装
npm install -g @anthropic-ai/sandbox-runtime
srt --version
# Linux 另需 bubblewrap/socat/ripgrep、可用的 user/network/PID namespace 与 seccomp
# macOS 需要可用的 sandbox-exec，Node/curl/SRT 依赖须在系统 PATH

docker version
docker build -f Dockerfile.sandbox -t go-tiny-claw-sandbox:latest .
go install ./cmd/claw

ws=$(mktemp -d /tmp/claw-acceptance.XXXXXX)
git init -q "$ws"
printf 'module acceptance\n\ngo 1.26.2\n' > "$ws/go.mod"
printf 'package acceptance\nfunc Sum(a,b int) int { return a+b }\n' > "$ws/sum.go"
printf 'package acceptance\nimport "testing"\nfunc TestSum(t *testing.T) { if Sum(1,2)!=3 { t.Fatal("sum") } }\n' > "$ws/sum_test.go"
```

首次调用 SRT 前（包括 `--version`）必须先检查安装路径与 workspace 不重叠；可执行文件或 npm 包目录位于 workspace、workspace 位于包目录、符号链接别名均不能绕过检查。包名不匹配、安装目录与凭据路径重叠也必须在执行前拒绝。

SRT 不存在、安装路径检查失败、版本报告失败或隔离探测失败时，explicit native 必须报错，不安装、不降级。auto 可以在**构建期**改选满足策略的 Docker。

```bash
# 仅使用临时 fixture；检查拒绝前没有执行恶意 SRT、没有创建工作区外 marker
go test -count=1 -v ./internal/executor -run 'NativeRejects.*BeforeProbes'
go test -count=1 -v ./cmd/claw -run NativeStartupRejectsWorkspaceSRTBeforeExecution
```

## 2. Native / Auto / Docker / Local

```bash
claw sandbox -sandbox=native -workdir "$ws" -command 'pwd; ls; git status; go test ./...'
claw sandbox -sandbox=auto   -workdir "$ws" -command 'pwd; ls; git status; go test ./...'
claw sandbox -sandbox=docker -workdir "$ws" -command 'pwd; ls; git status; go test ./...'
# 仅可信代码：应打印 UNSAFE，网络/文件隔离均不保证
claw sandbox -sandbox=local -workdir "$ws" -command 'echo trusted-local-debug'
```

系统 Go 需位于允许的 `/usr/local/go`；Native 不读取 HOME 下 module/build cache。上面的模块无第三方依赖。真实项目可能需要 workspace vendoring 或显式 proxy.golang.org/sum.golang.org allowlist；需要监听端口、嵌套 sandbox、宿主 API 的测试不一定适合 Native 的默认限制，不应因此放宽 HOME 或开启 weaker sandbox。

如果已经配置模型，可额外启动完整 Agent（需要现有 Provider 凭据，下面不设置/打印任何 token）：

```bash
claw -mode demo -sandbox=native -workdir "$ws" -prompt '用 bash 执行 pwd、ls、git status'
claw -mode demo -sandbox=auto -workdir "$ws" -prompt '用 bash 运行 go test ./...'
claw -mode demo -sandbox=docker -workdir "$ws" -prompt '检查项目并运行测试'
```

## 3. workspace 创建 / 修改 / Explorer

```bash
claw sandbox -sandbox=native -workdir "$ws" -command 'printf first > note; printf second > note; test "$(cat note)" = second'
# 以下必须失败；note 仍为 second
claw sandbox -sandbox=native -policy=explorer -workdir "$ws" -command 'echo forbidden > note'
claw sandbox -sandbox=docker -policy=explorer -workdir "$ws" -command 'echo forbidden > note'
test "$(<"$ws/note")" = second
```

Explorer 仅能写私有 temp，不是宿主文件系统快照。完整 Agent 的 Main 仍有 workspace 文件工具；`-policy=explorer` 是诊断命令的 Bash 策略，不把整个主进程变成 Explorer。

## 4. 真实 HOME / 凭据保护

以下只尝试打开，不输出秘密；`$HOME` 在**宿主 shell**展开成原始路径。若文件原本不存在，失败本身不足以证明隔离，应再运行下面的 fixture contract。

```bash
claw sandbox -sandbox=native -workdir "$ws" -command "cat '$HOME/.claw/auth.json' >/dev/null"
claw sandbox -sandbox=native -workdir "$ws" -command "cat '$HOME/.ssh/id_rsa' >/dev/null"
claw sandbox -sandbox=docker -workdir "$ws" -command "cat '$HOME/.claw/auth.json' >/dev/null"

# 只使用生成的临时秘密，不读取开发机真实凭据：
go test -count=1 -v ./internal/executor -run 'TestSandboxContract/(Native|Docker)/CredentialsAndSymlinks'
go test -count=1 -v ./internal/executor -run 'TestSandboxContract/(Native|Docker)/Environment'
go test -count=1 -v ./internal/executor -run 'SandboxWorkspace|SocketAndDevice'
```

Contract 构造 `.claw/auth.json`、host/session、`.ssh/id_rsa`、`.aws/credentials`、`.kube/config`、独立 CLAW_HOME，以及 workspace symlink；全部不得泄漏。不要把真实密钥复制进 workspace。

## 5. 网络 deny / allowlist / bypass

本地 fixture，不依赖公网。需要 python3，确认 18765 端口空闲：

```bash
python3 -m http.server 18765 --bind 127.0.0.1 --directory "$ws" > "$ws/http-server.log" 2>&1 &
http_pid=$!

# 必须失败
claw sandbox -sandbox=native -workdir "$ws" -command 'curl -f --max-time 2 http://127.0.0.1:18765/go.mod'
# 必须成功，经 SRT proxy
claw sandbox -sandbox=native -workdir "$ws" \
  -sandbox-network=allowlist -sandbox-allowed-domains=127.0.0.1:18765 \
  -command 'curl -f --max-time 2 http://127.0.0.1:18765/go.mod'
# 必须失败，不能绕过 proxy
claw sandbox -sandbox=native -workdir "$ws" \
  -sandbox-network=allowlist -sandbox-allowed-domains=127.0.0.1:18765 \
  -command "curl -f --noproxy '*' --max-time 2 http://127.0.0.1:18765/go.mod"
kill "$http_pid"

go test -count=1 -v ./internal/executor -run 'NativeIntegrationNetworkProxyEnforcement|NetworkPolicySemantics|DockerIntegrationNetworkDenied'
```

Docker + domain allowlist、Native + unrestricted、所有后端 + strict resources AND domain allowlist，均应拒绝，不能弱化语义。

## 6. timeout / cancellation / cleanup

```bash
# 必须超时并停止普通子进程
claw sandbox -sandbox=native -workdir "$ws" -timeout=2s -command 'echo ready; sleep 300 & wait'
claw sandbox -sandbox=docker -workdir "$ws" -timeout=2s -command 'echo ready; sleep 300 & wait'
# 终端看到 ready 后按 Ctrl+C
claw sandbox -sandbox=native -workdir "$ws" -command 'echo ready; sleep 300 & wait'

go test -count=1 -v ./internal/executor -run 'TestSandboxContract/(Native|Docker)/Cancellation'
go test -count=1 -v ./internal/executor -run 'NativeIntegrationRuntimeCleanup|NativeRuntimeStartup|DockerIntegrationCancellation'
docker ps -a --filter label=go-tiny-claw.sandbox=true
```

不要批量删除其他会话的容器/临时目录。清理失败必须报告；SIGKILL/崩溃后的残留需确认归属后人工处理。

## 7. strict 路由

```bash
# 即使 SRT 可用，也应 backend: docker，显示三项强制限制
claw sandbox -sandbox=auto -policy=strict -workdir "$ws" -command 'echo strict'
# 必须拒绝：memory,cpu,pids 不支持
claw sandbox -sandbox=native -policy=strict -workdir "$ws" -command 'echo must-not-run'
# 完整 Agent 的同一应用策略
claw -mode demo -sandbox=auto -sandbox-policy=strict -workdir "$ws" -prompt '用 bash 查看工作区'
```

## 8. 全套验证与性能

```bash
gofmt -w internal/executor cmd/claw/sandbox*.go cmd/sandbox-bench
go vet ./...
go test -count=1 ./...
go test -count=1 -v ./internal/executor -run 'SandboxContract|NativeIntegration|DockerIntegration|Backend|Network|NativeRuntime'
go test -count=1 -v ./cmd/claw -run 'NativeSessionFactoryIntegration|DockerSessionFactoryIntegration'
go run ./cmd/sandbox-bench
```

本次 macOS 使用 SRT 0.0.80 / Node 26.8.2，已实际通过 Native / Docker 全部 contract、Native 工具链、HOME/凭据与 symlink、网络 proxy/bypass、timeout/cancel/child、cleanup，以及 CLI native/auto/docker/local/strict smoke。全仓 uncached tests：711 PASS（含子测试）、1 个原有交互式 OAuth SKIP、0 FAIL；executor 与 claw race 检查通过。

SKIP 必须记录原因：缺 srt、Docker daemon/image 或不支持的 OS。已安装 SRT 但安全探测失败是 FAIL，不是 SKIP。Native/bubblewrap CI 见 `.github/workflows/sandbox.yml`；Linux 仅完成交叉编译，尚未在本次开发会话执行 bubblewrap/CI。Windows Native 明确不可用，不声称验证。

验收后自行删除本次 fixture：`rm -rf "$ws"`（先确认变量确实指向本次临时目录）。
