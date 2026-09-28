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
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"code-review-agent/diff"
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
	Port          int           // HTTP 端口
	DBPath        string        // SQLite 路径
	DataDir       string        // 审查产物目录（报告/审计日志）
	RulesDir      string        // YAML 自定义规则目录（可空）
	SandboxMode   string        // 仓库审查的沙箱模式：off / container / local
	SampleDir     string        // 示例 diff 目录（可空）
	Workers       int           // 异步审查并发 worker 数（M7-F1；<1 = 1，默认串行执行）
	TaskTimeout   time.Duration // 单任务看门狗上限（M7-F1；<=0 = 10 分钟）
	AuthToken     string        // 写操作认证 token（M7-F2；空 = 不启用认证）
	RatePerSec    float64       // 审查提交限流速率/每 IP（M7-F2；<=0 = 2）
	RateBurst     int           // 审查提交限流桶容量（M7-F2；<=0 = 10）
	MaxBodyBytes  int64         // 请求体上限（M7-F2；<=0 = 10MB）
	GitHubAPIBase string        // GitHub API 基地址（M7-F3；空 = 官方，测试可注入假服务）
	AllowedRepos  []string      // M7-F4：仓库路径白名单前缀（空 = 不限制，本地模式；配置后 repo_path 必须落在前缀内）
}

// 默认请求体上限 10MB：一个审查 diff 的合理上限远小于此。
const defaultMaxBodyBytes = 10 << 20

// Server 代码审查 HTTP 服务。
type Server struct {
	cfg     Config
	store   storage.Store
	mux     *http.ServeMux
	srv     *http.Server
	queue   *reviewQueue   // M7-F1：异步审查队列（替代原全局互斥的同步执行）
	limiter *ipRateLimiter // M7-F2：审查提交 IP 限流
	handler http.Handler   // 完整中间件链（Handler() 与 ListenAndServe 共用）
	maxBody int64          // 生效的请求体上限
}

// New 创建并初始化服务（打开数据库、注册路由、启动审查 worker）。
func New(cfg Config) (*Server, error) {
	if err := review.EnsureOutputDir(cfg.DataDir); err != nil {
		return nil, err
	}
	store, err := storage.NewSQLiteStore(cfg.DBPath)
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}

	maxBody := cfg.MaxBodyBytes
	if maxBody <= 0 {
		maxBody = defaultMaxBodyBytes
	}

	s := &Server{
		cfg:     cfg,
		store:   store,
		mux:     http.NewServeMux(),
		limiter: newIPRateLimiter(cfg.RatePerSec, cfg.RateBurst),
		maxBody: maxBody,
	}
	s.queue = newReviewQueue(store, cfg.Workers, cfg.TaskTimeout)
	s.routes()
	// 中间件链：安全头 → 写认证 → 路由（限流挂在提交端点上，见 routes）
	s.handler = secureHeaders(writeAuth(cfg.AuthToken, s.mux))
	return s, nil
}

// Handler 返回根 HTTP Handler（供测试与自定义宿主嵌入）。
func (s *Server) Handler() http.Handler { return s.handler }

