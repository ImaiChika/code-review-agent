// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//
// Package storage 提供审查任务和结果的持久化存储。
//
// 基于 SQLite 实现，保留接口以便后续切换 SQL 后端。
// 使用 trpc-agent-go 的 session/sqlite 作为底层数据库基础设施。
package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"trpc.group/trpc-go/trpc-agent-go/session/sqlite"

	"code-review-agent/findings"
)

// ========== 接口定义 ==========

// Store 是审查存储的接口。
// 定义了所有需要持久化的操作，方便后续替换实现。
type Store interface {
	// 任务管理
	CreateTask(task *ReviewTask) error
	CreateFailedTask(task *ReviewTask, errMsg string) error
	GetTask(taskID string) (*ReviewTask, error)
	UpdateTaskStatus(taskID string, status TaskStatus) error
	ListTasks(limit int) ([]*ReviewTask, error)

	// 审查发现
	SaveFindings(taskID string, findings []findings.Finding) error
	GetFindings(taskID string) ([]findings.Finding, error)

	// 沙箱执行记录
	SaveSandboxRun(run *SandboxRun) error
	GetSandboxRuns(taskID string) ([]*SandboxRun, error)

	// 权限决策记录
	SavePermissionDecision(decision *PermissionDecision) error
	GetPermissionDecisions(taskID string) ([]*PermissionDecision, error)

	// 报告
	SaveReport(taskID string, jsonReport, mdReport string) error
	GetReport(taskID string) (jsonReport, mdReport string, err error)

	// 产物
	SaveArtifact(artifact *Artifact) error
	GetArtifacts(taskID string) ([]*Artifact, error)

	// 聚合统计
	GetFindingStats() (*FindingStats, error)
	GetTrendStats() (*TrendStats, error)

	// 误报标记（M8-C9：记忆降噪）
	SaveFalsePositiveMark(mark *FalsePositiveMark) error
	ListFalsePositiveMarks() ([]*FalsePositiveMark, error)
	DeleteFalsePositiveMark(id int64) error

	// 运行时设置（M8-设置中心：LLM / E2B 配置，value 可能含密钥，外发前必须脱敏）
	GetSetting(key string) (value string, ok bool, err error)
	SetSetting(key, value string) error
	DeleteSetting(key string) error
	ListSettings() ([]*SettingKV, error)

	// 生命周期
	Close() error
}

// ========== 数据模型 ==========

// TaskStatus 任务状态。
type TaskStatus string

const (
	TaskStatusPending   TaskStatus = "pending"
	TaskStatusRunning   TaskStatus = "running"
	TaskStatusCompleted TaskStatus = "completed"
	TaskStatusFailed    TaskStatus = "failed"
)

// ReviewTask 表示一次审查任务。
type ReviewTask struct {
	TaskID       string     `json:"task_id"`
	Status       TaskStatus `json:"status"`
	TaskName     string     `json:"task_name,omitempty"` // 用户可读的任务名称（可空；空 = 前端显示 InputPath）
	InputType    string     `json:"input_type"`          // diff_file / repo_path / fixture
	InputPath    string     `json:"input_path"`
	FilesCount   int        `json:"files_count"`
	GoFilesCount int        `json:"go_files_count"`
	StartedAt    time.Time  `json:"started_at"`
	CompletedAt  *time.Time `json:"completed_at,omitempty"`
	Duration     string     `json:"duration,omitempty"`
	ErrorMsg     string     `json:"error_msg,omitempty"`
	RiskScore    float64    `json:"risk_score,omitempty"` // M7-F5：风险分冗余（趋势聚合用，免解析报告 JSON）
	RiskGrade    string     `json:"risk_grade,omitempty"` // M7-F5：A-F 等级
}

// TrendDay 单天的趋势数据（M7-F5）。
type TrendDay struct {
	Date    string  `json:"date"`     // YYYY-MM-DD
	Tasks   int     `json:"tasks"`    // 当天任务数
	AvgRisk float64 `json:"avg_risk"` // 当天平均风险分
}

