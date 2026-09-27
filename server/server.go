// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//
// Package server 提供代码审查服务的 HTTP API 与内嵌 Web 前端。
//
// 端点一览：
//
//	GET  /                      Web 前端（SPA，go:embed 内嵌）
//	GET  /static/*              前端静态资源
//	GET  /api/health            健康检查
//	POST /api/reviews           提交审查（diff 文本或仓库路径）
//	GET  /api/tasks             任务列表
//	GET  /api/tasks/{id}        任务详情（含 findings / 沙箱 / 权限决策）
//	GET  /api/tasks/{id}/report 任务 Markdown 报告
//	GET  /api/stats             聚合统计（总览页）
//	GET  /api/rules             规则引擎与评分维度说明
//	GET  /api/samples           示例 diff（一键演示）
//
// 业务管线与 CLI 完全同一套（review.Run），保证展示的就是真实业务逻辑。
package server

import (
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"code-review-agent/report"
	"code-review-agent/review"
	"code-review-agent/rules"
	"code-review-agent/scoring"
	"code-review-agent/storage"
)

//go:embed web
var webFS embed.FS

// Version 服务版本号。
const Version = "1.0.0"

// Config 服务配置。
type Config struct {
	Port        int    // HTTP 端口
	DBPath      string // SQLite 路径
	DataDir     string // 审查产物目录（报告/审计日志）
	RulesDir    string // YAML 自定义规则目录（可空）
	SandboxMode string // 仓库审查的沙箱模式：off / container / local
	SampleDir   string // 示例 diff 目录（可空）
}

// Server 代码审查 HTTP 服务。
type Server struct {
	cfg   Config
	store storage.Store
	mux   *http.ServeMux
	srv   *http.Server
	mu    sync.Mutex // 串行化审查请求，规避 SQLite 并发写
}

// New 创建并初始化服务（打开数据库、注册路由）。
func New(cfg Config) (*Server, error) {
	if err := review.EnsureOutputDir(cfg.DataDir); err != nil {
		return nil, err
	}
	store, err := storage.NewSQLiteStore(cfg.DBPath)
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}

	s := &Server{
		cfg:   cfg,
		store: store,
		mux:   http.NewServeMux(),
	}
	s.routes()
	return s, nil
}

// Handler 返回根 HTTP Handler（供测试与自定义宿主嵌入）。
func (s *Server) Handler() http.Handler { return s.mux }

// ListenAndServe 启动 HTTP 服务（阻塞）。
func (s *Server) ListenAndServe() error {
	s.srv = &http.Server{
		Addr:    fmt.Sprintf(":%d", s.cfg.Port),
		Handler: s.mux,
	}
	return s.srv.ListenAndServe()
}

// Close 关闭服务与数据库连接。
func (s *Server) Close() error {
	if s.srv != nil {
		_ = s.srv.Close()
	}
	return s.store.Close()
}

func (s *Server) routes() {
	s.mux.HandleFunc("/", s.handleIndex)
	s.mux.HandleFunc("/static/", s.handleStatic)
	s.mux.HandleFunc("/api/health", s.method("GET", s.handleHealth))
	s.mux.HandleFunc("/api/reviews", s.method("POST", s.handleCreateReview))
	s.mux.HandleFunc("/api/tasks", s.method("GET", s.handleListTasks))
	s.mux.HandleFunc("/api/tasks/", s.method("GET", s.handleTaskDetail))
	s.mux.HandleFunc("/api/stats", s.method("GET", s.handleStats))
	s.mux.HandleFunc("/api/rules", s.method("GET", s.handleRules))
	s.mux.HandleFunc("/api/samples", s.method("GET", s.handleSamples))
}

// method 包装 handler 做请求方法校验。
func (s *Server) method(m string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != m {
			writeErr(w, http.StatusMethodNotAllowed, fmt.Sprintf("需要 %s 方法", m))
			return
		}
		h(w, r)
	}
}

