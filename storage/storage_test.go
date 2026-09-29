// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
package storage

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"code-review-agent/findings"
)

func newTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	tmpDir := t.TempDir()
	store, err := NewSQLiteStore(tmpDir + "/test.db")
	if err != nil {
		t.Fatalf("NewSQLiteStore 失败: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// ========== 接口合规性 ==========

func TestStoreInterface(t *testing.T) {
	// 验证 SQLiteStore 实现了 Store 接口
	var _ Store = (*SQLiteStore)(nil)
}

// ========== 任务管理 ==========

func TestCreateAndGetTask(t *testing.T) {
	store := newTestStore(t)

	task := &ReviewTask{
		TaskID:    "task-001",
		Status:    TaskStatusPending,
		InputType: "diff_file",
		InputPath: "test.diff",
		StartedAt: time.Now(),
	}

	if err := store.CreateTask(task); err != nil {
		t.Fatalf("CreateTask 失败: %v", err)
	}

	got, err := store.GetTask("task-001")
	if err != nil {
		t.Fatalf("GetTask 失败: %v", err)
	}

	if got.TaskID != "task-001" {
		t.Errorf("TaskID = %q, 期望 %q", got.TaskID, "task-001")
	}
	if got.Status != TaskStatusPending {
		t.Errorf("Status = %q, 期望 %q", got.Status, TaskStatusPending)
	}
	if got.InputType != "diff_file" {
		t.Errorf("InputType = %q, 期望 %q", got.InputType, "diff_file")
	}
}

func TestGetTaskNotFound(t *testing.T) {
	store := newTestStore(t)

	_, err := store.GetTask("nonexistent")
	if err == nil {
		t.Error("不存在的任务应返回错误")
	}
}

func TestUpdateTaskStatus(t *testing.T) {
	store := newTestStore(t)

	task := &ReviewTask{
		TaskID:    "task-002",
		Status:    TaskStatusPending,
		InputType: "diff_file",
		InputPath: "test.diff",
		StartedAt: time.Now(),
	}
	store.CreateTask(task)

	// 更新为 running
	if err := store.UpdateTaskStatus("task-002", TaskStatusRunning); err != nil {
		t.Fatalf("UpdateTaskStatus(running) 失败: %v", err)
	}

	got, _ := store.GetTask("task-002")
	if got.Status != TaskStatusRunning {
		t.Errorf("Status = %q, 期望 %q", got.Status, TaskStatusRunning)
	}

	// 更新为 completed
	time.Sleep(10 * time.Millisecond) // 确保有时间差
	if err := store.UpdateTaskStatus("task-002", TaskStatusCompleted); err != nil {
		t.Fatalf("UpdateTaskStatus(completed) 失败: %v", err)
	}

	got, _ = store.GetTask("task-002")
	if got.Status != TaskStatusCompleted {
		t.Errorf("Status = %q, 期望 %q", got.Status, TaskStatusCompleted)
	}
	if got.CompletedAt == nil {
		t.Error("CompletedAt 不应为 nil")
	}
	if got.Duration == "" {
		t.Error("Duration 不应为空")
	}
}

func TestListTasks(t *testing.T) {
	store := newTestStore(t)

	for i := 0; i < 5; i++ {
		store.CreateTask(&ReviewTask{
			TaskID:    "task-" + string(rune('0'+i)),
			Status:    TaskStatusPending,
			InputType: "diff_file",
			InputPath: "test.diff",
			StartedAt: time.Now().Add(time.Duration(i) * time.Minute),
		})
	}

	tasks, err := store.ListTasks(3)
	if err != nil {
		t.Fatalf("ListTasks 失败: %v", err)
	}
	if len(tasks) != 3 {
		t.Errorf("ListTasks(3) 返回 %d 条，期望 3", len(tasks))
	}
}

// ========== 审查发现 ==========

func TestSaveAndGetFindings(t *testing.T) {
	store := newTestStore(t)

	// 先创建任务
	store.CreateTask(&ReviewTask{
		TaskID: "task-f01", Status: TaskStatusPending,
		InputType: "diff_file", InputPath: "test.diff", StartedAt: time.Now(),
	})

	// 保存 findings
	input := []findings.Finding{
		*findings.NewFinding(
			findings.SeverityHigh, findings.CategorySecurity, "SEC-001",
			"硬编码密钥", "config.go", 10,
			`APIKey = "sk-xxx"`, "使用环境变量", 0.95, "rule:sec",
		),
		*findings.NewFinding(
			findings.SeverityMedium, findings.CategoryResource, "RES-001",
			"资源未关闭", "handler.go", 25,
			`f, err := os.Open(path)`, "添加 defer f.Close()", 0.80, "rule:res",
		),
	}

	if err := store.SaveFindings("task-f01", input); err != nil {
		t.Fatalf("SaveFindings 失败: %v", err)
	}

	// 读取 findings
	got, err := store.GetFindings("task-f01")
	if err != nil {
		t.Fatalf("GetFindings 失败: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("GetFindings 返回 %d 条，期望 2", len(got))
	}

	// 验证按严重级别排序（high 在前）
	if got[0].Severity != findings.SeverityHigh {
		t.Errorf("第一条 Severity = %q, 期望 high", got[0].Severity)
	}
	if got[1].Severity != findings.SeverityMedium {
		t.Errorf("第二条 Severity = %q, 期望 medium", got[1].Severity)
	}
}

func TestGetFindingsEmpty(t *testing.T) {
	store := newTestStore(t)

	store.CreateTask(&ReviewTask{
		TaskID: "task-empty", Status: TaskStatusPending,
		InputType: "diff_file", InputPath: "test.diff", StartedAt: time.Now(),
	})

	got, err := store.GetFindings("task-empty")
	if err != nil {
		t.Fatalf("GetFindings 失败: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("GetFindings 返回 %d 条，期望 0", len(got))
	}
}

// ========== 沙箱执行记录 ==========

func TestSaveAndGetSandboxRuns(t *testing.T) {
	store := newTestStore(t)

	store.CreateTask(&ReviewTask{
		TaskID: "task-s01", Status: TaskStatusPending,
		InputType: "diff_file", InputPath: "test.diff", StartedAt: time.Now(),
	})

	run := &SandboxRun{
		TaskID:    "task-s01",
		Command:   "go vet ./...",
		Backend:   "local",
		ExitCode:  0,
		Output:    "ok",
		Truncated: false,
		Duration:  "1.2s",
		StartedAt: time.Now(),
	}

	if err := store.SaveSandboxRun(run); err != nil {
		t.Fatalf("SaveSandboxRun 失败: %v", err)
	}

	runs, err := store.GetSandboxRuns("task-s01")
	if err != nil {
		t.Fatalf("GetSandboxRuns 失败: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("GetSandboxRuns 返回 %d 条，期望 1", len(runs))
	}
	if runs[0].Command != "go vet ./..." {
		t.Errorf("Command = %q, 期望 %q", runs[0].Command, "go vet ./...")
	}
}

// ========== 权限决策记录 ==========

func TestSaveAndGetPermissionDecisions(t *testing.T) {
	store := newTestStore(t)

	store.CreateTask(&ReviewTask{
		TaskID: "task-p01", Status: TaskStatusPending,
		InputType: "diff_file", InputPath: "test.diff", StartedAt: time.Now(),
	})

	decision := &PermissionDecision{
		TaskID:    "task-p01",
		ToolName:  "workspace_exec",
		Command:   "go test ./...",
		Action:    "allow",
		Reason:    "",
		DecidedAt: time.Now(),
	}

	if err := store.SavePermissionDecision(decision); err != nil {
		t.Fatalf("SavePermissionDecision 失败: %v", err)
	}

	decisions, err := store.GetPermissionDecisions("task-p01")
	if err != nil {
		t.Fatalf("GetPermissionDecisions 失败: %v", err)
	}
	if len(decisions) != 1 {
		t.Fatalf("GetPermissionDecisions 返回 %d 条，期望 1", len(decisions))
	}
	if decisions[0].Action != "allow" {
		t.Errorf("Action = %q, 期望 %q", decisions[0].Action, "allow")
	}
}

// ========== 报告 ==========

func TestSaveAndGetReport(t *testing.T) {
	store := newTestStore(t)

	store.CreateTask(&ReviewTask{
		TaskID: "task-r01", Status: TaskStatusPending,
		InputType: "diff_file", InputPath: "test.diff", StartedAt: time.Now(),
	})

	jsonReport := `{"task_id":"task-r01","findings":[]}`
	mdReport := "# 代码审查报告\n\n无问题。"

	if err := store.SaveReport("task-r01", jsonReport, mdReport); err != nil {
		t.Fatalf("SaveReport 失败: %v", err)
	}

	gotJSON, gotMD, err := store.GetReport("task-r01")
	if err != nil {
		t.Fatalf("GetReport 失败: %v", err)
	}
	if gotJSON != jsonReport {
		t.Errorf("JSON 报告不匹配")
	}
	if gotMD != mdReport {
		t.Errorf("MD 报告不匹配")
	}
}

// ========== 辅助方法 ==========

func TestGetTaskSummary(t *testing.T) {
	store := newTestStore(t)

	store.CreateTask(&ReviewTask{
		TaskID: "task-sum", Status: TaskStatusCompleted,
		InputType: "diff_file", InputPath: "test.diff", StartedAt: time.Now(),
	})

	store.SaveFindings("task-sum", []findings.Finding{
		*findings.NewFinding(findings.SeverityHigh, findings.CategorySecurity, "SEC-001", "t", "a.go", 1, "e", "r", 0.9, "s"),
		*findings.NewFinding(findings.SeverityHigh, findings.CategorySecurity, "SEC-002", "t", "b.go", 2, "e", "r", 0.9, "s"),
		*findings.NewFinding(findings.SeverityMedium, findings.CategoryResource, "RES-001", "t", "c.go", 3, "e", "r", 0.8, "s"),
	})

	summary, err := store.GetTaskSummary("task-sum")
	if err != nil {
		t.Fatalf("GetTaskSummary 失败: %v", err)
	}

	if summary["high"] != 2 {
		t.Errorf("high = %v, 期望 2", summary["high"])
	}
	if summary["medium"] != 1 {
		t.Errorf("medium = %v, 期望 1", summary["medium"])
	}
	if summary["total"] != 3 {
		t.Errorf("total = %v, 期望 3", summary["total"])
	}
}

// TestCreateFailedTask M7-F1：异步队列失败的审查需补记失败行（含原因），
// 保证失败历史可按 task_id 查询。
func TestCreateFailedTask(t *testing.T) {
	store := newTestStore(t)

	task := &ReviewTask{
		TaskID:    "task-failed-test-0001",
		Status:    TaskStatusFailed, // CreateFailedTask 内部固定 failed，此处仅为语义完整
		InputType: "diff_content",
		InputPath: "api-upload",
		StartedAt: time.Now().Add(-3 * time.Second),
	}
	if err := store.CreateFailedTask(task, "任务执行超时（上限 10m0s）"); err != nil {
		t.Fatalf("CreateFailedTask 失败: %v", err)
	}

	got, err := store.GetTask("task-failed-test-0001")
	if err != nil {
		t.Fatalf("GetTask 失败: %v", err)
	}
	if got.Status != TaskStatusFailed {
		t.Errorf("status = %v, 期望 failed", got.Status)
	}
	if got.ErrorMsg != "任务执行超时（上限 10m0s）" {
		t.Errorf("error_msg = %q, 期望包含超时原因", got.ErrorMsg)
	}
	if got.CompletedAt == nil {
		t.Error("completed_at 应被填充")
	}
	if got.Duration == "" {
		t.Error("duration 应被填充")
	}
}

// TestGetTrendStats M7-F5：趋势聚合（全量 count/avg/max + 按天 + recent）。
func TestGetTrendStats(t *testing.T) {
	store := newTestStore(t)

	// 3 个任务：两个今天（风险 30/90），一个老任务（11 个月前，不应进 30 天窗口但计入全量）
	old := time.Now().Add(-330 * 24 * time.Hour)
	mk := func(id string, score float64, grade string, started time.Time) {
		task := &ReviewTask{
			TaskID: id, Status: TaskStatusCompleted, InputType: "diff_content",
			InputPath: id, StartedAt: started, RiskScore: score, RiskGrade: grade,
		}
		if err := store.CreateTask(task); err != nil {
			t.Fatalf("CreateTask(%s): %v", id, err)
		}
	}
	mk("task-trend-a", 30, "B", time.Now())
	mk("task-trend-b", 90, "F", time.Now())
	mk("task-trend-old", 10, "A", old)

	ts, err := store.GetTrendStats()
	if err != nil {
		t.Fatalf("GetTrendStats: %v", err)
	}
	if ts.TotalTasks != 3 {
		t.Errorf("TotalTasks = %d, 期望 3（全量，非截断）", ts.TotalTasks)
	}
	wantAvg := (30 + 90 + 10) / 3.0
	if ts.AvgRisk < wantAvg-0.01 || ts.AvgRisk > wantAvg+0.01 {
		t.Errorf("AvgRisk = %.2f, 期望 %.2f", ts.AvgRisk, wantAvg)
	}
	if ts.MaxRisk != 90 {
		t.Errorf("MaxRisk = %.0f, 期望 90", ts.MaxRisk)
	}
	// 30 天窗口只含今天的 2 个任务
	if len(ts.Daily) != 1 || ts.Daily[0].Tasks != 2 {
		t.Errorf("Daily = %+v, 期望 1 天 2 任务（老任务在窗口外）", ts.Daily)
	}
	if n := ts.Daily[0].AvgRisk; n < 59.9 || n > 60.1 {
		t.Errorf("当日 AvgRisk = %.1f, 期望 60", n)
	}
	// Recent 最近 10 个
	if len(ts.Recent) != 3 {
		t.Errorf("Recent = %d 条, 期望 3", len(ts.Recent))
	}
}

// TestMigrateColumns_OldSchema M7-F5：旧 schema（无 risk 列）打开时自动补列。
func TestMigrateColumns_OldSchema(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "old.db")

	// 手工建一个 v1.0 旧 schema 的库 + 一行数据
	oldDB, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = oldDB.Exec(`CREATE TABLE cr_review_tasks (
		task_id TEXT PRIMARY KEY, status TEXT NOT NULL DEFAULT 'pending',
		input_type TEXT NOT NULL, input_path TEXT NOT NULL,
		files_count INTEGER DEFAULT 0, go_files_count INTEGER DEFAULT 0,
		started_at DATETIME NOT NULL, completed_at DATETIME,
		duration TEXT, error_msg TEXT)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = oldDB.Exec(`INSERT INTO cr_review_tasks
		(task_id, status, input_type, input_path, started_at)
		VALUES ('task-old-1', 'completed', 'diff_file', 'x.diff', '2026-09-01 10:00:00')`)
	if err != nil {
		t.Fatal(err)
	}
	oldDB.Close()

	// 新版本打开：迁移应自动补列且旧数据可读
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("打开旧库失败: %v", err)
	}
	defer store.Close()

	task, err := store.GetTask("task-old-1")
	if err != nil {
		t.Fatalf("迁移后旧任务不可读: %v", err)
	}
	if task.RiskScore != 0 || task.RiskGrade != "" {
		t.Errorf("旧数据 risk 默认值应为 0/空, 得到 %.1f/%q", task.RiskScore, task.RiskGrade)
	}

	// 迁移后可写入新列
	nt := &ReviewTask{TaskID: "task-new-2", Status: TaskStatusCompleted, InputType: "diff_content",
		InputPath: "y.diff", StartedAt: time.Now(), RiskScore: 55, RiskGrade: "C"}
	if err := store.CreateTask(nt); err != nil {
		t.Fatalf("迁移后写任务失败: %v", err)
	}
	got, _ := store.GetTask("task-new-2")
	if got.RiskScore != 55 || got.RiskGrade != "C" {
		t.Errorf("迁移后新数据 risk 读写不符: %.1f/%q", got.RiskScore, got.RiskGrade)
	}
}

// TestFalsePositiveMarks M8-C9：误报标记 CRUD（保存/列表/删除）。
func TestFalsePositiveMarks(t *testing.T) {
	store := newTestStore(t)

	m := &FalsePositiveMark{
		RuleID: "SEC-AST-001", FilePath: "creds.go", Line: 3,
		TaskID: "task-fp-1", CreatedAt: time.Now(),
	}
	if err := store.SaveFalsePositiveMark(m); err != nil {
		t.Fatalf("SaveFalsePositiveMark: %v", err)
	}
	if m.ID <= 0 {
		t.Fatal("保存后应回填 ID")
	}

	marks, err := store.ListFalsePositiveMarks()
	if err != nil || len(marks) != 1 {
		t.Fatalf("ListFalsePositiveMarks = %d 项 (err=%v), 期望 1", len(marks), err)
	}
	if marks[0].RuleID != "SEC-AST-001" || marks[0].Line != 3 || marks[0].TaskID != "task-fp-1" {
		t.Errorf("回读字段不符: %+v", marks[0])
	}

	if err := store.DeleteFalsePositiveMark(marks[0].ID); err != nil {
		t.Fatalf("DeleteFalsePositiveMark: %v", err)
	}
	marks, _ = store.ListFalsePositiveMarks()
	if len(marks) != 0 {
		t.Errorf("删除后应剩 0 项, 得到 %d", len(marks))
	}
}