// ListenAndServe 启动 HTTP 服务（阻塞）。
func (s *Server) ListenAndServe() error {
	s.srv = &http.Server{
		Addr:    fmt.Sprintf(":%d", s.cfg.Port),
		Handler: s.handler,
		// M7-F2：连接层超时——慢连接/慢请求不占用服务资源
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	return s.srv.ListenAndServe()
}

// Close 关闭服务与数据库连接。
func (s *Server) Close() error {
	if s.queue != nil {
		s.queue.Close()
	}
	if s.srv != nil {
		_ = s.srv.Close()
	}
	return s.store.Close()
}

func (s *Server) routes() {
	s.mux.HandleFunc("/", s.handleIndex)
	s.mux.HandleFunc("/static/", s.handleStatic)
	s.mux.HandleFunc("/api/health", s.method("GET", s.handleHealth))
	// M7-F2/F3：审查提交是重操作，JSON 与上传端点都包 IP 限流
	s.mux.Handle("/api/reviews", s.limiter.limitSubmit(http.HandlerFunc(s.method("POST", s.handleCreateReview))))
	s.mux.Handle("/api/reviews/upload", s.limiter.limitSubmit(http.HandlerFunc(s.method("POST", s.handleUploadReview))))
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
	DiffContent  string            `json:"diff_content"`  // diff 文本（与 repo_path / files_content / pr_url 四选一）
	RepoPath     string            `json:"repo_path"`     // git 仓库路径（取未提交变更，服务器本地）
	FilesContent map[string]string `json:"files_content"` // M7-F3：粘贴整文件 {文件名: 内容}，整体按新增行审查
	PrURL        string            `json:"pr_url"`        // M7-F3：GitHub PR 链接（github.com/{owner}/{repo}/pull/123）
	Sandbox      bool              `json:"sandbox"`       // 是否执行沙箱（仅 repo_path 有效）
	LLMMode      string            `json:"llm_mode"`      // LLM 复核（M4）："fake" 确定性回放 / "openai"（服务端需 OPENAI_API_KEY）
}

// preflightAddedLines 检查解析出的文件里确有新增行（M7-F1 预检语义）。
func preflightAddedLines(files []diff.FileDiff) error {
	added := 0
	for i := range files {
		added += len(files[i].AddedLines())
	}
	if added == 0 {
		return errNoAddedLines
	}
	return nil
}

// errNoAddedLines 无新增行（映射 422）。
var errNoAddedLines = errors.New("diff 中没有可审查的变更（没有任何新增行）")

// normalizeRepoPath 规范化仓库路径：转绝对路径、Clean、确保以 / 结尾边界处理。
func normalizeRepoPath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	return filepath.Clean(abs)
}

// checkRepoAllowed 校验仓库路径是否在白名单内（M7-F4）。
// 未配置白名单 = 不限制（本地模式，行为不变）。
// 匹配规则：路径等于前缀，或位于前缀目录之下（目录边界，/tmp/repos 不放行 /tmp/repositories）；
// 请求路径先规范化（Abs+Clean），/tmp/allowed/../secret 这类穿越路径规范后落在白名单外即拒绝。
func (s *Server) checkRepoAllowed(repoPath string) error {
	if len(s.cfg.AllowedRepos) == 0 {
		return nil
	}
	norm := normalizeRepoPath(repoPath)
	for _, prefix := range s.cfg.AllowedRepos {
		p := normalizeRepoPath(prefix)
		if norm == p || strings.HasPrefix(norm, p+string(filepath.Separator)) {
			return nil
		}
	}
	return fmt.Errorf("仓库路径不在白名单内（--allow-repo）： %s", repoPath)
}