// ========== 前端静态资源 ==========

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		writeErr(w, http.StatusNotFound, "页面不存在")
		return
	}
	serveWebFile(w, "web/index.html", "text/html; charset=utf-8")
}

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/static/")
	switch name {
	case "app.js":
		serveWebFile(w, "web/app.js", "text/javascript; charset=utf-8")
	case "style.css":
		serveWebFile(w, "web/style.css", "text/css; charset=utf-8")
	default:
		writeErr(w, http.StatusNotFound, "资源不存在")
	}
}

func serveWebFile(w http.ResponseWriter, name, ctype string) {
	data, err := webFS.ReadFile(name)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取内嵌资源失败")
		return
	}
	w.Header().Set("Content-Type", ctype)
	_, _ = w.Write(data)
}

// ========== API ==========

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"version": Version,
	})
}

// createReviewRequest POST /api/reviews 请求体。
type createReviewRequest struct {
	DiffContent string `json:"diff_content"` // diff 文本（与 repo_path 二选一）
	RepoPath    string `json:"repo_path"`    // git 仓库路径（取未提交变更）
	Sandbox     bool   `json:"sandbox"`      // 是否执行沙箱（仅 repo_path 有效）
	LLMMode     string `json:"llm_mode"`     // LLM 复核（M4）："fake" 确定性回放 / "openai"（服务端需 OPENAI_API_KEY）
}