// FalsePositiveMark 人工标记的误报记录（M8-C9）。
// 同一 rule_id + 文件（可含行号）再次报出时，管线自动降置信度进 warnings。
type FalsePositiveMark struct {
	ID        int64     `json:"id"`
	RuleID    string    `json:"rule_id"`
	FilePath  string    `json:"file_path"` // 标记时的完整文件路径（匹配键之一）
	Line      int       `json:"line"`      // 标记时行号（精确匹配键；0 = 仅按文件匹配）
	TaskID    string    `json:"task_id"`   // 标记来源任务
	CreatedAt time.Time `json:"created_at"`
}

// SettingKV 一条运行时设置（key-value，M8-设置中心）。
// value 可能是 API Key 等敏感信息：接口层只回传脱敏提示，禁止原样外发。
type SettingKV struct {
	Key       string    `json:"key"`
	Value     string    `json:"-"`
	UpdatedAt time.Time `json:"updated_at"`
}

// 运行时设置的固定键名（M8-设置中心）。
const (
	SettingLLMProvider = "llm_provider" // [已废弃→迁移进方案] 服务商标签
	SettingLLMBaseURL  = "llm_base_url" // [已废弃→迁移进方案] OpenAI 兼容端点
	SettingLLMModel    = "llm_model"    // [已废弃→迁移进方案] 模型名
	SettingLLMAPIKey   = "llm_api_key"  // [已废弃→迁移进方案] 敏感
	SettingE2BAPIKey   = "e2b_api_key"  // 敏感

	// M8-设置中心 v2：多方案（最多 10 个）
	SettingLLMProfiles = "llm_profiles" // JSON 数组（含密钥，接口层脱敏外发）
	SettingLLMCurrent  = "llm_current"  // 当前方案 ID
)

// TrendStats 趋势聚合（M7-F5，纯 SQL 聚合，任务数无关的 O(1) 响应）。
type TrendStats struct {
	TotalTasks int           `json:"total_tasks"` // 全量任务数（替代旧 stats 的"最近 200 条"失真值）
	AvgRisk    float64       `json:"avg_risk"`    // 全量平均风险分
	MaxRisk    float64       `json:"max_risk"`    // 全量最高风险分
	Daily      []TrendDay    `json:"daily"`       // 最近 30 天按天聚合
	Recent     []*ReviewTask `json:"recent"`      // 最近 10 个任务（含风险分）
}

// SandboxRun 表示一次沙箱执行记录。
type SandboxRun struct {
	TaskID    string    `json:"task_id"`
	Command   string    `json:"command"`
	Backend   string    `json:"backend"` // local / container / e2b
	ExitCode  int       `json:"exit_code"`
	Output    string    `json:"output"`
	Truncated bool      `json:"truncated"`
	Duration  string    `json:"duration"`
	StartedAt time.Time `json:"started_at"`
}

// PermissionDecision 表示一次权限决策记录。
type PermissionDecision struct {
	TaskID    string    `json:"task_id"`
	ToolName  string    `json:"tool_name"`
	Command   string    `json:"command"`
	Action    string    `json:"action"` // allow / deny / ask
	Reason    string    `json:"reason"`
	DecidedAt time.Time `json:"decided_at"`
}

// Artifact 表示审查过程中产生的产物。
type Artifact struct {
	TaskID       string    `json:"task_id"`
	ArtifactType string    `json:"artifact_type"` // report / log / diff / sandbox_output
	FilePath     string    `json:"file_path"`
	Content      string    `json:"content"`
	Size         int       `json:"size"`
	CreatedAt    time.Time `json:"created_at"`
}

// RuleCount 单条规则的命中次数（统计用）。
type RuleCount struct {
	RuleID string `json:"rule_id"`
	Count  int    `json:"count"`
}

// FindingStats 全库 findings 聚合统计（总览页 / GET /api/stats 用）。
type FindingStats struct {
	Total      int            `json:"total"`
	BySeverity map[string]int `json:"by_severity"`
	ByCategory map[string]int `json:"by_category"`
	TopRules   []RuleCount    `json:"top_rules"`
}

// ========== 实现 ==========

// SQLiteStore 基于 SQLite 的存储实现。
type SQLiteStore struct {
	db  *sql.DB
	svc *sqlite.Service // 框架的 session service（用于初始化和基础设施）
}

