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
	"crypto/subtle"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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

// Version 服务版本号。单一来源：/api/health、前端侧栏与 MCP serverInfo 均读它。
// 发版时随 tag 同步（v1.1.0 后进入 M8 开发，故为 1.2.0-dev）。
const Version = "1.2.0-dev"

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
	// /api/tasks/{id}（GET 详情）与 /api/tasks/{id}/fp-marks（POST 标记误报）
	// 同前缀必须合并注册，ServeMux 不允许重复 pattern
	s.mux.HandleFunc("/api/tasks/", s.handleTaskRoutes)
	s.mux.HandleFunc("/api/stats", s.method("GET", s.handleStats))
	s.mux.HandleFunc("/api/rules", s.method("GET", s.handleRules))
	s.mux.HandleFunc("/api/samples", s.method("GET", s.handleSamples))
	// M8-C9：误报标记记忆降噪（写操作受认证保护；标记属于写语义）
	s.mux.HandleFunc("/api/fp-marks", s.fpMarksMethod(s.handleListFPMarks, s.handleDeleteFPMark))
	// M8-设置中心：GET 公开读（密钥只回脱敏提示），POST 仅 E2B key（写，受认证）；
	// 同 pattern 必须合并注册（ServeMux 不允许重复 pattern，见 handleTaskRoutes 注释）。
	// /api/settings/test 真实外呼 LLM（max_tokens=1），额外包提交限流。
	s.mux.HandleFunc("/api/settings", s.settingsMethod(s.handleGetSettings, s.handleSaveSettings))
	s.mux.Handle("/api/settings/test", s.limiter.limitSubmit(http.HandlerFunc(s.method("POST", s.handleTestSettings))))
	// M8-设置中心 v2：多方案 CRUD（POST /profiles 精确匹配创建；/profiles/ 子树
	// 处理 {id} 更新/删除、{id}/current 设当前、reset 重置）。写操作受 writeAuth。
	s.mux.HandleFunc("/api/settings/profiles", s.method("POST", s.handleCreateProfile))
	s.mux.HandleFunc("/api/settings/profiles/", s.handleProfileRoutes)
}

// settingsMethod 设置端点：GET=脱敏视图（公开读），POST=保存（写，受认证）。
func (s *Server) settingsMethod(getH, postH http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			getH(w, r)
		case http.MethodPost:
			// 与全局 writeAuth 同语义：配置了 token 且不匹配时拒绝
			if s.cfg.AuthToken != "" &&
				subtle.ConstantTimeCompare([]byte(extractToken(r)), []byte(s.cfg.AuthToken)) != 1 {
				w.Header().Set("WWW-Authenticate", `Bearer realm="code-review-agent"`)
				writeErr(w, http.StatusUnauthorized, "需要认证才能修改设置")
				return
			}
			postH(w, r)
		default:
			writeErr(w, http.StatusMethodNotAllowed, "需要 GET 或 POST 方法")
		}
	}
}

// fpMarksMethod 误报标记端点：GET=列表（公开读），DELETE=撤销（写，受认证）。
func (s *Server) fpMarksMethod(getH, delH http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			getH(w, r)
		case http.MethodDelete:
			// 仅在配置了 token 且不匹配时拒绝（未配置 = 本地模式直接放行）
			if s.cfg.AuthToken != "" &&
				subtle.ConstantTimeCompare([]byte(extractToken(r)), []byte(s.cfg.AuthToken)) != 1 {
				w.Header().Set("WWW-Authenticate", `Bearer realm="code-review-agent"`)
				writeErr(w, http.StatusUnauthorized, "需要认证才能撤销误报标记")
				return
			}
			delH(w, r)
		default:
			writeErr(w, http.StatusMethodNotAllowed, "需要 GET 或 DELETE 方法")
		}
	}
}

