// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
package rules

import (
	"strings"

	"code-review-agent/analyzer"
	"code-review-agent/diff"
	"code-review-agent/findings"
)

// ========== DB-AST-001: Token 感知的 DB 事务生命周期检测（M2-D4） ==========

// TokenDBLifecycleRule 检测新增行中开启却未配对提交/回滚的事务。
//
// 检测策略（M2-D4，补齐官方 7 类规则最后一类）：
//   - 新增行出现 db.Begin( / db.BeginTx( 调用
//   - 且本次变更的全部新增行中找不到 Commit( / Rollback(
//     → 说明本次变更开启了事务，却没有给出任何提交或回滚路径
//
// 配对语义：只要新增行中存在 Commit( 或 Rollback(（含 defer），
// 即视为"已处理"，不上报——见数据集 neg_hard_db_commit_001 / neg_hard_db_rollback_001。
type TokenDBLifecycleRule struct {
	analyzer *analyzer.TokenAnalyzer
}

// NewTokenDBLifecycleRule 创建 DB 事务生命周期检测规则实例。
func NewTokenDBLifecycleRule() *TokenDBLifecycleRule {
	return &TokenDBLifecycleRule{analyzer: analyzer.NewTokenAnalyzer()}
}

func (r *TokenDBLifecycleRule) ID() string                  { return "DB-AST-001" }
func (r *TokenDBLifecycleRule) Name() string                { return "Token 感知的 DB 事务生命周期检测" }
func (r *TokenDBLifecycleRule) Severity() findings.Severity { return findings.SeverityMedium }
func (r *TokenDBLifecycleRule) Category() findings.Category { return findings.CategoryLifecycle }

func (r *TokenDBLifecycleRule) Check(fd diff.FileDiff) ([]findings.Finding, error) {
	var result []findings.Finding

	// 收集全部新增行（跨 hunk：Begin 与 Commit/Rollback 可能分属不同 hunk）
	var added []diff.Line
	for _, hunk := range fd.Hunks {
		for _, line := range hunk.Lines {
			if line.Type == diff.LineAdded && strings.TrimSpace(line.Content) != "" {
				added = append(added, line)
			}
		}
	}

	for _, line := range added {
		content := line.Content
		trimmed := strings.TrimSpace(content)
		if strings.HasPrefix(trimmed, "//") {
			continue
		}

		if !strings.Contains(content, ".Begin(") && !strings.Contains(content, ".BeginTx(") {
			continue
		}

		// 已配对（任一新增行含 Commit/Rollback）则不报
		if hasTxCompletion(added) {
			continue
		}

		f := findings.NewFinding(
			r.Severity(), r.Category(), r.ID(),
			"Token 感知：事务开启后未见 Commit/Rollback",
			fd.NewPath, line.NewLine,
			content,
			"确保事务有明确的提交或回滚路径（tx.Commit / defer tx.Rollback）",
			0.80,
			"token:db_lifecycle",
		)
		f.EvidenceChain = findings.BuildEvidenceChain(
			fd.NewPath, line.NewLine, r.ID(),
			"identifier=tx/tx-like, call=Begin/BeginTx, no Commit/Rollback in added lines",
			0.80,
		)
		result = append(result, *f)
	}

	return result, nil
}

// hasTxCompletion 判断新增行中是否存在事务收尾调用（Commit / Rollback）。
func hasTxCompletion(added []diff.Line) bool {
	for _, line := range added {
		if strings.Contains(line.Content, ".Commit(") || strings.Contains(line.Content, ".Rollback(") {
			return true
		}
	}
	return false
}