func (s *Server) handleCreateReview(w http.ResponseWriter, r *http.Request) {
	var req createReviewRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.DiffContent == "" && req.RepoPath == "" {
		writeErr(w, http.StatusBadRequest, "diff_content 与 repo_path 必须提供其一")
		return
	}

	// 沙箱策略：默认关闭；显式要求且给了仓库路径时启用
	sandboxMode := review.SandboxOff
	if req.Sandbox && req.RepoPath != "" {
		sandboxMode = s.cfg.SandboxMode
		if sandboxMode == "" || sandboxMode == review.SandboxOff {
			sandboxMode = "local"
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	rep, err := review.Run(review.Options{
		DiffContent: req.DiffContent,
		RepoPath:    req.RepoPath,
		LLMMode:     req.LLMMode,
		RulesDir:    s.cfg.RulesDir,
		DBPath:      s.cfg.DBPath,
		OutputDir:   s.cfg.DataDir,
		SandboxMode: sandboxMode,
		AuditFile:   "tool_safety_audit.jsonl",
	})
	if err != nil {
		if err == review.ErrNoChanges {
			writeErr(w, http.StatusUnprocessableEntity, "diff 中没有可审查的变更")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, rep)
}

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &limit); err != nil || limit <= 0 || limit > 500 {
			limit = 50
		}
	}
	tasks, err := s.store.ListTasks(limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": tasks, "count": len(tasks)})
}

func (s *Server) handleTaskDetail(w http.ResponseWriter, r *http.Request) {
	taskID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/tasks/"), "/")
	if taskID == "" {
		writeErr(w, http.StatusBadRequest, "缺少任务 ID")
		return
	}

	// /api/tasks/{id} 与 /api/tasks/{id}/report 两个端点
	if strings.HasSuffix(taskID, "/report") {
		s.serveTaskReport(w, strings.TrimSuffix(taskID, "/report"))
		return
	}

	task, err := s.store.GetTask(taskID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "任务不存在: "+taskID)
		return
	}

	jsonReport, _, err := s.store.GetReport(taskID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取报告失败: "+err.Error())
		return
	}
	var rep report.ReviewReport
	if err := json.Unmarshal([]byte(jsonReport), &rep); err != nil {
		writeErr(w, http.StatusInternalServerError, "解析报告失败: "+err.Error())
		return
	}

	sandboxRuns, err := s.store.GetSandboxRuns(taskID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if sandboxRuns == nil {
		sandboxRuns = []*storage.SandboxRun{}
	}
	perms, err := s.store.GetPermissionDecisions(taskID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if perms == nil {
		perms = []*storage.PermissionDecision{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"task":                 task,
		"report":               &rep,
		"sandbox_runs":         sandboxRuns,
		"permission_decisions": perms,
	})
}

func (s *Server) serveTaskReport(w http.ResponseWriter, taskID string) {
	_, mdReport, err := s.store.GetReport(taskID)
	if err != nil || mdReport == "" {
		writeErr(w, http.StatusNotFound, "报告不存在: "+taskID)
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s.md", taskID))
	_, _ = w.Write([]byte(mdReport))
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.GetFindingStats()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	// 每个任务的风险评分在报告 JSON 里，取最近任务汇总
	tasks, err := s.store.ListTasks(200)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	var recent []map[string]any
	var sum, max float64
	counted := 0
	for _, t := range tasks {
		jsonReport, _, err := s.store.GetReport(t.TaskID)
		if err != nil {
			continue
		}
		var rep report.ReviewReport
		if json.Unmarshal([]byte(jsonReport), &rep) != nil {
			continue
		}
		sum += rep.Monitor.RiskScore
		counted++
		if rep.Monitor.RiskScore > max {
			max = rep.Monitor.RiskScore
		}
		if len(recent) < 10 {
			recent = append(recent, map[string]any{
				"task_id":    t.TaskID,
				"input_path": t.InputPath,
				"started_at": t.StartedAt,
				"risk_score": rep.Monitor.RiskScore,
				"risk_grade": rep.Monitor.RiskGrade,
			})
		}
	}

	var avg float64
	if counted > 0 {
		avg = sum / float64(counted)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"finding_stats": stats,
		"total_tasks":   len(tasks),
		"avg_risk":      avg,
		"max_risk":      max,
		"recent_risks":  recent,
	})
}

// ruleMeta 规则元数据（业务逻辑展示）。
type ruleMeta struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Severity string `json:"severity"`
	Category string `json:"category"`
	Source   string `json:"source"` // builtin / yaml-dsl
}

func (s *Server) handleRules(w http.ResponseWriter, r *http.Request) {
	engine := rules.NewEngine()
	engine.Register(rules.NewTokenSecretRule())
	engine.Register(rules.NewTokenLeakRule())
	engine.Register(rules.NewTokenGoroutineRule())
	engine.Register(rules.NewTokenResourceRule())
	engine.Register(rules.NewTokenErrorRule())
	engine.Register(rules.NewTokenMissingTestRule())
	engine.Register(rules.NewTokenDBLifecycleRule()) // M2-D4

	ruleList := make([]ruleMeta, 0, 6)
	for _, rule := range engine.Rules() {
		ruleList = append(ruleList, ruleMeta{
			ID: rule.ID(), Name: rule.Name(),
			Severity: string(rule.Severity()), Category: string(rule.Category()),
			Source: "builtin",
		})
	}

	if s.cfg.RulesDir != "" {
		if dslRules, err := rules.LoadDSLRules(s.cfg.RulesDir); err == nil {
			for _, rule := range dslRules {
				ruleList = append(ruleList, ruleMeta{
					ID: rule.ID(), Name: rule.Name(),
					Severity: string(rule.Severity()), Category: string(rule.Category()),
					Source: "yaml-dsl",
				})
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"rules":       ruleList,
		"dimensions":  scoring.Dimensions(),
		"weights_sum": weightSum(),
	})
}

func weightSum() float64 {
	var sum float64
	for _, d := range scoring.Dimensions() {
		sum += d.Weight
	}
	return sum
}

func (s *Server) handleSamples(w http.ResponseWriter, r *http.Request) {
	samples := []map[string]string{}
	if s.cfg.SampleDir != "" {
		entries, err := os.ReadDir(s.cfg.SampleDir)
		if err == nil {
			var names []string
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".diff") {
					names = append(names, e.Name())
				}
			}
			sort.Strings(names)
			for _, name := range names {
				data, err := os.ReadFile(filepath.Join(s.cfg.SampleDir, name))
				if err != nil {
					continue
				}
				samples = append(samples, map[string]string{
					"name":    name,
					"content": string(data),
				})
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"samples": samples, "count": len(samples)})
}

// ========== JSON 工具 ==========

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": msg})
}

func readJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("解析请求体失败: %w", err)
	}
	return nil
}
