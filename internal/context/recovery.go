package context

import (
	"fmt"
	"strings"
)

// RecoveryManager 负责在工具执行失败时，根据报错特征分析并注入恢复建议
type RecoveryManager struct{}

func NewRecoveryManager() *RecoveryManager {
	return &RecoveryManager{}
}

// AnalyzeAndInject 接收原始报错，匹配已知特征模式，返回增强后的报错信息
func (rm *RecoveryManager) AnalyzeAndInject(toolName string, rawError string) string {
	var hint string

	// 我们使用相对稳定的英文系统级报错关键字，或者我们自己手写的工具内部固定报错格式
	lowerError := strings.ToLower(rawError)

	switch toolName {
	case "edit_file":
		// 匹配我们在 07 讲中手写的 fuzzyReplace 的固定报错抛出
		if strings.Contains(rawError, "在文件中未找到 old_text") || strings.Contains(rawError, "找不到该代码片段") {
			hint = "你提供的 old_text 与文件当前内容不一致，或者缺少必要的缩进。请先使用 `read_file` 工具重新读取该文件，获取最新、准确的内容后，再重新发起编辑。"
		} else if strings.Contains(rawError, "匹配到了多处") || strings.Contains(rawError, "提供更多上下文") {
			hint = "你的 old_text 不够具体，命中了多个相同代码块。请在 old_text 中增加上下相邻的几行代码，以确保替换的唯一性。"
		}

	case "read_file", "write_file":
		// 匹配 Go 原生 os 包抛出的 POSIX 标准错误
		if strings.Contains(lowerError, "no such file or directory") {
			hint = "路径似乎不正确。请不要凭空猜测，先使用 `bash` 执行 `ls -la` 或 `find . -name` 命令查找正确的目录结构和文件名。"
		} else if strings.Contains(lowerError, "permission denied") {
			hint = "你没有权限操作该文件。请检查工作区限制，或者思考是否需要修改其他文件。"
		}

	case "bash":
		if strings.Contains(lowerError, "command not found") {
			hint = "执行环境未安装该命令。请优先使用已有替代命令；若必须安装依赖，请报告需要更新可信 Sandbox 镜像，不要切换宿主执行或绕过网络限制。"
		} else if strings.Contains(rawError, "超时") || strings.Contains(lowerError, "deadline exceeded") {
			hint = "该命令已因超时终止。请缩小任务范围或拆成更短的命令；不要依赖后台进程，执行环境会在命令结束后清理。"
		} else if strings.Contains(lowerError, "syntax error") {
			hint = "Bash 语法错误。请检查引号转义或特殊字符，确保命令在终端中可直接运行。"
		} else {
			hint = "命令执行失败。请检查退出码、stdout 和 stderr，修正原因后再尝试。若执行环境不可用或权限被拒绝，请报告问题，不要绕过 Sandbox 或切换到宿主执行。"
		}
	}

	// 如果没有匹配到特定特征，原样返回原始错误；
	// 如果匹配到了，拼接成强有力的、带有浓厚“系统指导意味”的行动指南。
	if hint == "" {
		return rawError
	}

	return fmt.Sprintf("%s\n\n[系统救援指南]: %s", rawError, hint)
}
