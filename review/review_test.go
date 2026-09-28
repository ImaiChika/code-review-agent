// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
package review

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code-review-agent/report"
)

// TestEnsureOutputDir 验证 --output 目录自动创建（M0-A2，修 P0-2）。
func TestEnsureOutputDir(t *testing.T) {
	t.Run("不存在的多级目录自动创建", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "reports", "2026-09", "25")
		if err := EnsureOutputDir(dir); err != nil {
			t.Fatalf("EnsureOutputDir(%q) 失败: %v", dir, err)
		}
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("目录未创建: %v", err)
		}
		if !info.IsDir() {
			t.Errorf("%s 不是目录", dir)
		}
	})

	t.Run("已存在的目录直接通过", func(t *testing.T) {
		dir := t.TempDir()
		if err := EnsureOutputDir(dir); err != nil {
			t.Errorf("已存在目录应直接通过，得到: %v", err)
		}
	})

	t.Run("路径被普通文件占用时报错", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "occupied.txt")
		if err := os.WriteFile(file, []byte("x"), 0644); err != nil {
			t.Fatalf("准备文件失败: %v", err)
		}
		if err := EnsureOutputDir(file); err == nil {
			t.Error("路径是普通文件时应返回错误")
		}
	})

	t.Run("空字符串视为当前目录不做操作", func(t *testing.T) {
		if err := EnsureOutputDir(""); err != nil {
			t.Errorf("空字符串应直接通过，得到: %v", err)
		}
	})
}

// TestResolveAuditPath 验证 --audit-file 路径解析（M0-A5）。
func TestResolveAuditPath(t *testing.T) {
	tests := []struct {
		name      string
		auditFlag string
		outputDir string
		want      string
	}{
		{"空串禁用", "", ".", ""},
		{"纯文件名落到输出目录", "audit.jsonl", "reports", filepath.Join("reports", "audit.jsonl")},
		{"默认值+默认输出目录", "tool_safety_audit.jsonl", ".", "tool_safety_audit.jsonl"},
		{"显式相对路径原样使用", "logs/audit.jsonl", "reports", "logs/audit.jsonl"},
		{"绝对路径原样使用", "/var/log/audit.jsonl", "reports", "/var/log/audit.jsonl"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveAuditPath(tt.auditFlag, tt.outputDir)
			if got != tt.want {
				t.Errorf("ResolveAuditPath(%q, %q) = %q, 期望 %q", tt.auditFlag, tt.outputDir, got, tt.want)
			}
		})
	}
}

// TestRun_RequiresInput 验证无输入时报错。
func TestRun_RequiresInput(t *testing.T) {
	_, err := Run(Options{})
	if err == nil || !strings.Contains(err.Error(), "必须指定") {
		t.Errorf("无输入应报错，得到: %v", err)
	}
}

// TestRun_DiffFileDryRun 管线冒烟：dry-run 模式跑一个含安全问题的 diff，
// 验证报告结构、评分、脱敏与"不落库不进沙箱"。
func TestRun_DiffFileDryRun(t *testing.T) {
	outDir := t.TempDir()
	rep, err := Run(Options{
		DiffFile:    "../testdata/security_issue.diff",
		OutputDir:   outDir,
		DBPath:      filepath.Join(outDir, "review.db"),
		SandboxMode: SandboxOff,
		DryRun:      true,
	})
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}

	if rep.TaskID == "" {
		t.Error("TaskID 不应为空")
	}
	if rep.InputType != "diff_file" {
		t.Errorf("InputType = %q, 期望 diff_file", rep.InputType)
	}
	if len(rep.Findings) == 0 {
		t.Error("security_issue.diff 应产生 findings")
	}
	if rep.Monitor.RiskScore <= 0 {
		t.Errorf("风险评分应 > 0, 得到 %v", rep.Monitor.RiskScore)
	}
	if rep.Monitor.ToolCallCount != 0 {
		t.Errorf("dry-run 不应执行沙箱, tool_call_count = %d", rep.Monitor.ToolCallCount)
	}
	// M0-A1：报告 evidence 不含明文密钥
	for _, f := range rep.Findings {
		if strings.Contains(f.Evidence, "sk-abc123secretkey2024") {
			t.Errorf("evidence 泄漏明文密钥: %q", f.Evidence)
		}
	}
	// 报告文件应已写出
	if _, err := os.Stat(filepath.Join(outDir, "review_report.json")); err != nil {
		t.Errorf("JSON 报告未生成: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "review_report.md")); err != nil {
		t.Errorf("Markdown 报告未生成: %v", err)
	}
	// dry-run 不应建库
	if _, err := os.Stat(filepath.Join(outDir, "review.db")); !errors.Is(err, os.ErrNotExist) {
		t.Error("dry-run 不应创建数据库文件")
	}
}

