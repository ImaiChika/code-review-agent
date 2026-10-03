// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
package rules

import (
	"regexp"
	"strings"

	"code-review-agent/diff"
	"code-review-agent/findings"
)

// ========== CTX-AST-001: context 取消函数泄漏检测（R1，命题点名缺口） ==========
//
// 官方命题方向之一是 "context"（并发 / context / error handling / resource lifecycle），
// 此前 GOR-AST-001 只覆盖 go 语句，context 的取消传播完全没管——本规则补齐。
//
// 检测策略（取竞品调研 #2240 的"变量名级匹配 + 作用域搜索"手法）：
//   - 新增行出现 context.WithCancel / WithTimeout / WithDeadline / WithCancelCause
//   - 提取返回的 cancel 变量名（`x, cancel := context.WithTimeout(...)` 的第二个返回值）
//   - 在本次变更的全部新增行中搜索 cancel( 调用；找不到 → 上报
//
// 已知边界（有意为之）：
//   - cancel 可能在未变更代码中调用（如外层 defer）——hunk 视角不可见，
//     因此命名变体压在 0.75 置信度（medium），丢弃变体（赋给 _）确定性泄漏报 high；
//   - `_` 丢弃变体当前会与 ERR-AST-001 的 `_` 丢弃噪音共报（R3 白名单落地后消解），
//     该形态由单测覆盖，暂不入数据集（见 dataset/cases/positive/r1_ctx_cancel_discard_001.json 注记）。
type TokenContextCancelRule struct{}

// NewTokenContextCancelRule 创建 context 取消泄漏检测规则实例。
func NewTokenContextCancelRule() *TokenContextCancelRule { return &TokenContextCancelRule{} }

func (r *TokenContextCancelRule) ID() string                  { return "CTX-AST-001" }
func (r *TokenContextCancelRule) Name() string                { return "Token 感知的 context 取消泄漏检测" }
func (r *TokenContextCancelRule) Severity() findings.Severity { return findings.SeverityMedium }
func (r *TokenContextCancelRule) Category() findings.Category { return findings.CategoryLifecycle }

// contextConstructors context 构造家族：第二个返回值都是 cancel 函数。
var contextConstructors = []string{
	"context.WithCancel(", "context.WithTimeout(",
	"context.WithDeadline(", "context.WithCancelCause(",
}

// cancelCallRe 校验某行是否真的调用了 cancel（词边界 + 紧跟左括号）。
var cancelCallRe = regexp.MustCompile(`\b[A-Za-z_][A-Za-z0-9_]*\s*\(`)

// collectAddedLines 收集全部新增行（跨 hunk）。
func collectAddedLines(fd diff.FileDiff) []diff.Line {
	var added []diff.Line
	for _, hunk := range fd.Hunks {
		for _, line := range hunk.Lines {
			if line.Type == diff.LineAdded && strings.TrimSpace(line.Content) != "" {
				added = append(added, line)
			}
		}
	}
	return added
}

// isCommentLine 判断是否注释行（行级快速排除）。
func isCommentLine(content string) bool {
	t := strings.TrimSpace(content)
	return strings.HasPrefix(t, "//") || strings.HasPrefix(t, "/*")
}

func (r *TokenContextCancelRule) Check(fd diff.FileDiff) ([]findings.Finding, error) {
	var result []findings.Finding

	// 通用文件门控：本规则检查的是 Go 语义，非 Go 文本（Markdown/模板/配置）不适用。
	if !fd.IsGoFile() {
		return nil, nil
	}
	added := collectAddedLines(fd)

	for _, line := range added {
		content := line.Content
		if isCommentLine(content) {
			continue
		}
		ctorIdx := -1
		for _, ctor := range contextConstructors {
			if i := strings.Index(content, ctor); i >= 0 {
				ctorIdx = i
				break
			}
		}
		if ctorIdx < 0 {
			continue
		}

		cancelVar := extractCancelVar(content[:ctorIdx])
		if cancelVar == "" {
			continue // 解析不出赋值形态（如内联传参），保守跳过
		}

		// 丢弃变体：cancel 赋给 _，确定性泄漏
		if cancelVar == "_" {
			result = append(result, *r.buildFinding(fd, line, cancelVar, findings.SeverityHigh, 0.85,
				"丢弃 cancel（赋给 _）——该 context 只能等父 context 取消才能释放"))
			continue
		}

		// 命名变体：在全部新增行中搜索 cancel( 调用
		if cancelCalled(added, cancelVar) {
			continue
		}
		result = append(result, *r.buildFinding(fd, line, cancelVar, findings.SeverityMedium, 0.75,
			"cancel 未在本次变更内调用——若取消逻辑在未变更代码中请标记误报"))
	}

	return result, nil
}

// extractCancelVar 从调用点之前的文本提取 cancel 变量名（第二个返回值）。
// 形态：`ctx, cancel := ` / `ctx2, cancel = ` / `_, cancel := `。
func extractCancelVar(before string) string {
	assign := strings.LastIndex(before, ":=")
	if assign < 0 {
		assign = strings.LastIndex(before, "=")
	}
	if assign < 0 {
		return ""
	}
	lhs := before[:assign]
	comma := strings.LastIndex(lhs, ",")
	name := strings.TrimSpace(lhs[comma+1:])
	if !cancelCallRe.MatchString(name + "_x(") { // 复用词边界校验：合法标识符形态
		return ""
	}
	return name
}

// cancelCalled 在新增行中查找 cancel 变量的调用。
func cancelCalled(added []diff.Line, cancelVar string) bool {
	callRe := regexp.MustCompile(`\b` + regexp.QuoteMeta(cancelVar) + `\s*\(`)
	for _, line := range added {
		if callRe.MatchString(line.Content) {
			return true
		}
	}
	return false
}

func (r *TokenContextCancelRule) buildFinding(fd diff.FileDiff, line diff.Line, cancelVar string,
	sev findings.Severity, conf float64, why string) *findings.Finding {
	title := "Token 感知：context 的 cancel 可能泄漏"
	f := findings.NewFinding(
		sev, r.Category(), r.ID(),
		title,
		fd.NewPath, line.NewLine,
		line.Content,
		"在合适的时机调用 "+cancelVar+"()（惯例 defer "+cancelVar+"()），否则 context 及其派生 goroutine 要等父 context 取消才能释放；"+why,
		conf,
		"token:context_cancel",
	)
	f.EvidenceChain = findings.BuildEvidenceChain(
		fd.NewPath, line.NewLine, r.ID(),
		"call=context.With*, cancel_var="+cancelVar+", no cancel() call in added lines",
		conf,
	)
	return f
}
