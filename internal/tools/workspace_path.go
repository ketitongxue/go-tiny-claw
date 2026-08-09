package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func resolveExistingPathInWorkDir(workDir, requestedPath string) (string, error) {
	if filepath.IsAbs(requestedPath) {
		return "", fmt.Errorf("文件路径必须是工作区内的相对路径")
	}

	absWorkDir, realWorkDir, err := canonicalWorkDir(workDir)
	if err != nil {
		return "", err
	}

	candidate := filepath.Join(absWorkDir, requestedPath)
	realPath, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", fmt.Errorf("解析文件真实路径失败: %w", err)
	}
	if err := ensurePathInWorkDir(realWorkDir, realPath, requestedPath); err != nil {
		return "", err
	}

	return realPath, nil
}

// resolvePathForWriteInWorkDir also supports files that do not exist yet. It
// resolves the nearest existing parent so symlinked directories cannot escape
// the workspace before a new file is created.
func resolvePathForWriteInWorkDir(workDir, requestedPath string) (string, error) {
	if filepath.IsAbs(requestedPath) {
		return "", fmt.Errorf("文件路径必须是工作区内的相对路径")
	}

	absWorkDir, realWorkDir, err := canonicalWorkDir(workDir)
	if err != nil {
		return "", err
	}

	candidate := filepath.Clean(filepath.Join(absWorkDir, requestedPath))
	if err := ensurePathInWorkDir(absWorkDir, candidate, requestedPath); err != nil {
		return "", err
	}

	current := candidate
	var missing []string
	for {
		_, statErr := os.Lstat(current)
		if statErr == nil {
			realCurrent, evalErr := filepath.EvalSymlinks(current)
			if evalErr != nil {
				return "", fmt.Errorf("解析工作区路径失败: %w", evalErr)
			}
			if err := ensurePathInWorkDir(realWorkDir, realCurrent, requestedPath); err != nil {
				return "", err
			}
			if len(missing) > 0 {
				resolvedInfo, infoErr := os.Stat(realCurrent)
				if infoErr != nil {
					return "", fmt.Errorf("校验父目录失败: %w", infoErr)
				}
				if !resolvedInfo.IsDir() {
					return "", fmt.Errorf("文件路径的父级不是目录: %s", requestedPath)
				}
			}

			resolved := realCurrent
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return resolved, nil
		}
		if !os.IsNotExist(statErr) {
			return "", fmt.Errorf("检查文件路径失败: %w", statErr)
		}

		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("无法解析文件路径: %s", requestedPath)
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

func canonicalWorkDir(workDir string) (string, string, error) {
	absWorkDir, err := filepath.Abs(workDir)
	if err != nil {
		return "", "", fmt.Errorf("解析工作区失败: %w", err)
	}
	realWorkDir, err := filepath.EvalSymlinks(absWorkDir)
	if err != nil {
		return "", "", fmt.Errorf("解析工作区真实路径失败: %w", err)
	}
	return absWorkDir, realWorkDir, nil
}

func ensurePathInWorkDir(workDir, candidate, requestedPath string) error {
	relativePath, err := filepath.Rel(workDir, candidate)
	if err != nil {
		return fmt.Errorf("校验文件路径失败: %w", err)
	}
	if relativePath == ".." || strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) || filepath.IsAbs(relativePath) {
		return fmt.Errorf("文件路径超出工作区: %s", requestedPath)
	}
	return nil
}