// TestRun_DiffContent 管线冒烟：内存 diff 文本输入（HTTP API 的主要路径）。
func TestRun_DiffContent(t *testing.T) {
	outDir := t.TempDir()
	diffContent := `--- a/leak.go
+++ b/leak.go
@@ -1,2 +1,4 @@
 package leak
 
+var apiKey = "sk-live-q1w2e3r4t5y6u7i8o9p0"
+func Unused() {}
`
	rep, err := Run(Options{
		DiffContent: diffContent,
		OutputDir:   outDir,
		DBPath:      filepath.Join(outDir, "review.db"),
		SandboxMode: SandboxOff,
	})
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if rep.InputType != "diff_content" {
		t.Errorf("InputType = %q, 期望 diff_content", rep.InputType)
	}
	found := false
	for _, f := range rep.Findings {
		if f.RuleID == "SEC-AST-001" {
			found = true
		}
		if strings.Contains(f.Evidence, "sk-live-q1w2e3r4t5y6u7i8o9p0") {
			t.Errorf("evidence 泄漏明文密钥: %q", f.Evidence)
		}
	}
	if !found {
		t.Error("硬编码密钥应被 SEC-AST-001 检出")
	}
}

// TestRun_NoChanges 无可审查变更返回 ErrNoChanges。
func TestRun_NoChanges(t *testing.T) {
	outDir := t.TempDir()
	// 非 diff 文本 → 解析出 0 个变更文件
	_, err := Run(Options{
		DiffContent: "plain text, not a diff",
		OutputDir:   outDir,
		SandboxMode: SandboxOff,
	})
	if !errors.Is(err, ErrNoChanges) {
		t.Errorf("无变更应返回 ErrNoChanges, 得到: %v", err)
	}
}

// TestRun_ContextOnlyDiff_NoAddedLines M7-F8（P2-10）：
// 纯上下文 / 纯删除的 diff 能解析出文件但没有新增行，同样返回 ErrNoChanges——
// 规则只扫新增行，与其产出空报告，不如明确告知调用方没有可审查的变更。
func TestRun_ContextOnlyDiff_NoAddedLines(t *testing.T) {
	outDir := t.TempDir()
	cases := map[string]string{
		"纯上下文": "--- a/x.go\n+++ b/x.go\n@@ -1,3 +1,3 @@\n package x\n \n func keep() {}\n",
		"纯删除":  "--- a/x.go\n+++ b/x.go\n@@ -1,3 +1,2 @@\n package x\n \n-func gone() {}\n",
	}
	for name, diffText := range cases {
		_, err := Run(Options{
			DiffContent: diffText,
			OutputDir:   outDir,
			SandboxMode: SandboxOff,
		})
		if !errors.Is(err, ErrNoChanges) {
			t.Errorf("%s diff 应返回 ErrNoChanges, 得到: %v", name, err)
		}
	}
}

// TestRun_InvalidInput M7-F8（P3-12）：输入本身不可用返回 ErrInvalidInput。
func TestRun_InvalidInput(t *testing.T) {
	outDir := t.TempDir()
	cases := map[string]Options{
		"diff 文件不存在": {DiffFile: "/nonexistent/a.diff", OutputDir: outDir, SandboxMode: SandboxOff},
		"仓库路径不存在":    {RepoPath: "/nonexistent/repo/xyz", OutputDir: outDir, SandboxMode: SandboxOff},
	}
	for name, opts := range cases {
		_, err := Run(opts)
		if !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s 应返回 ErrInvalidInput, 得到: %v", name, err)
		}
	}
}

