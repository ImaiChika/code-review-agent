// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
package report

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"code-review-agent/findings"
	"code-review-agent/scoring"
)

func newTestReport() *ReviewReport {
	r := NewReport("test-001", "diff_file", "testdata/security.diff")

	// 模拟审查结果
	f1 := findings.NewFinding(
		findings.SeverityHigh, findings.CategorySecurity, "SEC-001",
		"Hardcoded API key", "config.go", 10,
		`APIKey = "sk-abc123"`, "Use environment variable",
		0.95, "rule:hardcoded_secret",
	)
	f2 := findings.NewFinding(
		findings.SeverityMedium, findings.CategoryResource, "RES-001",
		"Missing defer close", "handler.go", 25,
		`conn, err := db.Conn(ctx)`, "Add defer conn.Close()",
		0.8, "rule:resource_leak",
	)
	f3 := findings.NewFinding(
		findings.SeverityLow, findings.CategoryTesting, "TST-001",
		"Missing test", "util.go", 5,
		`func Helper() string`, "Add TestHelper",
		0.5, "rule:missing_test", // 低置信度 → warnings
	)

	result := findings.Deduplicate([]findings.Finding{*f1, *f2, *f3})
	r.SetResult(result, 3, 3)
	r.Finalize(time.Now().Add(-2 * time.Second))

	return r
}

func TestNewReport(t *testing.T) {
	r := NewReport("task-123", "diff_file", "test.diff")

	if r.TaskID != "task-123" {
		t.Errorf("TaskID = %q, 期望 %q", r.TaskID, "task-123")
	}
	if r.InputType != "diff_file" {
		t.Errorf("InputType = %q, 期望 %q", r.InputType, "diff_file")
	}
	if r.StartTime == "" {
		t.Error("StartTime 不应为空")
	}
}

func TestSetResult(t *testing.T) {
	r := newTestReport()

	if r.Summary.TotalFindings != 2 {
		t.Errorf("TotalFindings = %d, 期望 2", r.Summary.TotalFindings)
	}
	if r.Summary.TotalWarnings != 1 {
		t.Errorf("TotalWarnings = %d, 期望 1", r.Summary.TotalWarnings)
	}
	if r.Summary.BySeverity["high"] != 1 {
		t.Errorf("BySeverity[high] = %d, 期望 1", r.Summary.BySeverity["high"])
	}
	if r.Summary.ByCategory["security"] != 1 {
		t.Errorf("ByCategory[security] = %d, 期望 1", r.Summary.ByCategory["security"])
	}
}

func TestWriteJSON(t *testing.T) {
	r := newTestReport()

	tmpDir := t.TempDir()
	path := tmpDir + "/report.json"

	if err := r.WriteJSON(path); err != nil {
		t.Fatalf("WriteJSON 失败: %v", err)
	}

	// 验证文件存在且是合法 JSON
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 JSON 失败: %v", err)
	}

	var parsed ReviewReport
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("JSON 解析失败: %v", err)
	}

	if parsed.TaskID != "test-001" {
		t.Errorf("JSON TaskID = %q, 期望 %q", parsed.TaskID, "test-001")
	}
	if len(parsed.Findings) != 2 {
		t.Errorf("JSON Findings 数量 = %d, 期望 2", len(parsed.Findings))
	}
}

func TestWriteMarkdown(t *testing.T) {
	r := newTestReport()

	tmpDir := t.TempDir()
	path := tmpDir + "/report.md"

	if err := r.WriteMarkdown(path); err != nil {
		t.Fatalf("WriteMarkdown 失败: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 Markdown 失败: %v", err)
	}

	md := string(data)

	// 验证关键内容
	checks := []string{
		"# 代码审查报告",
		"test-001",
		"Hardcoded API key",
		"SEC-001",
		"config.go",
		"需人工复核",
		"修复建议汇总",
	}

	for _, check := range checks {
		if !strings.Contains(md, check) {
			t.Errorf("Markdown 缺少 %q", check)
		}
	}
}

func TestToMarkdown_NoFindings(t *testing.T) {
	r := NewReport("empty-001", "diff_file", "test.diff")
	r.SetResult(findings.DedupResult{}, 0, 0)
	r.Finalize(time.Now())

	md := r.ToMarkdown()
	if !strings.Contains(md, "未发现高置信度问题") {
		t.Error("无 findings 时应显示 '未发现高置信度问题'")
	}
}