// NewSQLiteStore 创建一个新的 SQLite 存储实例。
//
// 参数：
//   - dbPath: SQLite 数据库文件路径，如 "./review.db"
func NewSQLiteStore(dbPath string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}

	// 使用框架的 session/sqlite 初始化数据库基础设施
	svc, err := sqlite.NewService(db,
		sqlite.WithTablePrefix("cr_"),
	)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("初始化 session service 失败: %w", err)
	}

	store := &SQLiteStore{db: db, svc: svc}

	// 创建审查专用的表
	if err := store.initTables(); err != nil {
		db.Close()
		return nil, fmt.Errorf("初始化审查表失败: %w", err)
	}

	return store, nil
}

// initTables 创建审查专用的表。
func (s *SQLiteStore) initTables() error {
	tables := []string{
		`CREATE TABLE IF NOT EXISTS cr_review_tasks (
			task_id TEXT PRIMARY KEY,
			status TEXT NOT NULL DEFAULT 'pending',
			task_name TEXT DEFAULT '',
			input_type TEXT NOT NULL,
			input_path TEXT NOT NULL,
			files_count INTEGER DEFAULT 0,
			go_files_count INTEGER DEFAULT 0,
			started_at DATETIME NOT NULL,
			completed_at DATETIME,
			duration TEXT,
			error_msg TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS cr_findings (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			task_id TEXT NOT NULL,
			severity TEXT NOT NULL,
			category TEXT NOT NULL,
			rule_id TEXT NOT NULL,
			title TEXT NOT NULL,
			file_path TEXT NOT NULL,
			line INTEGER NOT NULL,
			evidence TEXT,
			recommendation TEXT,
			confidence REAL,
			source TEXT,
			FOREIGN KEY (task_id) REFERENCES cr_review_tasks(task_id)
		)`,
		`CREATE TABLE IF NOT EXISTS cr_sandbox_runs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			task_id TEXT NOT NULL,
			command TEXT NOT NULL,
			backend TEXT DEFAULT 'local',
			exit_code INTEGER DEFAULT 0,
			output TEXT,
			truncated BOOLEAN DEFAULT 0,
			duration TEXT,
			started_at DATETIME NOT NULL,
			FOREIGN KEY (task_id) REFERENCES cr_review_tasks(task_id)
		)`,
		`CREATE TABLE IF NOT EXISTS cr_permission_decisions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			task_id TEXT NOT NULL,
			tool_name TEXT,
			command TEXT,
			action TEXT NOT NULL,
			reason TEXT,
			decided_at DATETIME NOT NULL,
			FOREIGN KEY (task_id) REFERENCES cr_review_tasks(task_id)
		)`,
		`CREATE TABLE IF NOT EXISTS cr_reports (
			task_id TEXT PRIMARY KEY,
			json_report TEXT,
			md_report TEXT,
			FOREIGN KEY (task_id) REFERENCES cr_review_tasks(task_id)
		)`,
		`CREATE TABLE IF NOT EXISTS cr_false_positive_marks (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			rule_id TEXT NOT NULL,
			file_path TEXT NOT NULL,
			line INTEGER DEFAULT 0,
			task_id TEXT,
			created_at DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS cr_artifacts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			task_id TEXT NOT NULL,
			artifact_type TEXT NOT NULL,
			file_path TEXT,
			content TEXT,
			size INTEGER DEFAULT 0,
			created_at DATETIME NOT NULL,
			FOREIGN KEY (task_id) REFERENCES cr_review_tasks(task_id)
		)`,
		`CREATE TABLE IF NOT EXISTS cr_settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
	}

	for _, table := range tables {
		if _, err := s.db.Exec(table); err != nil {
			return fmt.Errorf("创建表失败: %w", err)
		}
	}

	// 创建索引
	indexes := []string{
		`CREATE INDEX IF NOT EXISTS idx_findings_task ON cr_findings(task_id)`,
		`CREATE INDEX IF NOT EXISTS idx_sandbox_task ON cr_sandbox_runs(task_id)`,
		`CREATE INDEX IF NOT EXISTS idx_permission_task ON cr_permission_decisions(task_id)`,
		`CREATE INDEX IF NOT EXISTS idx_tasks_status ON cr_review_tasks(status)`,
		`CREATE INDEX IF NOT EXISTS idx_artifacts_task ON cr_artifacts(task_id)`,
	}
	for _, idx := range indexes {
		if _, err := s.db.Exec(idx); err != nil {
			return fmt.Errorf("创建索引失败: %w", err)
		}
	}

	// M7-F5：列迁移——旧库文件升级时补 risk_score / risk_grade（SQLite 无 ADD COLUMN IF NOT EXISTS）
	if err := s.migrateColumns(); err != nil {
		return err
	}
	return nil
}

// migrateColumns 幂等列迁移：检查 cr_review_tasks 缺失的列并补齐。
func (s *SQLiteStore) migrateColumns() error {
	existing := map[string]bool{}
	rows, err := s.db.Query(`PRAGMA table_info(cr_review_tasks)`)
	if err != nil {
		return fmt.Errorf("检查表结构失败: %w", err)
	}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notNull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err == nil {
			existing[name] = true
		}
	}
	rows.Close()

	migrations := []struct{ col, ddl string }{
		{"task_name", `ALTER TABLE cr_review_tasks ADD COLUMN task_name TEXT DEFAULT ''`},
		{"risk_score", `ALTER TABLE cr_review_tasks ADD COLUMN risk_score REAL DEFAULT 0`},
		{"risk_grade", `ALTER TABLE cr_review_tasks ADD COLUMN risk_grade TEXT DEFAULT ''`},
	}
	for _, m := range migrations {
		if existing[m.col] {
			continue
		}
		if _, err := s.db.Exec(m.ddl); err != nil {
			return fmt.Errorf("迁移列 %s 失败: %w", m.col, err)
		}
	}
	return nil
}

// Close 关闭存储连接。
func (s *SQLiteStore) Close() error {
	if s.svc != nil {
		return s.svc.Close()
	}
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

// ========== 任务管理 ==========

// CreateTask 创建一个新的审查任务。
func (s *SQLiteStore) CreateTask(task *ReviewTask) error {
	_, err := s.db.Exec(
		`INSERT INTO cr_review_tasks (task_id, status, task_name, input_type, input_path, files_count, go_files_count, started_at, risk_score, risk_grade)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		task.TaskID, task.Status, task.TaskName, task.InputType, task.InputPath,
		task.FilesCount, task.GoFilesCount, task.StartedAt, task.RiskScore, task.RiskGrade,
	)
	return err
}