// handleDeleteFPMark 撤销一条误报标记（恢复该模式的正常上报）。
func (s *Server) handleDeleteFPMark(w http.ResponseWriter, r *http.Request) {
	idStr := r.URL.Query().Get("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "需要合法的 id 查询参数")
		return
	}
	if err := s.store.DeleteFalsePositiveMark(id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
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
	DiffContent    string            `json:"diff_content"`    // diff 文本（与 repo_path / files_content / pr_url 四选一）
	RepoPath       string            `json:"repo_path"`       // git 仓库路径（取未提交变更，服务器本地）
	FilesContent   map[string]string `json:"files_content"`   // M7-F3：粘贴整文件 {文件名: 内容}，整体按新增行审查
	PrURL          string            `json:"pr_url"`          // M7-F3：GitHub PR 链接（github.com/{owner}/{repo}/pull/123）
	TaskName       string            `json:"task_name"`       // M8：用户可读的任务名称（可空，≤80 字，超长截断）
	Sandbox        bool              `json:"sandbox"`         // 是否执行沙箱（仅 repo_path 有效）
	SandboxBackend string            `json:"sandbox_backend"` // M8-设置中心：local / container / container-fx / e2b；空 = 服务默认
	LLMMode        string            `json:"llm_mode"`        // LLM 复核（M4）："fake" 确定性回放 / "openai"；空 = 关闭
}

// sanitizeTaskName 任务名清洗：去首尾空白、压掉换行、超长截断到 80 字。
func sanitizeTaskName(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	if runes := []rune(s); len(runes) > 80 {
		s = string(runes[:80]) // 名称上限 80 字符（按 rune，中英文一致）
	}
	return s
}

// allowedSandboxBackends 沙箱后端白名单（防止任意字符串进沙箱构造器）。
var allowedSandboxBackends = map[string]bool{
	"local": true, "container": true, "container-fx": true, "e2b": true,
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

	// 沙箱策略：默认关闭；显式要求且给了仓库路径时启用。
	// M8-设置中心：可按请求指定后端（白名单校验），缺省用服务默认。
	sandboxMode := review.SandboxOff
	if req.Sandbox && req.RepoPath != "" {
		if req.SandboxBackend != "" {
			if !allowedSandboxBackends[req.SandboxBackend] {
				writeErr(w, http.StatusBadRequest, "不支持的沙箱后端: "+req.SandboxBackend+"（可选 local/container/container-fx/e2b）")
				return
			}
			sandboxMode = req.SandboxBackend
		} else {
			sandboxMode = s.cfg.SandboxMode
			if sandboxMode == "" || sandboxMode == review.SandboxOff {
				sandboxMode = "local"
			}
		}
		// 选了 e2b 但没有 key：直接给可操作提示，而不是执行期静默回退
		if sandboxMode == "e2b" {
			if key, _ := s.resolveE2BKey(); key == "" {
				writeErr(w, http.StatusBadRequest, "尚未配置 E2B API Key：请到「智能与配置 → 模型与密钥」填写后重试")
				return
			}
		}
	}

	// M8-设置中心：任务 ID 在入队时预分配（202 响应与 review.Run 落库用同一个），
	// 设置中心生效配置（LLM key/base/model、E2B key）统一由 injectSettings 注入
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
			TaskName:     sanitizeTaskName(req.TaskName),
			InputLabel:   inputPath,
			LLMMode:      req.LLMMode,
			RulesDir:     s.cfg.RulesDir,
			DBPath:       s.cfg.DBPath,
			OutputDir:    s.cfg.DataDir,
			SandboxMode:  sandboxMode,
			AuditFile:    "tool_safety_audit.jsonl",
		},
	}
	if err := s.injectSettings(&job.opts); err != nil {
		writeErr(w, http.StatusInternalServerError, "读取设置失败: "+err.Error())
		return
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
	// M8-设置中心：上传审查同样支持 LLM 复核开关（?llm_mode=openai）与任务名（?task_name=）+ 设置注入
	if q := r.URL.Query(); q.Get("llm_mode") != "" {
		job.opts.LLMMode = q.Get("llm_mode")
	}
	job.opts.TaskName = sanitizeTaskName(r.URL.Query().Get("task_name"))
	if err := s.injectSettings(&job.opts); err != nil {
		writeErr(w, http.StatusInternalServerError, "读取设置失败: "+err.Error())
		return
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

// handleTaskRoutes 分派 /api/tasks/ 子路由：
// GET /api/tasks/{id}（详情）与 POST /api/tasks/{id}/fp-marks（标记误报）。
func (s *Server) handleTaskRoutes(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/tasks/"), "/")
	if strings.HasSuffix(rest, "/fp-marks") {
		if r.Method != http.MethodPost {
			writeErr(w, http.StatusMethodNotAllowed, "需要 POST 方法")
			return
		}
		s.handleMarkFalsePositive(w, r)
		return
	}
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "需要 GET 方法")
		return
	}
	s.handleTaskDetail(w, r)
}