func TestSeverityIcon(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"high", "🔴 high"},
		{"medium", "🟡 medium"},
		{"low", "🔵 low"},
		{"info", "⚪ info"},
		{"other", "other"},
	}

	for _, tt := range tests {
		got := severityIcon(tt.input)
		if got != tt.want {
			t.Errorf("severityIcon(%q) = %q, 期望 %q", tt.input, got, tt.want)
		}
	}
}

// ========== M7-F6：单文件 HTML 报告 ==========

func sampleHTMLReport() *ReviewReport {
	rep := NewReport("task-html-001", "diff_content", "api-upload")
	rep.Summary = Summary{
		TotalFindings: 1, TotalWarnings: 1,
		BySeverity: map[string]int{"high": 1},
	}
	rep.Findings = []findings.Finding{
		{
			Severity: "high", Category: "security", RuleID: "SEC-AST-001",
			Title: "硬编码密钥", File: "creds.go", Line: 3,
			Evidence:       `var apiKey = "sk-***REDACTED***"`,
			Recommendation: "改用环境变量",
			Confidence:     0.9,
		},
	}
	rep.Warnings = []findings.Finding{
		{Severity: "low", Category: "testing", RuleID: "TST-AST-001",
			Title: "缺少测试", File: "creds.go", Line: 3, Confidence: 0.65},
	}
	rep.Monitor.RiskScore = 42
	rep.Monitor.RiskGrade = "C"
	rep.Monitor.RiskBreakdown = map[string]scoring.Dimension{
		"security": {Name: "安全问题", Weight: 0.30, Score: 40},
		"resource": {Name: "资源泄漏", Weight: 0.20, Score: 0},
	}
	return rep
}

func TestToHTML_ContainsCoreSections(t *testing.T) {
	h := sampleHTMLReport().ToHTML()

	checks := []string{
		"task-html-001",    // 任务 ID
		"42",               // 风险分
		"creds.go:3",       // finding 定位
		"SEC-AST-001",      // 规则
		"sk-",              // 脱敏后的证据（占位符被高亮 span 包裹，字面串不连续）
		`class="redacted"`, // 高亮标记存在
		"改用环境变量",           // 建议
		"安全问题",             // 六维名称
		"自动脱敏",             // 页脚声明
		`lang="zh-CN"`,     // 文档结构
	}
	for _, c := range checks {
		if !strings.Contains(h, c) {
			t.Errorf("HTML 应包含 %q", c)
		}
	}
	// 明文密钥不可能出现（输入本身就是脱敏后的，但守住 HTML 层不再引入）
	if strings.Contains(h, "sk-live-") {
		t.Error("HTML 不应包含明文密钥")
	}
}

func TestToHTML_SelfContained(t *testing.T) {
	h := sampleHTMLReport().ToHTML()
	// 自包含：不允许外部资源引用（CDN/外链 src/href）——允许 #锚点和相对无协议文本
	for _, bad := range []string{`src="http`, `href="http`, `src="//`, `href="//`, `@import`} {
		if strings.Contains(h, bad) {
			t.Errorf("HTML 报告应自包含，发现外部引用 %q", bad)
		}
	}
	if !strings.Contains(h, "<style>") || !strings.Contains(h, "<script>") {
		t.Error("CSS/JS 应内联")
	}
}

func TestToHTML_EmptyReport(t *testing.T) {
	rep := NewReport("task-empty", "diff_content", "api-upload")
	rep.Monitor.RiskScore = 0
	rep.Monitor.RiskGrade = "A"
	h := rep.ToHTML()
	if !strings.Contains(h, "本次审查未发现问题") {
		t.Error("空报告应有未发现问题提示")
	}
	if strings.Contains(h, "风险评分维度") {
		t.Error("无 breakdown 时不应渲染维度块")
	}
	if !strings.Contains(h, "A 级") {
		t.Error("应包含等级")
	}
}

func TestToHTML_HTMLCape(t *testing.T) {
	rep := NewReport("task-x", "diff_content", "api-upload")
	rep.Findings = []findings.Finding{
		{Severity: "high", RuleID: "SEC-1", Title: `<script>alert(1)</script>`,
			File: "a.go", Line: 1, Evidence: `var s = "<img src=x onerror=alert(2)>"`},
	}
	h := rep.ToHTML()
	if strings.Contains(h, `<script>alert(1)</script>`) {
		t.Error("标题脚本标签必须被转义")
	}
	if strings.Contains(h, `<img src=x`) {
		t.Error("evidence 必须被转义（无原始 img 标签）")
	}
}
