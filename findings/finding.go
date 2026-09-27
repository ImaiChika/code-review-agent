// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//
// Package findings 定义代码审查结果的数据结构和处理逻辑。
package findings

import (
	"fmt"
	"time"

	"code-review-agent/safety"
)

// Severity 表示问题的严重级别。
type Severity string

const (
	SeverityHigh   Severity = "high"   // 高危：必须修复
	SeverityMedium Severity = "medium" // 中危：建议修复
	SeverityLow    Severity = "low"    // 低危：可以改进
	SeverityInfo   Severity = "info"   // 信息：仅供参考
)

// Category 表示问题的分类。
type Category string

const (
	CategorySecurity      Category = "security"       // 安全风险
	CategoryResource      Category = "resource"       // 资源泄漏（goroutine、文件、连接）
	CategoryErrorHandling Category = "error_handling" // 错误处理
	CategoryTesting       Category = "testing"        // 测试缺失
	CategoryLifecycle     Category = "lifecycle"      // 生命周期问题（DB连接、context）
	CategorySensitiveLeak Category = "sensitive_leak" // 敏感信息泄漏
	CategoryConcurrency   Category = "concurrency"    // 并发问题
	CategoryQuality       Category = "quality"        // 代码质量（M3-D2：staticcheck 等外部工具发现，不参与评分维度）
)

// Finding 表示一次代码审查中发现的一个问题。
type Finding struct {
	// 基本信息
	Severity Severity `json:"severity"` // 严重级别
	Category Category `json:"category"` // 问题分类
	RuleID   string   `json:"rule_id"`  // 规则编号，如 "SEC-001"
	Title    string   `json:"title"`    // 问题标题（一句话）

	// 定位信息
	File   string `json:"file"`             // 文件路径
	Line   int    `json:"line"`             // 行号
	Column int    `json:"column,omitempty"` // 列号（可选）

	// 详情
	Evidence       string   `json:"evidence"`                 // 证据（问题代码片段，已脱敏）
	Recommendation string   `json:"recommendation"`           // 修复建议
	EvidenceChain  []string `json:"evidence_chain,omitempty"` // 证据链（M2：hunk→fact→规则→置信度）

	// 元数据
	Confidence float64 `json:"confidence"` // 置信度 0.0-1.0
	Source     string  `json:"source"`     // 来源，如 "rule:hardcoded_secret"
	Timestamp  string  `json:"timestamp"`  // 发现时间

	// 去重相关（不输出到 JSON，仅内部使用）
	dedupKey string
}

// NewFinding 创建一个新的 Finding，并自动设置时间戳和去重键。
//
// 统一 Redactor 出口（PROJECT_GUIDE §七 M0-A1）：evidence 和 recommendation
// 在进入 findings 之前统一过 safety.MaskSensitiveInfo 脱敏，
// 保证报告和数据库中不出现明文密钥（官方验收标准 6）。
// 规则层可自行做更精细的脱敏（如 sanitizeTokenEvidence），此处是兜底防线。
func NewFinding(severity Severity, category Category, ruleID, title, file string, line int, evidence, recommendation string, confidence float64, source string) *Finding {
	f := &Finding{
		Severity:       severity,
		Category:       category,
		RuleID:         ruleID,
		Title:          title,
		File:           file,
		Line:           line,
		Evidence:       safety.MaskSensitiveInfo(evidence),
		Recommendation: safety.MaskSensitiveInfo(recommendation),
		Confidence:     confidence,
		Source:         source,
		Timestamp:      time.Now().Format(time.RFC3339),
	}
	f.dedupKey = f.DedupKey()
	return f
}

// BuildEvidenceChain 组装证据链（M2）：hunk 定位 → token 事实 → 规则依据 → 置信度。
//
// 链条步骤只包含定位信息、事实类型描述、规则 ID 和置信度，
// 不携带原始代码内容——避免密钥经证据链泄漏（与统一 Redactor 同一纪律）。
func BuildEvidenceChain(file string, line int, ruleID, fact string, confidence float64) []string {
	return []string{
		"hunk: " + fmt.Sprintf("%s@L%d", file, line),
		"fact: " + fact,
		"rule: " + ruleID,
		fmt.Sprintf("confidence: %.2f", confidence),
	}
}

// DedupKey 生成去重键：文件+行号+分类+规则ID。
//
// 同一个文件的同一行、同一类问题只报告一次。
func (f *Finding) DedupKey() string {
	return fmt.Sprintf("%s:%d:%s:%s", f.File, f.Line, f.Category, f.RuleID)
}

// IsHighConfidence 判断是否为高置信度发现。
// 高置信度（>=0.7）进入正式 findings，低置信度进入 warnings。
func (f *Finding) IsHighConfidence() bool {
	return f.Confidence >= 0.7
}

// String 返回一行可读的摘要。
func (f *Finding) String() string {
	return fmt.Sprintf("[%s][%s] %s:%d - %s", f.Severity, f.Category, f.File, f.Line, f.Title)
}

// SeverityOrder 返回严重级别的排序权重（数字越大越严重）。
func (f *Finding) SeverityOrder() int {
	switch f.Severity {
	case SeverityHigh:
		return 4
	case SeverityMedium:
		return 3
	case SeverityLow:
		return 2
	case SeverityInfo:
		return 1
	default:
		return 0
	}
}