func (s *Server) handleCreateReview(w http.ResponseWriter, r *http.Request) {
	// M7-F2：请求体上限（超限 413）
	r.Body = http.MaxBytesReader(w, r.Body, s.maxBody)
	var req createReviewRequest
	if err := readJSON(r, &req); err != nil {
		if bodyTooLarge(err) {
			writeErr(w, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("请求体超过上限 %d MB", s.maxBody>>20))
			return
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.DiffContent == "" && req.RepoPath == "" && len(req.FilesContent) == 0 && req.PrURL == "" {
		writeErr(w, http.StatusBadRequest, "diff_content / repo_path / files_content / pr_url 必须提供其一")
		return
	}

	// M7-F1/F3：入队前同步预检——非法输入与空变更当场 400/422，
	// PR 拉取失败当场 502，只有执行期错误才落在任务状态里。
	var inputType, inputPath string
	var fileContents []diff.NamedContent
	switch {
	case req.PrURL != "":
		ref, err := parsePRURL(req.PrURL)
		if err != nil {
			writeErr(w, http.StatusBadRequest, review.ErrInvalidInput.Error()+": "+err.Error())
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		defer cancel()
		diffText, err := s.fetchPRDiff(ctx, ref)
		if err != nil {
			writeErr(w, http.StatusBadGateway, "拉取 PR 失败: "+err.Error())
			return
		}
		files, err := diff.ReadFromContent(diffText)
		if err != nil {
			writeErr(w, http.StatusBadRequest, review.ErrInvalidInput.Error()+": PR diff 解析失败: "+err.Error())
			return
		}
		if err := preflightAddedLines(files); err != nil {
			writeErr(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		inputType = "pr_url"
		inputPath = fmt.Sprintf("%s/%s#%s", ref.Owner, ref.Repo, ref.Number)
		req.DiffContent = diffText // 复用 diff 管线
	case len(req.FilesContent) > 0:
		// M7-F3：粘贴整文件；按文件名排序保证输出顺序稳定
		names := make([]string, 0, len(req.FilesContent))
		for name := range req.FilesContent {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			fileContents = append(fileContents, diff.NamedContent{Name: name, Content: req.FilesContent[name]})
		}
		files, err := diff.ReadFromContents(fileContents)
		if err != nil {
			writeErr(w, http.StatusBadRequest, review.ErrInvalidInput.Error()+": "+err.Error())
			return
		}
		if err := preflightAddedLines(files); err != nil {
			writeErr(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		inputType = "file_contents"
		inputPath = strings.Join(names, ",")
	case req.DiffContent != "":
		inputType, inputPath = "diff_content", "api-upload"
		files, err := diff.ReadFromContent(req.DiffContent)
		if err != nil {
			writeErr(w, http.StatusBadRequest, review.ErrInvalidInput.Error()+": "+err.Error())
			return
		}
		if err := preflightAddedLines(files); err != nil {
			writeErr(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
	default:
		inputType, inputPath = "repo_path", req.RepoPath
		if err := s.checkRepoAllowed(req.RepoPath); err != nil {
			writeErr(w, http.StatusForbidden, err.Error())
			return
		}
		if _, err := os.Stat(req.RepoPath); err != nil {
			writeErr(w, http.StatusBadRequest, review.ErrInvalidInput.Error()+": 仓库路径不可访问: "+req.RepoPath)
			return
		}
	}

	// 沙箱策略：默认关闭；显式要求且给了仓库路径时启用
	sandboxMode := review.SandboxOff
	if req.Sandbox && req.RepoPath != "" {
		sandboxMode = s.cfg.SandboxMode
		if sandboxMode == "" || sandboxMode == review.SandboxOff {
			sandboxMode = "local"
		}
	}

	// M7-F1：任务 ID 在入队时预分配（202 响应与 review.Run 落库用同一个）
	taskID := review.NewTaskID()
	job := &queuedJob{
		id:        taskID,
		inputType: inputType,
		inputPath: inputPath,
		submitted: time.Now(),
		opts: review.Options{
			DiffContent:  req.DiffContent,
			FileContents: fileContents,
			RepoPath:     req.RepoPath,
			TaskID:       taskID,
			InputLabel:   inputPath,
			LLMMode:      req.LLMMode,
			RulesDir:     s.cfg.RulesDir,
			DBPath:       s.cfg.DBPath,
			OutputDir:    s.cfg.DataDir,
			SandboxMode:  sandboxMode,
			AuditFile:    "tool_safety_audit.jsonl",
		},
	}
	if err := s.queue.submit(job); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, errQueueFull) || errors.Is(err, errQueueClosed) {
			status = http.StatusServiceUnavailable
		}
		writeErr(w, status, err.Error())
		return
	}

	// M7-F1：202 + task_id，结果由 GET /api/tasks/{id} 轮询
	writeJSON(w, http.StatusAccepted, map[string]any{
		"task_id": job.id,
		"status":  "queued",
	})
}

// handleUploadReview POST /api/reviews/upload（M7-F3，multipart/form-data）：
// 字段 files——一个或多个文本文件，或单个 .zip；整体按新增行审查。
func (s *Server) handleUploadReview(w http.ResponseWriter, r *http.Request) {
	// multipart 大小受同一 maxBody 上限约束（超限 413）
	r.Body = http.MaxBytesReader(w, r.Body, s.maxBody)
	if err := r.ParseMultipartForm(s.maxBody); err != nil {
		if bodyTooLarge(err) {
			writeErr(w, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("上传超过上限 %d MB", s.maxBody>>20))
			return
		}
		writeErr(w, http.StatusBadRequest, "解析 multipart 表单失败: "+err.Error())
		return
	}
	if r.MultipartForm == nil {
		writeErr(w, http.StatusBadRequest, "没有 multipart 表单（字段名 files）")
		return
	}
	contents, err := parseUploadFiles(r.MultipartForm)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	names := make([]string, 0, len(contents))
	for _, nc := range contents {
		names = append(names, nc.Name)
	}

	taskID := review.NewTaskID()
	job := &queuedJob{
		id:        taskID,
		inputType: "upload",
		inputPath: strings.Join(names, ","),
		submitted: time.Now(),
		opts: review.Options{
			FileContents: contents,
			TaskID:       taskID,
			InputLabel:   strings.Join(names, ","),
			RulesDir:     s.cfg.RulesDir,
			DBPath:       s.cfg.DBPath,
			OutputDir:    s.cfg.DataDir,
			SandboxMode:  review.SandboxOff,
			AuditFile:    "tool_safety_audit.jsonl",
		},
	}
	if err := s.queue.submit(job); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, errQueueFull) || errors.Is(err, errQueueClosed) {
			status = http.StatusServiceUnavailable
		}
		writeErr(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"task_id": job.id,
		"status":  "queued",
	})
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
		// DB 还没有该任务的行：要么是异步队列里的进行中/刚终态任务（M7-F1 内存注册表），
		// 要么真的不存在。
		if errors.Is(err, sql.ErrNoRows) {
			if job := s.queue.lookup(taskID); job != nil {
				st, errMsg := job.getStatus()
				writeJSON(w, http.StatusOK, map[string]any{
					"task": map[string]any{
						"task_id":    job.id,
						"status":     st,
						"input_type": job.inputType,
						"input_path": job.inputPath,
						"started_at": job.submitted,
						"error_msg":  errMsg,
					},
					"report":               nil,
					"sandbox_runs":         []any{},
					"permission_decisions": []any{},
				})
				return
			}
			writeErr(w, http.StatusNotFound, "任务不存在: "+taskID)
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	// 失败任务（异步失败由队列补记，无报告可读）
	if task.Status == storage.TaskStatusFailed {
		writeJSON(w, http.StatusOK, map[string]any{
			"task":                 task,
			"report":               nil,
			"sandbox_runs":         []*storage.SandboxRun{},
			"permission_decisions": []*storage.PermissionDecision{},
		})
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

	// M7-F5（修 P2-11）：趋势走 SQL 聚合——total/avg/max 全量准确，
	// 不再逐个解析报告 JSON（O(N) → O(1)），recent 直接读任务表冗余列
	trend, err := s.store.GetTrendStats()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	recent := make([]map[string]any, 0, len(trend.Recent))
	for _, t := range trend.Recent {
		recent = append(recent, map[string]any{
			"task_id":    t.TaskID,
			"input_path": t.InputPath,
			"started_at": t.StartedAt,
			"risk_score": t.RiskScore,
			"risk_grade": t.RiskGrade,
		})
	}

	if trend.Daily == nil {
		trend.Daily = []storage.TrendDay{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"finding_stats": stats,
		"total_tasks":   trend.TotalTasks,
		"avg_risk":      trend.AvgRisk,
		"max_risk":      trend.MaxRisk,
		"recent_risks":  recent,
		"trend_daily":   trend.Daily,
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
