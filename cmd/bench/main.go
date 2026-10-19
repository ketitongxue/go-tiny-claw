package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/ketitongxue/go-tiny-claw/internal/app"
	"github.com/ketitongxue/go-tiny-claw/internal/config"
	ctxpkg "github.com/ketitongxue/go-tiny-claw/internal/context"
	"github.com/ketitongxue/go-tiny-claw/internal/eval"
	"github.com/ketitongxue/go-tiny-claw/internal/executor"
)

func main() {
	model := flag.String("model", "", "模型 provider/model；默认读取全局 config.json")
	sandbox := flag.String("sandbox", executor.ModeAuto, "执行后端：auto、native、docker、local（不安全）")
	image := flag.String("sandbox-image", executor.DefaultImage, "预先构建的 Sandbox 镜像")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *sandbox == executor.ModeLocal {
		log.Print("WARNING: Benchmark 使用宿主 LocalExecutor（UNSAFE），命令拥有当前用户权限；workdir 不是隔离边界。请勿执行不可信代码。")
	}
	home, err := config.AgentHomeDir()
	if err != nil {
		log.Fatal(err)
	}
	paths, err := config.NewPaths(home)
	if err != nil {
		log.Fatal(err)
	}
	runtime, err := app.Load(paths, os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	ref, legacy, err := config.ResolveSelectedModel(*model, runtime.Config)
	if err != nil {
		log.Fatal(err)
	}
	if legacy {
		log.Printf("WARNING: 未限定的 -model 已弃用；请使用 -model %s", ref)
	}
	if err := paths.ValidateWorkspace("workspace"); err != nil {
		log.Fatal(err)
	}
	_, authInfo, err := runtime.Manager.Inspect(ctx, ref)
	if err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll("workspace", 0o755); err != nil {
		log.Fatal(err)
	}
	exec, err := executor.NewConfigured(ctx, *sandbox, *image, "workspace", executor.CodingPolicy())
	if err != nil {
		log.Fatal(err)
	}

	// 构建一套微型评测集
	testcases := []eval.TestCase{
		{
			ID:   "test_001_edit",
			Name: "测试模糊替换工具的准确性",
			// 准备靶机：生成一个有错误的 json 文件
			SetupScript: `echo '{"name": "tiny-claw", "version": "v1.0.0"}' > config.json`,
			// 考题：要求修改版本号
			TaskPrompt: `当前目录下有一个 config.json。请你使用 edit_file 工具，将其中的 version 从 v1.0.0 改为 v2.0.0。不要做其他多余操作。`,
			// 判卷脚本：使用 grep 检查文件是否包含 v2.0.0
			ValidateScript: `grep '"version": "v2.0.0"' config.json`,
		},
		{
			ID:   "test_002_code_gen",
			Name: "测试代码阅读与创建新文件的综合能力",
			// 准备靶机：生成一个简单的乘法函数
			SetupScript: `echo 'package math\n\nfunc Multiply(a, b int) int {\n\treturn a * b\n}' > math.go`,
			// 考题：要求 Agent 根据刚才的代码，自己去写一份单元测试
			TaskPrompt: `当前目录下有一个 math.go。请你仔细阅读它，然后在同级目录下，帮我写一个规范的单元测试文件 math_test.go，用来测试 Multiply 函数。请务必包含正常的测试用例。`,
			// 判卷脚本：直接运行 go test！如果不通过则直接 0 分。
			ValidateScript: `go mod init bench && go test -v ./...`,
		},
	}

	p, info, err := runtime.Manager.Create(ctx, ref)
	if err != nil {
		log.Fatal(err)
	}
	runner := eval.NewBenchmarkRunner(p, info, string(authInfo.Type), exec)
	runner.Prompts = ctxpkg.PromptService{Resources: ctxpkg.ResourceLoader{Home: paths.Home}, Language: runtime.Config.Language}
	runner.RunSuite(ctx, testcases)
}
