// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
package report

import (
	"encoding/json"
	"fmt"
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

// ========== M9-G4：整查聚合 ==========

// TestBuildRepoScanAgg 聚合构造：目录归并、密度计算、严重级加权排序。
func TestBuildRepoScanAgg(t *testing.T) {
	paths := []string{
		"internal/server/a.go", "internal/server/b.go", "internal/server/c.go",
		"internal/rules/x.go",
		"main.go",
	}
	fs := []findings.Finding{
		{Severity: findings.SeverityHigh, File: "internal/server/a.go"},
		// b.go：high(5)+low(2)=权重 7，应压过 a.go 的单条 high(5)
		{Severity: findings.SeverityHigh, File: "internal/server/b.go"},
		{Severity: findings.SeverityLow, File: "internal/server/b.go"},
		{Severity: findings.SeverityInfo, File: "internal/rules/x.go"},
	}
	ws := []findings.Finding{
		{Severity: findings.SeverityLow, File: "internal/server/c.go"},
	}

	agg := BuildRepoScanAgg(paths, fs, ws)
	if agg.FilesCollected != 5 {
		t.Errorf("FilesCollected = %d, 期望 5", agg.FilesCollected)
	}
	if len(agg.ByDirectory) != 3 {
		t.Fatalf("目录数 = %d, 期望 3: %+v", len(agg.ByDirectory), agg.ByDirectory)
	}
	// internal/server：3 发现 + 1 警告 = 4 问题 / 3 文件 → 排第一，密度 1.33
	first := agg.ByDirectory[0]
	if first.Dir != "internal/server" {
		t.Errorf("问题最多的目录应排第一，实际 %s", first.Dir)
	}
	if first.Findings != 3 || first.Warnings != 1 {
		t.Errorf("internal/server 统计错误: findings=%d warnings=%d", first.Findings, first.Warnings)
	}
	if first.Density < 1.32 || first.Density > 1.34 {
		t.Errorf("密度 = %v, 期望 1.33", first.Density)
	}
	// Top 文件：b.go（high 5 + low 2 = 权重 7）应压过 a.go 的单条 high（5）
	if len(agg.TopFiles) == 0 || agg.TopFiles[0].File != "internal/server/b.go" {
		t.Errorf("b.go 应排 Top 第一: %+v", agg.TopFiles)
	}
	if agg.TopFiles[0].Findings != 2 || agg.TopFiles[0].Weight != 7 {
		t.Errorf("b.go 统计错误: %+v", agg.TopFiles[0])
	}
	if len(agg.TopFiles) < 2 || agg.TopFiles[1].File != "internal/server/a.go" || agg.TopFiles[1].Weight != 5 {
		t.Errorf("a.go 应排第二（权重 5）: %+v", agg.TopFiles)
	}
}

// TestBuildRepoScanAgg_Caps 容量上限：目录 >20 / 文件 >10 时截断，不刷屏。
func TestBuildRepoScanAgg_Caps(t *testing.T) {
	var paths []string
	var fs []findings.Finding
	for i := 0; i < 30; i++ {
		d := fmt.Sprintf("dir%02d", i)
		paths = append(paths, d+"/f.go")
		fs = append(fs, findings.Finding{Severity: findings.SeverityHigh, File: d + "/f.go"})
	}
	agg := BuildRepoScanAgg(paths, fs, nil)
	if len(agg.ByDirectory) != aggMaxDirs {
		t.Errorf("目录应截断到 %d，实际 %d", aggMaxDirs, len(agg.ByDirectory))
	}
	if len(agg.TopFiles) != aggMaxTopFiles {
		t.Errorf("Top 文件应截断到 %d，实际 %d", aggMaxTopFiles, len(agg.TopFiles))
	}
	// 权重相同时按名字排序保证确定性：dir00 应在目录列表里
	if agg.ByDirectory[0].Dir != "dir00" {
		t.Errorf("同权重应按目录名排序，实际 %s", agg.ByDirectory[0].Dir)
	}
}

// TestBuildRepoScanAgg_OutOfList findings 路径不在采集清单里（如沙箱工具
// 产出路径）也参与统计，且文件数为 0 时密度不除零。
func TestBuildRepoScanAgg_OutOfList(t *testing.T) {
	fs := []findings.Finding{
		{Severity: findings.SeverityHigh, File: "vendor/extra/tool.go"},
	}
	agg := BuildRepoScanAgg([]string{"main.go"}, fs, nil)
	if len(agg.ByDirectory) != 2 {
		t.Fatalf("目录数 = %d, 期望 2（main.go 根目录 + vendor/extra）", len(agg.ByDirectory))
	}
	for _, d := range agg.ByDirectory {
		if d.Dir == "vendor/extra" && (d.Files != 1 || d.Density != 1) {
			t.Errorf("清单外目录密度应按 1 文件兜底: %+v", d)
		}
	}
}

// TestRepoScanReportSections 聚合段三格式同步：JSON 带 repo_scan、
// Markdown 有整查聚合、HTML 有文件风险分布；diff 模式（nil）整块省略。
func TestRepoScanReportSections(t *testing.T) {
	rep := newTestReport()
	rep.RepoScan = &RepoScanAgg{
		FilesCollected: 12,
		FilesSkipped:   3,
		SkipReasons:    map[string]int{"binary": 2, "symlink": 1},
		ByDirectory: []DirStat{
			{Dir: ".", Files: 5, Findings: 1, Warnings: 0, Density: 0.2},
			{Dir: "internal/server", Files: 7, Findings: 1, Warnings: 0, Density: 0.14},
		},
		TopFiles: []FileRisk{{File: "config.go", Findings: 1, Warnings: 0, Weight: 5}},
	}

	// JSON 序列化含 repo_scan
	j, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(j), `"repo_scan"`) || !strings.Contains(string(j), `"files_collected":12`) {
		t.Error("JSON 应含 repo_scan 段")
	}

	// Markdown
	md := rep.ToMarkdown()
	for _, want := range []string{"整查聚合", "采集文件**: 12（跳过 3", "二进制 2", "符号链接 1", "(根目录)", "internal/server", "Top 风险文件"} {
		if !strings.Contains(md, want) {
			t.Errorf("Markdown 缺少 %q", want)
		}
	}

	// HTML
	h := rep.ToHTML()
	for _, want := range []string{"文件风险分布", "跳过 <b>3</b>", "(根目录)", "Top 风险文件"} {
		if !strings.Contains(h, want) {
			t.Errorf("HTML 缺少 %q", want)
		}
	}

	// diff 模式（RepoScan = nil）：三格式整块省略
	rep2 := newTestReport()
	h2 := rep2.ToHTML()
	if strings.Contains(h2, "文件风险分布") {
		t.Error("无 repo_scan 时 HTML 不应渲染分布卡")
	}
	md2 := rep2.ToMarkdown()
	if strings.Contains(md2, "整查聚合") {
		t.Error("无 repo_scan 时 Markdown 不应有整查聚合段")
	}
	j2, _ := json.Marshal(rep2)
	if strings.Contains(string(j2), `"repo_scan"`) {
		t.Error("无 repo_scan 时 JSON 不应有序列化空段")
	}
}
