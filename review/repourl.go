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
//
// 文件数上限按输入来源区分信任边界（M9-G5）：repo_url 是用户给的远端地址，
// 爬取必须有硬护栏（1000）；本地仓库全量扫描是用户自己的目录、显式勾选，
// 放宽到 5000——可见性靠分段进度上报与三段耗时保障。
const (
	repoMaxFiles         = 1000
	repoMaxFilesFullScan = 5000
	repoMaxFileBytes     = 1 << 20 // 1MB/文件
	cloneTimeout         = 5 * time.Minute
)

var repoSkipDirs = map[string]bool{
	".git": true, "vendor": true, "node_modules": true, "dist": true,
	"build": true, "target": true, "__pycache__": true, ".idea": true, ".vscode": true,
}

// cloneRepoForReview 浅克隆远端仓库到临时目录。失败时错误信息已脱敏 token。
// GITHUB_TOKEN 环境变量存在时用于私有仓库鉴权（仅注入 https URL）。
func cloneRepoForReview(repoURL, ref string) (string, error) {
	cloneURL := canonicalizeRepoURL(repoURL)

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
			return "", fmt.Errorf("%w: 仓库不存在或无权访问（私有仓库需服务端配置 GITHUB_TOKEN）；git: %s", ErrInvalidInput, summarizeCloneErr(msg))
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

// repoCollectStats 整查采集统计（M9-G4 聚合视图用）：审查覆盖面可解释——
// 跳过了哪些文件、什么原因，而不是一个黑盒数字。
type repoCollectStats struct {
	skipped map[string]int // 原因 → 数量（binary/oversized/unreadable/symlink/skipdir）
	total   int
}

func (s *repoCollectStats) skip(reason string) {
	if s == nil {
		return
	}
	if s.skipped == nil {
		s.skipped = map[string]int{}
	}
	s.skipped[reason]++
	s.total++
}

// collectProgressInterval 大仓库采集分段进度间隔（M9-G5）：每采集 N 个文件
// 上报一次进度，让"整查在跑"对用户可见，而不是像卡死。
const collectProgressInterval = 500

// collectProgress 采集进度上报（stdout 日志；异步模式下落服务端日志可查）。
func collectProgress(collected int) {
	fmt.Printf("📦 采集进度: %d 文件…\n", collected)
}

// readFullRepoFiles 收集仓库全部文本文件（跳过 vendored/二进制/超大文件），
// 整体按新增行审查（等价 --files 语义）。
//
// 采集健壮性（2026-10-03，psf/requests 整仓审查失败教训）：
//   - 符号链接跳过——filepath.Walk 用 Lstat，指向目录的 symlink（如
//     requests tests/certs 里的 ca -> ../../expired/ca/）会被当普通文件
//     采集，读取时 "is a directory" 直接拖垮整个审查；
//   - 单个不可读文件跳过（fail-open）——采集边界内一个坏文件不应让整仓失败。
//
// 返回的 repoCollectStats 记录跳过明细（M9-G4），供整查聚合展示；
// onProgress 非空时每采集 collectProgressInterval 个文件回调一次（M9-G5）。
func readFullRepoFiles(root string, maxFiles int, onProgress func(collected int)) ([]diff.FileDiff, *repoCollectStats, error) {
	stats := &repoCollectStats{}
	var paths []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // 单个不可读条目跳过（fail-open）
		}
		name := info.Name()
		if info.IsDir() {
			if repoSkipDirs[name] {
				stats.skip("skipdir")
				return filepath.SkipDir
			}
			return nil
		}
		// 符号链接不采集（可指向目录或越界路径，审查按真实文件走）
		if info.Mode()&os.ModeSymlink != 0 {
			stats.skip("symlink")
			return nil
		}
		if len(paths) >= maxFiles {
			return fmt.Errorf("仓库文件数超过上限 %d：请改用 PR 链接审查变更，或本地部署后用仓库路径模式", maxFiles)
		}
		if info.Size() > repoMaxFileBytes {
			stats.skip("oversized")
			return nil // 超大文件跳过
		}
		// 只读前 8KB 做二进制嗅探与可读性探测（不再整读文件）
		head := make([]byte, 8000)
		f, ferr := os.Open(path)
		if ferr != nil {
			stats.skip("unreadable")
			return nil // 不可读文件跳过（fail-open）
		}
		n, _ := f.Read(head)
		f.Close()
		if bytes.IndexByte(head[:n], 0) >= 0 {
			stats.skip("binary")
			return nil // 前 8KB 含 NUL 视为二进制
		}
		paths = append(paths, path)
		if onProgress != nil && len(paths)%collectProgressInterval == 0 {
			onProgress(len(paths))
		}
		return nil
	})
	if err != nil {
		return nil, stats, err
	}
	if len(paths) == 0 {
		return nil, stats, fmt.Errorf("仓库中没有可审查的文本文件（空仓库或全为二进制/跳过目录）")
	}
	files, err := diff.ReadFromFilePaths(paths)
	if err != nil {
		return nil, stats, err
	}
	return files, stats, nil
}

func contextWithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

// summarizeCloneErr 截取 git stderr 的最后一行有效信息（脱敏后）用于错误提示。
func summarizeCloneErr(msg string) string {
	msg = redactToken(msg)
	lines := strings.Split(strings.TrimSpace(msg), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		t := strings.TrimSpace(lines[i])
		if t != "" {
			if len(t) > 120 {
				t = t[:120] + "…"
			}
			return t
		}
	}
	return "无 stderr"
}

// canonicalizeRepoURL 规范化仓库链接：
//   - https://github.com/o/r / github.com/o/r / o/r → https://github.com/o/r
//   - 本地路径等非 github 形态原样透传（测试可注入本地 bare remote）
//   - GITHUB_TOKEN 存在时注入 x-access-token 鉴权（仅 github 形态）
func canonicalizeRepoURL(repoURL string) string {
	trimmed := strings.TrimSpace(repoURL)
	// 本地路径（绝对/相对路径形态）/ 其他协议远端原样透传（bare 仓库的
	// .git 后缀是合法远端名）；owner/repo 两段简写仍补全为 github 形态。
	// 区分：本地路径首段不含 '.' 且不是两段形态，或以 / ./ ../ 开头，或含 ://
	if isLocalLikeRemote(trimmed) {
		return trimmed
	}
	trimmed = strings.TrimSuffix(trimmed, ".git")
	trimmed = strings.TrimPrefix(trimmed, "https://")
	trimmed = strings.TrimPrefix(trimmed, "http://")
	trimmed = strings.TrimPrefix(trimmed, "github.com/")
	if !strings.HasPrefix(trimmed, "github.com/") && !strings.Contains(trimmed, "://") && !strings.HasPrefix(trimmed, "/") {
		// owner/repo 简写 → 补全 github 形态
		return "https://github.com/" + trimmed
	}
	if strings.HasPrefix(trimmed, "github.com/") {
		cloneURL := "https://github.com/" + strings.TrimPrefix(trimmed, "github.com/")
		if token := os.Getenv("GITHUB_TOKEN"); token != "" {
			cloneURL = "https://x-access-token:" + token + "@github.com/" + strings.TrimPrefix(trimmed, "github.com/")
		}
		return cloneURL
	}
	return trimmed
}

// isLocalLikeRemote 判断是否本地路径/非 github 远端形态（透传）。
// 透传条件：含协议分隔（file:// 等）、以路径分隔符开头（绝对路径）、
// 以 ./ ../ 开头（相对路径）、或路径中含目录分隔符且首段含 '.' /
// 以 / 结尾的 bare 形态。owner/repo 简写（两段、无点）不透传。
func isLocalLikeRemote(u string) bool {
	// github 形态永远走规范化分支（含 github.com/o/r.git 形态）
	if strings.Contains(u, "github.com") {
		return false
	}
	if strings.Contains(u, "://") {
		return true // 其他协议远端（file:// 等）
	}
	if strings.HasPrefix(u, "/") || strings.HasPrefix(u, "./") || strings.HasPrefix(u, "../") {
		return true
	}
	// 多段 bare 路径（如 /tmp/x/remote.git 已由绝对路径覆盖；
	// 相对多段且 .git 结尾视为本地）
	if strings.HasSuffix(u, ".git") && strings.Count(u, "/") > 1 {
		return true
	}
	return false
}

// relativizeFiles 把 findings/文件路径从仓库根的绝对路径改为相对路径
// （克隆临时目录随机，两轮绝对路径必然失配；展示也以相对路径为准）。
func relativizeFiles(files []diff.FileDiff, root string) {
	prefix := root + string(filepath.Separator)
	for i := range files {
		files[i].NewPath = strings.TrimPrefix(files[i].NewPath, prefix)
		files[i].OldPath = strings.TrimPrefix(files[i].OldPath, prefix)
	}
}