// markFPRequest POST /api/tasks/{id}/fp-marks 请求体（M8-C9）。
type markFPRequest struct {
	RuleID string `json:"rule_id"`
	File   string `json:"file"`
	Line   int    `json:"line"`
}

// handleMarkFalsePositive 记录一条误报标记：影响后续审查的同模式上报。
func (s *Server) handleMarkFalsePositive(w http.ResponseWriter, r *http.Request) {
	// 路径形如 /api/tasks/{taskID}/fp-marks
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/tasks/"), "/")
	if !strings.HasSuffix(rest, "/fp-marks") {
		writeErr(w, http.StatusNotFound, "页面不存在")
		return
	}
	taskID := strings.TrimSuffix(rest, "/fp-marks")
	if taskID == "" {
		writeErr(w, http.StatusBadRequest, "缺少任务 ID")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var req markFPRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.RuleID == "" || req.File == "" {
		writeErr(w, http.StatusBadRequest, "rule_id 与 file 必填")
		return
	}

	if err := s.store.SaveFalsePositiveMark(&storage.FalsePositiveMark{
		RuleID:    req.RuleID,
		FilePath:  req.File,
		Line:      req.Line,
		TaskID:    taskID,
		CreatedAt: time.Now(),
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, "保存误报标记失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"message": "已记录误报标记，同类问题后续审查将自动降级为警告",
	})
}

// handleListFPMarks 列出全部误报标记（透明度：用户可查看已记忆的模式）。
func (s *Server) handleListFPMarks(w http.ResponseWriter, r *http.Request) {
	marks, err := s.store.ListFalsePositiveMarks()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if marks == nil {
		marks = []*storage.FalsePositiveMark{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"marks": marks, "count": len(marks)})
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
		s.serveTaskReport(w, r, strings.TrimSuffix(taskID, "/report"))
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

func (s *Server) serveTaskReport(w http.ResponseWriter, r *http.Request, taskID string) {
	jsonReport, mdReport, err := s.store.GetReport(taskID)
	if err != nil || (mdReport == "" && jsonReport == "") {
		writeErr(w, http.StatusNotFound, "报告不存在: "+taskID)
		return
	}

	// M7-F6：?format=html 即时生成自包含 HTML 报告（浏览器直接打开/转发）
	if r.URL.Query().Get("format") == "html" {
		var rep report.ReviewReport
		if err := json.Unmarshal([]byte(jsonReport), &rep); err != nil {
			writeErr(w, http.StatusInternalServerError, "解析报告失败: "+err.Error())
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Disposition",
			fmt.Sprintf("inline; filename=%s.html", taskID))
		_, _ = w.Write([]byte(rep.ToHTML()))
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
			"task_name":  t.TaskName,
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
	engine.Register(rules.NewTokenDBLifecycleRule())      // M2-D4
	engine.Register(rules.NewTokenContextCancelRule())    // R1：CTX-AST-001
	engine.Register(rules.NewTokenSQLInjectionRule())     // R1：SEC-AST-003
	engine.Register(rules.NewTokenCommandInjectionRule()) // R1：SEC-AST-004
	engine.Register(rules.NewTokenInsecureTLSRule())      // R2：SEC-AST-005
	engine.Register(rules.NewTokenContextRootRule())      // R2：CTX-AST-002
	engine.Register(rules.NewTokenMutexRule())            // R2：CON-AST-001
	engine.Register(rules.NewTokenLoopTimerRule())        // R2：RES-AST-002
	engine.Register(rules.NewTokenRowsErrRule())          // R2：DB-AST-002
	engine.Register(rules.NewGeneralSecretRule())         // W1：SEC-GEN-001
	engine.Register(rules.NewGeneralLargeDeleteRule())    // W1：DEL-GEN-001

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
