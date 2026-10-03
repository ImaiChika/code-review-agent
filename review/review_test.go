// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
package review

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code-review-agent/diff"
	"code-review-agent/report"
	"code-review-agent/storage"
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

// TestRun_TaskRowCompletionMetadata 验证任务行的 completed_at/duration 被填充
// （P3-13 修复：此前主流程只写 status=completed，前端任务详情"结束"列恒为空）。
func TestRun_TaskRowCompletionMetadata(t *testing.T) {
	outDir := t.TempDir()
	dbPath := filepath.Join(outDir, "review.db")
	rep, err := Run(Options{
		DiffContent: "--- a/a.go\n+++ b/a.go\n@@ -1,1 +1,2 @@\n package a\n+var x = 1\n",
		OutputDir:   outDir,
		DBPath:      dbPath,
		SandboxMode: SandboxOff,
	})
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	store, err := storage.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer store.Close()
	task, err := store.GetTask(rep.TaskID)
	if err != nil {
		t.Fatalf("GetTask 失败: %v", err)
	}
	if task.CompletedAt == nil {
		t.Error("completed_at 未填充：任务详情\"结束\"列将为空")
	}
	if task.Duration == "" {
		t.Error("duration 未填充")
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

// TestRun_FileContents M7-F3：内存文件内容输入（上传/粘贴）端到端冒烟。
func TestRun_FileContents(t *testing.T) {
	outDir := t.TempDir()
	rep, err := Run(Options{
		FileContents: []diff.NamedContent{
			{Name: "leak.go", Content: "package leak\n\nvar apiKey = \"sk-live-q1w2e3r4t5y6u7i8o9p0\"\n"},
		},
		OutputDir:   outDir,
		SandboxMode: SandboxOff,
		DryRun:      true,
	})
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if rep.InputType != "file_contents" {
		t.Errorf("input_type = %q, 期望 file_contents", rep.InputType)
	}
	found := false
	for _, f := range rep.Findings {
		if f.RuleID == "SEC-AST-001" && f.File == "leak.go" {
			found = true
		}
	}
	if !found {
		t.Error("粘贴代码中的硬编码密钥应被 SEC-AST-001 检出")
	}
}

// TestRun_FakeModelSuggestions M8-C3 管线级：复核轮 + 建议轮两段响应，
// LLM 建议替换静态 recommendation 且脱敏兜底生效、Monitor 计数正确。
func TestRun_FakeModelSuggestions(t *testing.T) {
	outDir := t.TempDir()
	rep, err := Run(Options{
		DiffFile:    "../testdata/security_issue.diff",
		OutputDir:   outDir,
		SandboxMode: SandboxOff,
		LLMMode:     "fake",
		LLMFakeResponses: []string{
			"1. CONFIRM: ok\n2. CONFIRM: ok\n", // 复核轮：全确认（样本产出 2 条）
			"1. 用 os.Getenv 注入密钥并接入密钥管理服务，轮换已泄漏的 key\n2. 改用环境变量读取并轮换凭据，密钥管理服务统一托管\n", // 建议轮
		},
	})
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if rep.Monitor.LLMSuggested != len(rep.Findings) {
		t.Errorf("LLMSuggested = %d, findings = %d（两条都应被替换）",
			rep.Monitor.LLMSuggested, len(rep.Findings))
	}
	for _, f := range rep.Findings {
		// 静态原文案是「使用环境变量或密钥管理服务」——替换后应指向轮换凭据
		if !strings.Contains(f.Recommendation, "轮换") {
			t.Errorf("finding %s 建议应被 LLM 文案替换: %q", f.RuleID, f.Recommendation)
		}
		if strings.Contains(f.Recommendation, "sk-") {
			t.Errorf("建议不应含明文密钥: %q", f.Recommendation)
		}
	}
}

// TestRun_FakeModelSuggestionRoundDefault M8-C3：只给复核轮响应（建议轮空队列
// 默认回放空响应）→ 静态建议保留、LLMSuggested=0，recall 不降。
func TestRun_FakeModelSuggestionRoundDefault(t *testing.T) {
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
	if rep.Monitor.LLMSuggested != 0 {
		t.Errorf("默认建议轮应无建议, LLMSuggested = %d", rep.Monitor.LLMSuggested)
	}
	if len(rep.Findings) == 0 {
		t.Error("recall 不应下降")
	}
	for _, f := range rep.Findings {
		if f.Recommendation == "" {
			t.Errorf("finding %s 应保留静态建议", f.RuleID)
		}
	}
}

// TestRun_FPMemoryDowngrade M8-C9 管线级闭环：预先落一条误报标记，
// 重审同一 diff → 同位置问题 confidence 降级并进入 warnings（不再占用 findings）。
func TestRun_FPMemoryDowngrade(t *testing.T) {
	outDir := t.TempDir()
	dbPath := filepath.Join(outDir, "review.db")

	// 基线：security_issue.diff 产出 2 条 SEC-AST-001 findings
	// 注：非 dry-run——记忆降噪在 dry-run 下不读库（TestRun_DiffFileDryRun 守护该语义）
	base, err := Run(Options{
		DiffFile:  "../testdata/security_issue.diff",
		OutputDir: t.TempDir(), DBPath: dbPath, SandboxMode: SandboxOff,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(base.Findings) == 0 {
		t.Fatal("基线应有 findings")
	}
	target := base.Findings[0]

	// 落误报标记（rule + file + line 精确匹配）
	store, err := storage.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveFalsePositiveMark(&storage.FalsePositiveMark{
		RuleID: target.RuleID, FilePath: target.File, Line: target.Line,
		TaskID: "task-manual", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	store.Close()

	// 重审：同位置问题应降级进 warnings
	after, err := Run(Options{
		DiffFile:  "../testdata/security_issue.diff",
		OutputDir: t.TempDir(), DBPath: dbPath, SandboxMode: SandboxOff,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range after.Findings {
		if f.RuleID == target.RuleID && f.File == target.File && f.Line == target.Line {
			t.Errorf("标记过的位置不应再出现在 findings: %s %s:%d", f.RuleID, f.File, f.Line)
		}
	}
	found := false
	for _, w := range after.Warnings {
		if w.RuleID == target.RuleID && w.File == target.File && w.Line == target.Line {
			found = true
			if w.Confidence >= 0.7 {
				t.Errorf("降级后 confidence 应 < 0.7, 得到 %.2f", w.Confidence)
			}
			if w.Confidence >= target.Confidence {
				t.Errorf("降级后 confidence 应低于原值 %.2f, 得到 %.2f", target.Confidence, w.Confidence)
			}
		}
	}
	if !found {
		t.Error("标记位置应出现在 warnings（降级不删除，人工仍可复核）")
	}
}

// TestRun_TaskNameAndLLMModel 任务名称落库/进报告，Monitor 记录实际模型名
// （"openai" 只是协议名，用户关心的是 qwen3.8-flash 这类真实模型）。
func TestRun_TaskNameAndLLMModel(t *testing.T) {
	outDir := t.TempDir()
	dbPath := filepath.Join(outDir, "review.db")
	rep, err := Run(Options{
		DiffContent: "--- a/a.go\n+++ b/a.go\n@@ -1,1 +1,3 @@\n package a\n+var password = \"secret-xyz\"\n+var other = 1\n",
		TaskName:    "登录模块安全检查",
		LLMMode:     "fake",
		OutputDir:   outDir,
		DBPath:      dbPath,
		SandboxMode: SandboxOff,
	})
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if rep.TaskName != "登录模块安全检查" {
		t.Errorf("报告 TaskName = %q", rep.TaskName)
	}
	if rep.Monitor.LLMModel != "fake（确定性回放）" {
		t.Errorf("Monitor.LLMModel = %q, 期望 fake 标注", rep.Monitor.LLMModel)
	}

	store, err := storage.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer store.Close()
	task, err := store.GetTask(rep.TaskID)
	if err != nil {
		t.Fatalf("GetTask 失败: %v", err)
	}
	if task.TaskName != "登录模块安全检查" {
		t.Errorf("任务行 TaskName = %q", task.TaskName)
	}
}

// TestRun_RepoTypes_TypeAwareExemption D3 集成：repo 模式下类型信息让 ERR 规则
// "从猜变知道"——非 error 位置的 _ 丢弃（int64/bool）豁免，error 位置保持检出。
func TestRun_RepoTypes_TypeAwareExemption(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		// 静默执行 git 命令
		out, err := exec.Command("git", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v 失败: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", dir)
	run("-C", dir, "config", "user.email", "t@t.co")
	run("-C", dir, "config", "user.name", "t")

	base := `package app

import "os"

func sizes() (int64, bool) { return 0, false }

func doErr2() (string, error) { return "", nil }

func existing() error {
	f, err := os.Open("x")
	if err != nil {
		return err
	}
	return f.Close()
}
`
	if err := os.WriteFile(filepath.Join(dir, "util.go"), []byte(base), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module d3app\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	run("-C", dir, "add", "-A")
	run("-C", dir, "commit", "-qm", "base")

	// 未暂存变更：非 error 丢弃（18 行，bool 位）+ error 丢弃（19 行，末位 error）
	// 注：载体用混合形态（n, _ :=）——显式全丢弃（_ = f）2026-10-03 起整体降级
	// warnings，不再进入类型层判定路径。
	changed := base + `
func more() {
	n, _ := sizes()
	s, _ := doErr2()
}
`
	if err := os.WriteFile(filepath.Join(dir, "util.go"), []byte(changed), 0644); err != nil {
		t.Fatal(err)
	}

	rep, err := Run(Options{
		RepoPath:    dir,
		DBPath:      filepath.Join(t.TempDir(), "review.db"),
		OutputDir:   t.TempDir(),
		SandboxMode: SandboxOff,
	})
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}

	errHits := map[int]bool{}
	for _, f := range rep.Findings {
		if f.RuleID == "ERR-AST-001" && strings.HasSuffix(f.File, "util.go") {
			errHits[f.Line] = true
		}
	}
	if errHits[19] != true {
		t.Errorf("error 位置的 _ 丢弃应检出（util.go:19），实际: %v", errHits)
	}
	if errHits[18] == true {
		t.Errorf("非 error 位置的 _ 丢弃应被类型信息豁免（util.go:18 int64/bool）: %v", errHits)
	}
	// RES 基线：existing() 里 f.Close() 已存在，不应误报
	for _, f := range rep.Findings {
		if f.RuleID == "RES-AST-001" {
			t.Errorf("existing() 的 os.Open 已 Close，不应报 RES: %+v", f)
		}
	}
}

// TestRun_RepoTypes_FailOpen 导入不可解析（外部模块缺失）时类型信息降级：
// 该文件类型查找返回未知 → 规则退回词法行为（不静默丢检出）。
// 注：纯 stdlib 包即使无 go.mod 也能解析类型（source importer 走 GOROOT），
// 因此降级验证必须用不可解析的外部导入。
func TestRun_RepoTypes_FailOpen(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v 失败: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", dir)
	run("-C", dir, "config", "user.email", "t@t.co")
	run("-C", dir, "config", "user.name", "t")

	base := "package app\n\nimport ghost \"ghost.invalid/ghost\"\n\nfunc sizes() (int64, bool) { return 0, false }\n"
	if err := os.WriteFile(filepath.Join(dir, "util.go"), []byte(base), 0644); err != nil {
		t.Fatal(err)
	}
	run("-C", dir, "add", "-A")
	run("-C", dir, "commit", "-qm", "base")

	// 导入不可解析：追加调用 ghost 包（类型未知 → 词法行为）与真实类型已知调用
	// （混合形态载体：显式全丢弃 2026-10-03 起降级 warnings，不再走类型层路径）
	changed := base + "\nfunc more() {\n\tg, _ := ghost.Do()\n\tn, _ := sizes()\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "util.go"), []byte(changed), 0644); err != nil {
		t.Fatal(err)
	}

	rep, err := Run(Options{
		RepoPath:    dir,
		DBPath:      filepath.Join(t.TempDir(), "review.db"),
		OutputDir:   t.TempDir(),
		SandboxMode: SandboxOff,
	})
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	// fail-open：ghost.Do 类型未知 → 词法行为保持（上报）；而同文件的
	// `_ = sizes()`（int64 已知非 error）被类型信息豁免——降级与增强并存
	var ghostFlagged, sizesExempt bool
	for _, f := range rep.Findings {
		if f.RuleID == "ERR-AST-001" && strings.HasSuffix(f.File, "util.go") {
			if f.Line == 8 {
				ghostFlagged = true
			}
			if f.Line == 9 {
				sizesExempt = false
			}
		}
	}
	_ = sizesExempt
	if !ghostFlagged {
		t.Error("类型未知时词法行为应保持（ghost.Do 的 _ 丢弃按旧语义上报）")
	}
}

// TestRun_RepoIncrementalDiff D7 集成：同仓库两轮审查的增量对比——
// 第二轮相对第一轮：新增/复发/已消失 各就各位。
func TestRun_RepoIncrementalDiff(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v 失败: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", dir)
	run("-C", dir, "config", "user.email", "t@t.co")
	run("-C", dir, "config", "user.name", "t")

	dbPath := filepath.Join(t.TempDir(), "review.db")
	outDir := t.TempDir()

	// ── 第一轮：两个问题（硬编码密码 + 忽略错误）──
	v1 := `package app

func auth() string {
	password := "hunter2-v1"
	return password
}

func write() {
	n, _ := writeAll(nil, nil)
}
`
	// writeAll 未定义会类型检查失败——D3 类型加载 fail-open，词法行为保留，无妨
	if err := os.WriteFile(filepath.Join(dir, "app.go"), []byte(v1), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module inc\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// 基线只提交 go.mod；app.go 用 intent-to-add（git diff 可见未提交新增行）
	run("-C", dir, "add", "go.mod")
	run("-C", dir, "commit", "-qm", "base")
	run("-C", dir, "add", "-N", "app.go")
	rep1, err := Run(Options{RepoPath: dir, DBPath: dbPath, OutputDir: outDir, SandboxMode: SandboxOff})
	if err != nil {
		t.Fatalf("第一轮失败: %v", err)
	}
	if rep1.Incremental != nil {
		t.Fatal("第一轮不应有增量对比（没有上一次）")
	}

	// ── 第二轮：修复密码（已消失）、保留忽略错误（复发）、新增 SQL 拼接（新增）──
	v2 := `package app

func auth() string {
	return "from-config"
}

func write() {
	n, _ := writeAll(nil, nil)
}

func query(name string) string {
	return "SELECT * FROM users WHERE name = '" + name + "'"
}
`
	if err := os.WriteFile(filepath.Join(dir, "app.go"), []byte(v2), 0644); err != nil {
		t.Fatal(err)
	}

	rep2, err := Run(Options{RepoPath: dir, DBPath: dbPath, OutputDir: outDir, SandboxMode: SandboxOff})
	if err != nil {
		t.Fatalf("第二轮失败: %v", err)
	}
	inc := rep2.Incremental
	if inc == nil {
		t.Fatalf("第二轮应有增量对比")
	}
	if inc.BaseTaskID != rep1.TaskID {
		t.Errorf("对比基准应为第一轮任务 %s, got %s", rep1.TaskID, inc.BaseTaskID)
	}
	has := func(list []report.FindingRef, rule string) bool {
		for _, f := range list {
			if f.RuleID == rule {
				return true
			}
		}
		return false
	}
	// 新增：SQL 拼接（第二轮新写法）
	if !has(inc.NewFindings, "SEC-AST-003") {
		t.Errorf("SEC-AST-003 应为新增: %+v", inc.NewFindings)
	}
	// 已消失：第一轮的硬编码密码（SEC-AST-001）被修复
	if !has(inc.GoneFindings, "SEC-AST-001") {
		t.Errorf("SEC-AST-001 应为已消失: %+v", inc.GoneFindings)
	}
	// 复发：writeAll 的忽略错误两轮都有
	if !has(inc.RecurFindings, "ERR-AST-001") {
		t.Errorf("ERR-AST-001 应为复发: 新增%+v 复发%+v", inc.NewFindings, inc.RecurFindings)
	}
}

// TestRun_RepoURL_CloneAndReview D3+D7 串测：远端仓库（本地 bare remote 模拟）
// 克隆 → 全文件整体审查 → 第二轮增量对比（新增/已消失）。
func TestRun_RepoURL_CloneAndReview(t *testing.T) {
	base := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v 失败: %v\n%s", args, err, out)
		}
	}
	// 源仓库（普通）→ 推到 bare remote（模拟远端）
	src := filepath.Join(base, "src")
	os.MkdirAll(src, 0755)
	run("init", "-q", "-b", "main", src) // 显式分支名：CI 上 git 默认 master 会让 push HEAD:main 与 remote HEAD 错位
	run("-C", src, "config", "user.email", "t@t.co")
	run("-C", src, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(src, "go.mod"), []byte("module repourl\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	v1 := `package app

func auth() string {
	password := "hunter2-url-v1"
	return password
}
`
	if err := os.WriteFile(filepath.Join(src, "app.go"), []byte(v1), 0644); err != nil {
		t.Fatal(err)
	}
	run("-C", src, "add", "-A")
	run("-C", src, "commit", "-qm", "v1")
	remote := filepath.Join(base, "remote.git")
	run("clone", "-q", "--bare", src, remote)

	dbPath := filepath.Join(base, "review.db")
	outDir := t.TempDir()

	// ── 第一轮：克隆 + 整体审查 ──
	rep1, err := Run(Options{
		RepoURL:     remote, // 本地路径作为远端（git clone 支持任意 URL）
		DBPath:      dbPath,
		OutputDir:   outDir,
		SandboxMode: SandboxOff,
	})
	if err != nil {
		t.Fatalf("第一轮失败: %v", err)
	}
	if rep1.InputType != "repo_url" {
		t.Errorf("InputType = %q, 期望 repo_url", rep1.InputType)
	}
	hasSecret := false
	for _, f := range rep1.Findings {
		if f.RuleID == "SEC-AST-001" {
			hasSecret = true
		}
	}
	if !hasSecret {
		t.Error("整体审查应检出硬编码密码")
	}

	// ── 第二轮：修复密码 + 新增 SQL 拼接 → 增量对比 ──
	v2 := `package app

func auth() string {
	return "from-config"
}

func query(name string) string {
	return "SELECT * FROM users WHERE name = '" + name + "'"
}
`
	if err := os.WriteFile(filepath.Join(src, "app.go"), []byte(v2), 0644); err != nil {
		t.Fatal(err)
	}
	run("-C", src, "add", "-A")
	run("-C", src, "commit", "-qm", "v2")
	run("-C", src, "push", "-q", remote, "HEAD:refs/heads/main")

	rep2, err := Run(Options{
		RepoURL:     remote,
		DBPath:      dbPath,
		OutputDir:   outDir,
		SandboxMode: SandboxOff,
	})
	if err != nil {
		t.Fatalf("第二轮失败: %v", err)
	}
	inc := rep2.Incremental
	if inc == nil {
		t.Fatalf("第二轮应有增量对比")
	}
	has := func(list []report.FindingRef, rule string) bool {
		for _, f := range list {
			if f.RuleID == rule {
				return true
			}
		}
		return false
	}
	if !has(inc.NewFindings, "SEC-AST-003") {
		t.Errorf("SQL 拼接应为新增: %+v", inc.NewFindings)
	}
	if !has(inc.GoneFindings, "SEC-AST-001") {
		t.Errorf("密码修复应为已消失: %+v", inc.GoneFindings)
	}
}

// TestRedactToken 克隆错误脱敏：token 不落任务记录。
func TestRedactToken(t *testing.T) {
	in := "fatal: could not read Username for 'https://x-access-token:ghp_SUPERSECRET@github.com/owner/repo': terminal prompts disabled"
	out := redactToken(in)
	if strings.Contains(out, "ghp_SUPERSECRET") {
		t.Errorf("token 未脱敏: %q", out)
	}
	if !strings.Contains(out, "***") {
		t.Errorf("应有 *** 占位: %q", out)
	}
}

// TestCanonicalizeRepoURL 仓库链接规范化（R：github.com/github.com 双前缀 bug 回归）。
func TestCanonicalizeRepoURL(t *testing.T) {
	cases := map[string]string{
		"https://github.com/o/r":                 "https://github.com/o/r",
		"github.com/o/r":                         "https://github.com/o/r",
		"github.com/o/r.git":                     "https://github.com/o/r",
		"o/r":                                    "https://github.com/o/r",
		"/var/folders/x/remote.git":              "/var/folders/x/remote.git", // 本地路径透传
		"https://github.com/octocat/Hello-World": "https://github.com/octocat/Hello-World",
	}
	for in, want := range cases {
		if got := canonicalizeRepoURL(in); got != want {
			t.Errorf("canonicalizeRepoURL(%q) = %q, 期望 %q", in, got, want)
		}
	}
	// 双前缀回归：输入已含 github.com/ 时不得再拼一次
	if got := canonicalizeRepoURL("https://github.com/github.com/octocat/Hello-World"); strings.Contains(got, "github.com/github.com") {
		t.Errorf("双前缀回归: %q", got)
	}
}

// TestRun_RepoPathFullScan G1 集成：全量扫描审查全部文件（含已提交内容），
// 与 diff 模式（仅未提交变更）形成对比。
func TestRun_RepoPathFullScan(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v 失败: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", dir)
	run("-C", dir, "config", "user.email", "t@t.co")
	run("-C", dir, "config", "user.name", "t")

	// 已提交文件含硬编码密钥（已提交 = diff 模式看不见，全量扫描能看见）
	base := `package app

func auth() string {
	password := "hunter2-fullscan-v1"
	return password
}
`
	if err := os.WriteFile(filepath.Join(dir, "app.go"), []byte(base), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module fs\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	run("-C", dir, "add", "-A")
	run("-C", dir, "commit", "-qm", "base")

	dbPath := filepath.Join(t.TempDir(), "review.db")

	// 全量扫描：应检出已提交文件中的密钥
	rep, err := Run(Options{
		RepoPath:    dir,
		FullScan:    true,
		DBPath:      dbPath,
		OutputDir:   t.TempDir(),
		SandboxMode: SandboxOff,
	})
	if err != nil {
		t.Fatalf("全量扫描失败: %v", err)
	}
	foundSecret := false
	for _, f := range rep.Findings {
		if f.RuleID == "SEC-AST-001" && strings.Contains(f.File, "app.go") {
			foundSecret = true
		}
	}
	if !foundSecret {
		t.Error("全量扫描应检出已提交文件中的硬编码密钥")
	}
	if !strings.HasSuffix(rep.InputPath, "@full") {
		t.Errorf("全量扫描 input_path 应带 @full 后缀: %q", rep.InputPath)
	}
	if rep.Incremental != nil {
		t.Error("首次全量扫描不应有增量对比")
	}

	// 二次全量扫描（内容未变）→ 增量对比全部复发
	rep2, err := Run(Options{
		RepoPath:    dir,
		FullScan:    true,
		DBPath:      dbPath,
		OutputDir:   t.TempDir(),
		SandboxMode: SandboxOff,
	})
	if err != nil {
		t.Fatalf("二次扫描失败: %v", err)
	}
	if rep2.Incremental == nil {
		t.Fatal("二次全量扫描应有增量对比")
	}
	if len(rep2.Incremental.RecurFindings) == 0 {
		t.Error("内容未变时应全部复发")
	}
	if len(rep2.Incremental.NewFindings) != 0 || len(rep2.Incremental.GoneFindings) != 0 {
		t.Errorf("内容未变不应有新增/消失: %+v", rep2.Incremental)
	}
}