// TestRun_FilesList D5 文件列表输入冒烟：整体按新增行审查。
func TestRun_FilesList(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "leak.go")
	if err := os.WriteFile(f, []byte("package leak\n\nvar apiKey = \"sk-live-q1w2e3r4t5y6u7i8o9p0\"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	outDir := t.TempDir()
	rep, err := Run(Options{
		Files:       []string{f},
		OutputDir:   outDir,
		DBPath:      filepath.Join(outDir, "review.db"),
		SandboxMode: SandboxOff,
	})
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if rep.InputType != "files" {
		t.Errorf("InputType = %q, 期望 files", rep.InputType)
	}
	found := false
	for _, fd := range rep.Findings {
		if fd.RuleID == "SEC-AST-001" {
			found = true
		}
	}
	if !found {
		t.Error("整体新增的硬编码密钥应被 SEC-AST-001 检出")
	}
}

// TestRun_FakeModelConfirmAll C2：--fake-model 默认全确认——全链路跑通、计数正确、recall 不降。
func TestRun_FakeModelConfirmAll(t *testing.T) {
	outDir := t.TempDir()
	rep, err := Run(Options{
		DiffFile:    "../testdata/security_issue.diff",
		OutputDir:   outDir,
		SandboxMode: SandboxOff,
		LLMMode:     "fake",
	})
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if rep.Monitor.LLMMode != "fake" {
		t.Errorf("LLMMode = %q", rep.Monitor.LLMMode)
	}
	if rep.Monitor.LLMReviewed != len(rep.Findings) {
		t.Errorf("送审数 %d 与保留数 %d 不一致（全确认应全保留）",
			rep.Monitor.LLMReviewed, len(rep.Findings))
	}
	if rep.Monitor.LLMDropped != 0 {
		t.Errorf("全确认不应剔除, 得到 %d", rep.Monitor.LLMDropped)
	}
	if len(rep.Findings) == 0 {
		t.Error("应保留规则发现")
	}
}

// TestRun_FakeModelDenyDrops C1 管线级：预设 DENY 响应剔除候选。
func TestRun_FakeModelDenyDrops(t *testing.T) {
	outDir := t.TempDir()
	rep, err := Run(Options{
		DiffFile:    "../testdata/security_issue.diff",
		OutputDir:   outDir,
		SandboxMode: SandboxOff,
		LLMMode:     "fake",
		LLMFakeResponses: []string{
			"1. DENY: fake-复核不成立\n2. DENY: fake-复核不成立\n",
		},
	})
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if len(rep.Findings) != 0 {
		t.Fatalf("全部 DENY 后 findings 应为 0, 得到 %d", len(rep.Findings))
	}
	if rep.Monitor.LLMDropped != 2 {
		t.Errorf("LLMDropped = %d, 期望 2", rep.Monitor.LLMDropped)
	}
	if rep.Monitor.LLMReviewed != 2 {
		t.Errorf("LLMReviewed = %d, 期望 2", rep.Monitor.LLMReviewed)
	}
}

// TestRun_FakeModelDeterministic C2 退出标准：全链路确定性（两次运行结果一致）。
func TestRun_FakeModelDeterministic(t *testing.T) {
	run := func() *report.ReviewReport {
		rep, err := Run(Options{
			DiffFile:    "../testdata/security_issue.diff",
			OutputDir:   t.TempDir(),
			SandboxMode: SandboxOff,
			LLMMode:     "fake",
		})
		if err != nil {
			t.Fatalf("Run 失败: %v", err)
		}
		return rep
	}
	a, b := run(), run()

	if len(a.Findings) != len(b.Findings) || len(a.Warnings) != len(b.Warnings) {
		t.Fatal("两次运行的 findings/warnings 数量不一致")
	}
	for i := range a.Findings {
		if a.Findings[i].RuleID != b.Findings[i].RuleID ||
			a.Findings[i].File != b.Findings[i].File ||
			a.Findings[i].Line != b.Findings[i].Line ||
			a.Findings[i].Evidence != b.Findings[i].Evidence {
			t.Errorf("第 %d 条 finding 不一致", i)
		}
	}
	if a.Monitor.LLMReviewed != b.Monitor.LLMReviewed ||
		a.Monitor.LLMDropped != b.Monitor.LLMDropped {
		t.Error("LLM 复核计数不一致")
	}
}

// TestRun_PresetTaskID M7-F1：异步队列预分配的任务 ID 应透传到报告与落库主键。
func TestRun_PresetTaskID(t *testing.T) {
	outDir := t.TempDir()
	rep, err := Run(Options{
		DiffContent: "--- a/x.go\n+++ b/x.go\n@@ -1,2 +1,4 @@\n package x\n \n+var apiKey = \"sk-live-zz99xx88ww77\"\n+var _ = 1\n",
		TaskID:      "task-preset-id-0001",
		OutputDir:   outDir,
		SandboxMode: SandboxOff,
		DryRun:      true,
	})
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if rep.TaskID != "task-preset-id-0001" {
		t.Errorf("TaskID = %q, 期望透传预分配 ID", rep.TaskID)
	}
}

// TestNewTaskID_Unique M7-F1：同秒内多次生成不冲突（202 响应依赖此唯一性）。
func TestNewTaskID_Unique(t *testing.T) {
	seen := make(map[string]bool, 100)
	for i := 0; i < 100; i++ {
		id := NewTaskID()
		if seen[id] {
			t.Fatalf("task ID 冲突: %s", id)
		}
		seen[id] = true
	}
}