// CreateFailedTask 创建一条已失败的任务记录（M7-F1 异步队列用）。
// 审查在落库前失败（执行出错/超时）时没有任何任务行，由队列侧补记，
// 保证失败历史与错误原因可按 task_id 查询。
func (s *SQLiteStore) CreateFailedTask(task *ReviewTask, errMsg string) error {
	completedAt := time.Now()
	duration := completedAt.Sub(task.StartedAt).Round(time.Millisecond).String()
	_, err := s.db.Exec(
		`INSERT INTO cr_review_tasks
		   (task_id, status, input_type, input_path, files_count, go_files_count,
		    started_at, completed_at, duration, error_msg)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		task.TaskID, TaskStatusFailed, task.InputType, task.InputPath,
		task.FilesCount, task.GoFilesCount, task.StartedAt, completedAt, duration, errMsg,
	)
	return err
}

// GetTask 获取审查任务。
func (s *SQLiteStore) GetTask(taskID string) (*ReviewTask, error) {
	row := s.db.QueryRow(
		`SELECT task_id, status, task_name, input_type, input_path, files_count, go_files_count,
		        started_at, completed_at, duration, error_msg, risk_score, risk_grade
		 FROM cr_review_tasks WHERE task_id = ?`, taskID,
	)

	var task ReviewTask
	var completedAt sql.NullTime
	var taskName, duration, errorMsg, riskGrade sql.NullString
	var riskScore sql.NullFloat64
	err := row.Scan(
		&task.TaskID, &task.Status, &taskName, &task.InputType, &task.InputPath,
		&task.FilesCount, &task.GoFilesCount,
		&task.StartedAt, &completedAt, &duration, &errorMsg, &riskScore, &riskGrade,
	)
	if err != nil {
		return nil, err
	}
	if taskName.Valid {
		task.TaskName = taskName.String
	}
	if completedAt.Valid {
		task.CompletedAt = &completedAt.Time
	}
	if duration.Valid {
		task.Duration = duration.String
	}
	if errorMsg.Valid {
		task.ErrorMsg = errorMsg.String
	}
	if riskScore.Valid {
		task.RiskScore = riskScore.Float64
	}
	if riskGrade.Valid {
		task.RiskGrade = riskGrade.String
	}
	return &task, nil
}

// UpdateTaskStatus 更新任务状态。
func (s *SQLiteStore) UpdateTaskStatus(taskID string, status TaskStatus) error {
	var completedAt *time.Time
	duration := ""
	if status == TaskStatusCompleted || status == TaskStatusFailed {
		now := time.Now()
		completedAt = &now
		// 计算耗时
		task, err := s.GetTask(taskID)
		if err == nil {
			duration = now.Sub(task.StartedAt).Round(time.Millisecond).String()
		}
	}

	_, err := s.db.Exec(
		`UPDATE cr_review_tasks SET status = ?, completed_at = ?, duration = ? WHERE task_id = ?`,
		status, completedAt, duration, taskID,
	)
	return err
}

// ListTasks 列出最近的审查任务。
func (s *SQLiteStore) ListTasks(limit int) ([]*ReviewTask, error) {
	rows, err := s.db.Query(
		`SELECT task_id, status, task_name, input_type, input_path, files_count, go_files_count,
		        started_at, completed_at, duration, error_msg, risk_score, risk_grade
		 FROM cr_review_tasks ORDER BY started_at DESC LIMIT ?`, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []*ReviewTask
	for rows.Next() {
		var task ReviewTask
		var completedAt sql.NullTime
		var taskName, duration, errorMsg, riskGrade sql.NullString
		var riskScore sql.NullFloat64
		err := rows.Scan(
			&task.TaskID, &task.Status, &taskName, &task.InputType, &task.InputPath,
			&task.FilesCount, &task.GoFilesCount,
			&task.StartedAt, &completedAt, &duration, &errorMsg, &riskScore, &riskGrade,
		)
		if err != nil {
			return nil, err
		}
		if taskName.Valid {
			task.TaskName = taskName.String
		}
		if completedAt.Valid {
			task.CompletedAt = &completedAt.Time
		}
		if duration.Valid {
			task.Duration = duration.String
		}
		if errorMsg.Valid {
			task.ErrorMsg = errorMsg.String
		}
		if riskScore.Valid {
			task.RiskScore = riskScore.Float64
		}
		if riskGrade.Valid {
			task.RiskGrade = riskGrade.String
		}
		tasks = append(tasks, &task)
	}
	return tasks, nil
}

// ========== 审查发现 ==========

// SaveFindings 保存审查发现（批量插入）。
func (s *SQLiteStore) SaveFindings(taskID string, findingsList []findings.Finding) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(
		`INSERT INTO cr_findings (task_id, severity, category, rule_id, title, file_path, line, evidence, recommendation, confidence, source)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
	)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, f := range findingsList {
		_, err := stmt.Exec(
			taskID, f.Severity, f.Category, f.RuleID, f.Title,
			f.File, f.Line, f.Evidence, f.Recommendation, f.Confidence, f.Source,
		)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

// GetFindings 获取任务的所有审查发现。
func (s *SQLiteStore) GetFindings(taskID string) ([]findings.Finding, error) {
	rows, err := s.db.Query(
		`SELECT severity, category, rule_id, title, file_path, line, evidence, recommendation, confidence, source
		 FROM cr_findings WHERE task_id = ? ORDER BY
		 CASE severity WHEN 'high' THEN 1 WHEN 'medium' THEN 2 WHEN 'low' THEN 3 ELSE 4 END,
		 file_path, line`, taskID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []findings.Finding
	for rows.Next() {
		var f findings.Finding
		err := rows.Scan(
			&f.Severity, &f.Category, &f.RuleID, &f.Title,
			&f.File, &f.Line, &f.Evidence, &f.Recommendation, &f.Confidence, &f.Source,
		)
		if err != nil {
			return nil, err
		}
		result = append(result, f)
	}
	return result, nil
}

// ========== 沙箱执行记录 ==========

// SaveSandboxRun 保存沙箱执行记录。
func (s *SQLiteStore) SaveSandboxRun(run *SandboxRun) error {
	_, err := s.db.Exec(
		`INSERT INTO cr_sandbox_runs (task_id, command, backend, exit_code, output, truncated, duration, started_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		run.TaskID, run.Command, run.Backend, run.ExitCode,
		run.Output, run.Truncated, run.Duration, run.StartedAt,
	)
	return err
}

// GetSandboxRuns 获取任务的所有沙箱执行记录。
func (s *SQLiteStore) GetSandboxRuns(taskID string) ([]*SandboxRun, error) {
	rows, err := s.db.Query(
		`SELECT task_id, command, backend, exit_code, output, truncated, duration, started_at
		 FROM cr_sandbox_runs WHERE task_id = ? ORDER BY started_at`, taskID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var runs []*SandboxRun
	for rows.Next() {
		var run SandboxRun
		err := rows.Scan(
			&run.TaskID, &run.Command, &run.Backend, &run.ExitCode,
			&run.Output, &run.Truncated, &run.Duration, &run.StartedAt,
		)
		if err != nil {
			return nil, err
		}
		runs = append(runs, &run)
	}
	return runs, nil
}

// ========== 权限决策记录 ==========

// SavePermissionDecision 保存权限决策记录。
func (s *SQLiteStore) SavePermissionDecision(decision *PermissionDecision) error {
	_, err := s.db.Exec(
		`INSERT INTO cr_permission_decisions (task_id, tool_name, command, action, reason, decided_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		decision.TaskID, decision.ToolName, decision.Command,
		decision.Action, decision.Reason, decision.DecidedAt,
	)
	return err
}

// GetPermissionDecisions 获取任务的所有权限决策记录。
func (s *SQLiteStore) GetPermissionDecisions(taskID string) ([]*PermissionDecision, error) {
	rows, err := s.db.Query(
		`SELECT task_id, tool_name, command, action, reason, decided_at
		 FROM cr_permission_decisions WHERE task_id = ? ORDER BY decided_at`, taskID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var decisions []*PermissionDecision
	for rows.Next() {
		var d PermissionDecision
		err := rows.Scan(
			&d.TaskID, &d.ToolName, &d.Command, &d.Action, &d.Reason, &d.DecidedAt,
		)
		if err != nil {
			return nil, err
		}
		decisions = append(decisions, &d)
	}
	return decisions, nil
}

// ========== 报告 ==========

// SaveReport 保存审查报告。
func (s *SQLiteStore) SaveReport(taskID string, jsonReport, mdReport string) error {
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO cr_reports (task_id, json_report, md_report) VALUES (?, ?, ?)`,
		taskID, jsonReport, mdReport,
	)
	return err
}

// GetReport 获取审查报告。
func (s *SQLiteStore) GetReport(taskID string) (jsonReport, mdReport string, err error) {
	row := s.db.QueryRow(
		`SELECT json_report, md_report FROM cr_reports WHERE task_id = ?`, taskID,
	)
	err = row.Scan(&jsonReport, &mdReport)
	return
}

// ========== 辅助方法 ==========

// SaveFullResult 一次性保存完整的审查结果。
func (s *SQLiteStore) SaveFullResult(task *ReviewTask, findingsList []findings.Finding, report any) error {
	// 保存任务
	if err := s.CreateTask(task); err != nil {
		return fmt.Errorf("保存任务失败: %w", err)
	}

	// 保存 findings
	if len(findingsList) > 0 {
		if err := s.SaveFindings(task.TaskID, findingsList); err != nil {
			return fmt.Errorf("保存 findings 失败: %w", err)
		}
	}

	// 保存报告（如果是 map 或 struct，序列化为 JSON）
	if report != nil {
		jsonBytes, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return fmt.Errorf("序列化报告失败: %w", err)
		}
		if err := s.SaveReport(task.TaskID, string(jsonBytes), ""); err != nil {
			return fmt.Errorf("保存报告失败: %w", err)
		}
	}

	return nil
}

// GetTaskSummary 获取任务摘要（任务 + findings 数量 + 状态）。
func (s *SQLiteStore) GetTaskSummary(taskID string) (map[string]any, error) {
	task, err := s.GetTask(taskID)
	if err != nil {
		return nil, err
	}

	// 统计 findings
	var high, medium, low, info int
	rows, err := s.db.Query(
		`SELECT severity, COUNT(*) FROM cr_findings WHERE task_id = ? GROUP BY severity`, taskID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var sev string
		var count int
		if err := rows.Scan(&sev, &count); err != nil {
			return nil, err
		}
		switch sev {
		case "high":
			high = count
		case "medium":
			medium = count
		case "low":
			low = count
		case "info":
			info = count
		}
	}

	return map[string]any{
		"task":   task,
		"high":   high,
		"medium": medium,
		"low":    low,
		"info":   info,
		"total":  high + medium + low + info,
	}, nil
}

// ========== 产物管理 ==========

// SaveArtifact 保存审查产物。
func (s *SQLiteStore) SaveArtifact(artifact *Artifact) error {
	_, err := s.db.Exec(
		`INSERT INTO cr_artifacts (task_id, artifact_type, file_path, content, size, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		artifact.TaskID, artifact.ArtifactType, artifact.FilePath,
		artifact.Content, artifact.Size, artifact.CreatedAt,
	)
	return err
}

// GetArtifacts 获取任务的所有产物。
func (s *SQLiteStore) GetArtifacts(taskID string) ([]*Artifact, error) {
	rows, err := s.db.Query(
		`SELECT task_id, artifact_type, file_path, content, size, created_at
		 FROM cr_artifacts WHERE task_id = ? ORDER BY created_at`, taskID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var artifacts []*Artifact
	for rows.Next() {
		var a Artifact
		if err := rows.Scan(&a.TaskID, &a.ArtifactType, &a.FilePath,
			&a.Content, &a.Size, &a.CreatedAt); err != nil {
			return nil, err
		}
		artifacts = append(artifacts, &a)
	}
	return artifacts, nil
}

// GetFindingStats 返回全库 findings 聚合统计（按严重级别/分类/规则 TopN）。
func (s *SQLiteStore) GetFindingStats() (*FindingStats, error) {
	stats := &FindingStats{
		BySeverity: make(map[string]int),
		ByCategory: make(map[string]int),
	}

	if err := s.db.QueryRow(`SELECT COUNT(*) FROM cr_findings`).Scan(&stats.Total); err != nil {
		return nil, fmt.Errorf("统计 findings 总数失败: %w", err)
	}

	rows, err := s.db.Query(`SELECT severity, COUNT(*) FROM cr_findings GROUP BY severity`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			rows.Close()
			return nil, err
		}
		stats.BySeverity[k] = n
	}
	rows.Close()

	rows, err = s.db.Query(`SELECT category, COUNT(*) FROM cr_findings GROUP BY category`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			rows.Close()
			return nil, err
		}
		stats.ByCategory[k] = n
	}
	rows.Close()

	rows, err = s.db.Query(
		`SELECT rule_id, COUNT(*) AS n FROM cr_findings GROUP BY rule_id ORDER BY n DESC LIMIT 10`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var rc RuleCount
		if err := rows.Scan(&rc.RuleID, &rc.Count); err != nil {
			return nil, err
		}
		stats.TopRules = append(stats.TopRules, rc)
	}

	return stats, nil
}

// GetTrendStats 趋势聚合（M7-F5）：全量任务数/均分/最高分 + 最近 30 天按天聚合
// + 最近 10 个任务（风险分直接读任务表冗余列，不再逐个解析报告 JSON）。
func (s *SQLiteStore) GetTrendStats() (*TrendStats, error) {
	stats := &TrendStats{}

	// 全量聚合（修 P2-11：total_tasks 不再被 LIMIT 200 截断）
	var avg, maxRisk sql.NullFloat64
	if err := s.db.QueryRow(
		`SELECT COUNT(*), AVG(risk_score), MAX(risk_score) FROM cr_review_tasks`,
	).Scan(&stats.TotalTasks, &avg, &maxRisk); err != nil {
		return nil, fmt.Errorf("聚合任务统计失败: %w", err)
	}
	if avg.Valid {
		stats.AvgRisk = avg.Float64
	}
	if maxRisk.Valid {
		stats.MaxRisk = maxRisk.Float64
	}

	// 最近 30 天按天聚合。
	// 用 substr(started_at,1,10) 取"写入时的本地日期"而非 date()——
	// Go driver 存的 RFC3339 带 +08:00 时区，date() 会折算成 UTC 日期，
	// 凌晨任务会整体漂移到"昨天"；substr 与 naive 本地字符串语义一致。
	rows, err := s.db.Query(
		`SELECT substr(started_at, 1, 10) AS day, COUNT(*), AVG(risk_score)
		 FROM cr_review_tasks
		 WHERE substr(started_at, 1, 10) >= date('now', 'localtime', '-29 days')
		 GROUP BY day ORDER BY day`,
	)
	if err != nil {
		return nil, fmt.Errorf("聚合每日趋势失败: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var day TrendDay
		var avgDay sql.NullFloat64
		if err := rows.Scan(&day.Date, &day.Tasks, &avgDay); err != nil {
			return nil, err
		}
		if avgDay.Valid {
			day.AvgRisk = avgDay.Float64
		}
		stats.Daily = append(stats.Daily, day)
	}

	// 最近 10 个任务
	recent, err := s.ListTasks(10)
	if err != nil {
		return nil, err
	}
	stats.Recent = recent
	if stats.Recent == nil {
		stats.Recent = []*ReviewTask{}
	}
	return stats, nil
}

// ========== 误报标记（M8-C9：记忆降噪） ==========

// SaveFalsePositiveMark 记录一条人工误报标记。
func (s *SQLiteStore) SaveFalsePositiveMark(mark *FalsePositiveMark) error {
	res, err := s.db.Exec(
		`INSERT INTO cr_false_positive_marks (rule_id, file_path, line, task_id, created_at)
		 VALUES (?, ?, ?, ?, ?)`,
		mark.RuleID, mark.FilePath, mark.Line, mark.TaskID, mark.CreatedAt,
	)
	if err != nil {
		return err
	}
	if id, err := res.LastInsertId(); err == nil {
		mark.ID = id
	}
	return nil
}

// ListFalsePositiveMarks 返回全部误报标记（表规模=人工标记数，全量拉取内存匹配）。
func (s *SQLiteStore) ListFalsePositiveMarks() ([]*FalsePositiveMark, error) {
	rows, err := s.db.Query(
		`SELECT id, rule_id, file_path, line, task_id, created_at
		 FROM cr_false_positive_marks ORDER BY id DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var marks []*FalsePositiveMark
	for rows.Next() {
		var m FalsePositiveMark
		var taskID sql.NullString
		if err := rows.Scan(&m.ID, &m.RuleID, &m.FilePath, &m.Line, &taskID, &m.CreatedAt); err != nil {
			return nil, err
		}
		if taskID.Valid {
			m.TaskID = taskID.String
		}
		marks = append(marks, &m)
	}
	return marks, nil
}

// DeleteFalsePositiveMark 撤销一条误报标记（恢复该模式的正常上报）。
func (s *SQLiteStore) DeleteFalsePositiveMark(id int64) error {
	_, err := s.db.Exec(`DELETE FROM cr_false_positive_marks WHERE id = ?`, id)
	return err
}

// ========== 运行时设置（M8-设置中心） ==========

// GetSetting 读取一条设置。ok=false 表示未设置。
func (s *SQLiteStore) GetSetting(key string) (string, bool, error) {
	var value string
	err := s.db.QueryRow(`SELECT value FROM cr_settings WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

// SetSetting 写入（upsert）一条设置。
func (s *SQLiteStore) SetSetting(key, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO cr_settings (key, value, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, time.Now(),
	)
	return err
}

// DeleteSetting 删除一条设置。
func (s *SQLiteStore) DeleteSetting(key string) error {
	_, err := s.db.Exec(`DELETE FROM cr_settings WHERE key = ?`, key)
	return err
}

// ListSettings 返回全部设置键（value 不填充，避免敏感值扩散；需要值用 GetSetting）。
func (s *SQLiteStore) ListSettings() ([]*SettingKV, error) {
	rows, err := s.db.Query(`SELECT key, updated_at FROM cr_settings ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*SettingKV
	for rows.Next() {
		kv := &SettingKV{}
		if err := rows.Scan(&kv.Key, &kv.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, kv)
	}
	return out, rows.Err()
}
