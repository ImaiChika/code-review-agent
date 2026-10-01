// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
package review

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"code-review-agent/diff"
)

// repo_url 整体审查的采集边界（防止爬取失控）：
//   - 浅克隆 depth=1（只取目标快照，历史不拉取）
//   - 跳过 vendored/构建产物目录与二进制文件
//   - 文件数与单文件大小上限（超限给可读错误）
const (
	repoMaxFiles     = 1000
	repoMaxFileBytes = 1 << 20 // 1MB/文件
	cloneTimeout     = 5 * time.Minute
)

var repoSkipDirs = map[string]bool{
	".git": true, "vendor": true, "node_modules": true, "dist": true,
	"build": true, "target": true, "__pycache__": true, ".idea": true, ".vscode": true,
}

// cloneRepoForReview 浅克隆远端仓库到临时目录。失败时错误信息已脱敏 token。
// GITHUB_TOKEN 环境变量存在时用于私有仓库鉴权（仅注入 https URL）。
func cloneRepoForReview(repoURL, ref string) (string, error) {
	trimmed := strings.TrimSpace(repoURL)
	cleanURL := strings.TrimSuffix(strings.TrimPrefix(trimmed, "https://"), ".git")
	// 仅 github.com 形态做前缀规范化与 token 注入；其他形态（测试用本地路径）
	// 直接透传给 git
	cloneURL := cleanURL
	if isGitHubPath(cleanURL) {
		cloneURL = "https://github.com/" + cleanURL
		if token := os.Getenv("GITHUB_TOKEN"); token != "" {
			cloneURL = "https://x-access-token:" + token + "@github.com/" + cleanURL
		}
	}

	args := []string{"clone", "--depth", "1", "--single-branch", "--quiet"}
	if ref != "" {
		args = append(args, "--branch", ref)
	}
	args = append(args, cloneURL)

	tmpDir, err := os.MkdirTemp("", "cra-repo-")
	if err != nil {
		return "", fmt.Errorf("%w: 创建临时目录失败: %v", ErrInvalidInput, err)
	}
	args = append(args, tmpDir)

	ctx, cancel := contextWithTimeout(cloneTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		os.RemoveAll(tmpDir)
		msg := redactToken(stderr.String())
		if ctx.Err() != nil {
			return "", fmt.Errorf("%w: 克隆超时（%v）：仓库可能过大或网络不可达", ErrInvalidInput, cloneTimeout)
		}
		if strings.Contains(msg, "not found") || strings.Contains(msg, "does not exist") ||
			strings.Contains(msg, "Repository not found") || strings.Contains(msg, "could not read Username") {
			return "", fmt.Errorf("%w: 仓库不存在或无权访问（私有仓库需服务端配置 GITHUB_TOKEN）", ErrInvalidInput)
		}
		if strings.Contains(msg, "not found in upstream origin") || strings.Contains(msg, "Remote branch") {
			return "", fmt.Errorf("%w: 分支/tag 不存在：%s", ErrInvalidInput, ref)
		}
		return "", fmt.Errorf("%w: 克隆失败: %s", ErrInvalidInput, strings.TrimSpace(msg))
	}
	return tmpDir, nil
}

// isGitHubPath 判断是否 github.com 的 owner/repo 形态。
func isGitHubPath(u string) bool {
	if strings.HasPrefix(u, "github.com/") {
		return true
	}
	return false
}

// redactToken 脱敏克隆 URL 中注入的鉴权 token（绝不落日志/任务记录）。
func redactToken(s string) string {
	if i := strings.Index(s, "x-access-token:"); i >= 0 {
		start := i + len("x-access-token:")
		end := strings.IndexAny(s[start:], "@")
		if end >= 0 {
			return s[:start] + "***" + s[start+end:]
		}
	}
	return s
}

// readFullRepoFiles 收集仓库全部文本文件（跳过 vendored/二进制/超大文件），
// 整体按新增行审查（等价 --files 语义）。
func readFullRepoFiles(root string) ([]diff.FileDiff, error) {
	var paths []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // 单个不可读条目跳过（fail-open）
		}
		name := info.Name()
		if info.IsDir() {
			if repoSkipDirs[name] {
				return filepath.SkipDir
			}
			return nil
		}
		if len(paths) >= repoMaxFiles {
			return fmt.Errorf("仓库文件数超过上限 %d：请改用 PR 链接审查变更，或本地部署后用仓库路径模式", repoMaxFiles)
		}
		if info.Size() > repoMaxFileBytes {
			return nil // 超大文件跳过
		}
		// 二进制嗅探：前 8KB 含 NUL 视为二进制
		if head, err := os.ReadFile(path); err == nil {
			if bytes.IndexByte(head[:min(len(head), 8000)], 0) >= 0 {
				return nil
			}
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("仓库中没有可审查的文本文件（空仓库或全为二进制/跳过目录）")
	}
	return diff.ReadFromFilePaths(paths)
}

func contextWithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}
