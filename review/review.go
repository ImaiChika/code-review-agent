// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//
// Package review 组合 diff 解析、规则引擎、沙箱执行、评分、报告生成和存储，
// 提供一次完整代码审查的管线入口（原 main.go 的 8 步流程）。
//
// CLI（main.go）与 HTTP API（server 包）共用本入口，
// 保证两种形态的业务逻辑完全一致。
package review

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/model/openai"
	"trpc.group/trpc-go/trpc-agent-go/tool"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"code-review-agent/diff"
	"code-review-agent/findings"
	"code-review-agent/llmreview"
	"code-review-agent/report"
	"code-review-agent/rules"
	"code-review-agent/safety"
	"code-review-agent/sandbox"
	"code-review-agent/scoring"
	"code-review-agent/storage"
)

// tracer 动态解析全局 TracerProvider（M1-B6）：
// 默认 noop 不产生开销；框架 telemetry/trace.Start() 或测试注入 provider 后，
// span 自动进入导出管线（OTEL_EXPORTER_OTLP_ENDPOINT 配置导出端点）。
func tracer() trace.Tracer {
	return otel.GetTracerProvider().Tracer("code-review-agent")
}

// buildLLMModel 按配置构造 LLM 复核模型（M4-C2）。
//
// fake → 内置确定性假模型（无网络，官方要求的 --fake-model 可复现模式）；
// openai → OpenAI 兼容 API（--llm-base-url 可指向 ollama/vLLM 等兼容端点，
// 需要 OPENAI_API_KEY 环境变量）。
func buildLLMModel(opts Options) (model.Model, error) {
	switch opts.LLMMode {
	case "fake":
		fm := llmreview.NewFakeModel()
		for _, resp := range opts.LLMFakeResponses {
			fm.PushText(resp)
		}
		return fm, nil
	case "openai":
		apiKey := os.Getenv("OPENAI_API_KEY")
		if apiKey == "" {
			return nil, errors.New("openai 复核需要 OPENAI_API_KEY 环境变量")
		}
		name := opts.LLMModelName
		if name == "" {
			name = "gpt-4o-mini"
		}
		o := []openai.Option{openai.WithAPIKey(apiKey)}
		if opts.LLMBaseURL != "" {
			o = append(o, openai.WithBaseURL(opts.LLMBaseURL))
		}
		return openai.New(name, o...), nil
	default:
		return nil, fmt.Errorf("未知 LLM 模式: %q", opts.LLMMode)
	}
}

// randHex 生成 n 字节的随机 hex 字符串（task_id 去重后缀用）。
func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano()%0xffff)
	}
	return hex.EncodeToString(b)
}

// ErrNoChanges 输入中没有可审查的变更（解析不出文件，或所有文件都没有新增行）。
var ErrNoChanges = errors.New("没有变更文件")

// ErrInvalidInput 输入本身不可用（diff 文件读取失败 / 仓库路径无效 / 文件列表读取失败）。
// API 层据此映射 400，与"输入合法但没有变更"（ErrNoChanges → 422）区分。
var ErrInvalidInput = errors.New("输入不可用")

// SandboxOff 关闭沙箱执行（API 模式默认值；CLI 由 --sandbox flag 控制）。
const SandboxOff = "off"

// Options 一次审查的全部输入。
//
// 输入来源优先级：DiffFile > DiffContent > Files > RepoPath（四选一）。
type Options struct {
	DiffFile         string   // diff 文件路径
	DiffContent      string   // 内存中的 diff 文本（HTTP API 上传）
	Files            []string // 文件路径列表（M2-D5：整体按新增行审查）
	RepoPath         string   // git 仓库路径（取未提交变更；此模式才可能触发沙箱）
	RulesDir         string   // YAML 自定义规则目录（可空）
	DBPath           string   // SQLite 路径
	OutputDir        string   // 报告输出目录
	SandboxMode      string   // "container" / "container-fx" / "e2b" / "local" / "off"；空 = container
	LLMMode          string   // LLM 复核模式（M4）："fake"（确定性回放）/ "openai"（兼容 API）；空 = 关闭
	LLMModelName     string   // openai 模式模型名（默认 gpt-4o-mini）
	LLMBaseURL       string   // openai 兼容端点（ollama: http://localhost:11434/v1）
	LLMFakeResponses []string // fake 模式的预设判定响应（按序回放；空 = 默认全 CONFIRM；测试/脚本用）
	AuditFile        string   // 审计日志路径；空 = 默认 tool_safety_audit.jsonl 落 OutputDir
	SkillsDir        string   // CR Skill 目录（M1-B1；空 = 自动探测 ./skills，找不到则报告不含 skill 元数据）
	TaskID           string   // 预分配的任务 ID（M7-F1 异步队列用；空 = 自动生成）
	DryRun           bool     // 不写数据库、不执行沙箱
	Verbose          bool     // 过程日志打到 stdout
}

// NewTaskID 生成任务 ID：秒级时间戳 + 随机后缀。
// 服务模式下同一秒可能有多个请求，随机后缀防止撞 cr_review_tasks 唯一约束。
func NewTaskID() string {
	return fmt.Sprintf("task-%s-%s", time.Now().Format("20060102-150405"), randHex(3))
}

// Run 执行一次完整审查，返回最终报告。
//
// 流程：读 diff → 规则引擎 → （可选）沙箱 → 去重 → 评分 → 报告 → 落库。
// 报告文件（review_report.json/md）写入 opts.OutputDir。
func Run(opts Options) (reviewReport *report.ReviewReport, err error) {
	if opts.DiffFile == "" && opts.DiffContent == "" && len(opts.Files) == 0 && opts.RepoPath == "" {
		return nil, errors.New("必须指定 DiffFile / DiffContent / Files / RepoPath 之一")
	}
	sandboxMode := opts.SandboxMode
	if sandboxMode == "" {
		sandboxMode = "container"
	}

	if err := EnsureOutputDir(opts.OutputDir); err != nil {
		return nil, err
	}

	start := time.Now()
	// M7-F1：异步模式下任务 ID 由队列预分配（202 响应要用），透传进来
	taskID := opts.TaskID
	if taskID == "" {
		taskID = NewTaskID()
	}
	ctx := context.Background()
	counters := &reviewCounters{}

	// M1-B6：审查主流程 span（框架遥测管线；属性随流程推进逐步补齐）
	ctx, span := tracer().Start(ctx, "review.run",
		trace.WithAttributes(attribute.String("review.task_id", taskID)))
	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		span.End()
	}()

	if opts.Verbose {
		fmt.Printf("🔍 开始审查任务: %s\n", taskID)
	}

	// ========== Step 1: 读取 diff ==========
	var files []diff.FileDiff
	var inputType, inputPath string

	switch {
	case opts.DiffFile != "":
		inputType = "diff_file"
		inputPath = opts.DiffFile
		var err error
		files, err = diff.ReadFromFile(opts.DiffFile)
		if err != nil {
			return nil, fmt.Errorf("%w: 读取 diff 文件失败: %w", ErrInvalidInput, err)
		}
	case opts.DiffContent != "":
		inputType = "diff_content"
		inputPath = "api-upload"
		var err error
		files, err = diff.ReadFromContent(opts.DiffContent)
		if err != nil {
			return nil, fmt.Errorf("%w: 解析 diff 内容失败: %w", ErrInvalidInput, err)
		}
	case len(opts.Files) > 0:
		// M2-D5：文件路径列表输入（整体按新增行审查）
		inputType = "files"
		inputPath = strings.Join(opts.Files, ",")
		var err error
		files, err = diff.ReadFromFilePaths(opts.Files)
		if err != nil {
			return nil, fmt.Errorf("%w: 读取文件列表失败: %w", ErrInvalidInput, err)
		}
	default:
		inputType = "repo_path"
		inputPath = opts.RepoPath
		var err error
		files, err = diff.ReadFromGitDiff(opts.RepoPath)
		if err != nil {
			return nil, fmt.Errorf("%w: 读取 git diff 失败: %w", ErrInvalidInput, err)
		}
	}

	if len(files) == 0 {
		return nil, ErrNoChanges
	}
	// M7-F8（P2-10）：所有文件都没有新增行（纯上下文/纯删除的 diff）时，
	// 与"解析不出文件"同语义返回 ErrNoChanges——规则只扫新增行，
	// 空报告不如明确告诉调用方"没有可审查的变更"。
	addedTotal := 0
	for i := range files {
		addedTotal += len(files[i].AddedLines())
	}
	if addedTotal == 0 {
		return nil, ErrNoChanges
	}

	goFiles := diff.ChangedGoFiles(files)
	if opts.Verbose {
		fmt.Printf("📄 变更文件: %d 个（其中 Go 文件 %d 个）\n", len(files), len(goFiles))
	}

	// ========== Step 2: 初始化规则引擎 ==========
	engine := rules.NewEngine()

	// 注册 Token 感知规则
	engine.Register(rules.NewTokenSecretRule())
	engine.Register(rules.NewTokenLeakRule())
	engine.Register(rules.NewTokenGoroutineRule())
	engine.Register(rules.NewTokenResourceRule())
	engine.Register(rules.NewTokenErrorRule())
	engine.Register(rules.NewTokenMissingTestRule())
	engine.Register(rules.NewTokenDBLifecycleRule()) // M2-D4

	// 加载 YAML 自定义规则
	if opts.RulesDir != "" {
		dslRules, err := rules.LoadDSLRules(opts.RulesDir)
		if err != nil {
			return nil, fmt.Errorf("加载 YAML 规则失败: %w", err)
		}
		engine.RegisterAll(dslRules...)
		if opts.Verbose {
			fmt.Printf("📋 加载了 %d 条 YAML 自定义规则\n", len(dslRules))
		}
	}

	if opts.Verbose {
		fmt.Printf("⚙️  规则引擎: 已注册 %d 条规则\n", len(engine.Rules()))
	}

	// ========== Step 3: 执行审查 ==========
	ruleStart := time.Now()
	allFindings, err := engine.Run(files)
	ruleDuration := time.Since(ruleStart)
	if err != nil {
		counters.exceptions++
		log.Printf("⚠️ 规则执行出错: %v", err)
	}
	span.SetAttributes(
		attribute.Int("review.rules", len(engine.Rules())),
		attribute.Int("review.findings_raw", len(allFindings)),
	)

	if opts.Verbose {
		fmt.Printf("🔎 发现 %d 个问题（去重前）\n", len(allFindings))
	}

	// ========== Step 3.5: 沙箱执行（对 Go 文件，仅 repo_path 模式）==========
	if !opts.DryRun && sandboxMode != SandboxOff && len(goFiles) > 0 && opts.RepoPath != "" {
		runSandbox(ctx, opts, sandboxMode, taskID, counters)
	} else if opts.DryRun && opts.Verbose {
		fmt.Println("⏭️  dry-run 模式，跳过沙箱执行")
	}

	// M3-D2：沙箱内静态工具（staticcheck）的发现并入，一起参与去重
	allFindings = append(allFindings, counters.toolFindings...)

	// ========== Step 4: 去重和分组 ==========
	dedupResult := findings.Deduplicate(allFindings)

	// ========== Step 4.5: LLM 复核降噪（M4-C1/C2，默认关闭） ==========
	// 注：报告对象在 Step 6 才创建，复核统计先存局部变量
	var llmMode string
	var llmReviewed, llmDropped int
	if opts.LLMMode != "" {
		llmMode = opts.LLMMode
		if mdl, merr := buildLLMModel(opts); merr != nil {
			log.Printf("⚠️ LLM 复核未启用: %v", merr)
		} else {
			kept, stats := llmreview.Review(ctx, mdl, dedupResult.Findings)
			dedupResult.Findings = kept
			llmReviewed, llmDropped = stats.Reviewed, stats.Dropped
			if stats.Error != "" {
				log.Printf("⚠️ LLM 复核失败（保守保留全部候选）: %s", stats.Error)
			}
			if opts.Verbose {
				fmt.Printf("🤖 LLM 复核: 送审 %d, 剔除 %d\n", stats.Reviewed, stats.Dropped)
			}
		}
	}

	if opts.Verbose {
		fmt.Printf("📊 去重后: %d 个高置信度发现, %d 个低置信度警告, %d 个被移除\n",
			len(dedupResult.Findings), len(dedupResult.Warnings), dedupResult.Removed)
	}

	// ========== Step 5: 风险评分 ==========
	riskScore := scoring.Calculate(dedupResult.Findings, dedupResult.Warnings)
	span.SetAttributes(
		attribute.String("review.input_type", inputType),
		attribute.Int("review.files_scanned", len(files)),
		attribute.Int("review.findings_total", len(dedupResult.Findings)),
		attribute.Int("review.warnings_total", len(dedupResult.Warnings)),
		attribute.Float64("review.risk_score", riskScore.Score),
		attribute.String("review.risk_grade", riskScore.Grade),
	)

	if opts.Verbose {
		fmt.Printf("\n%s\n", riskScore.ToReport())
	}

	// ========== Step 6: 生成报告 ==========
	reviewReport = report.NewReport(taskID, inputType, inputPath)
	reviewReport.SetResult(dedupResult, len(files), len(goFiles))

	// 填充监控信息
	reviewReport.Monitor.TotalDuration = time.Since(start).Round(time.Millisecond).String()
	reviewReport.Monitor.RuleDuration = ruleDuration.Round(time.Millisecond).String()
	reviewReport.Monitor.SandboxDuration = counters.sandboxDuration.Round(time.Millisecond).String()
	// 工具调用次数 = 沙箱实际执行的命令数（此前误用 findings 数，语义失真）
	reviewReport.Monitor.ToolCallCount = len(counters.sandboxRuns)
	reviewReport.Monitor.RuleCount = len(engine.Rules())
	reviewReport.Monitor.FilesScanned = len(files)
	reviewReport.Monitor.PermissionDenied = counters.permissionDenied
	reviewReport.Monitor.ExceptionCount = counters.exceptions
	reviewReport.Monitor.LLMMode = llmMode
	reviewReport.Monitor.LLMReviewed = llmReviewed
	reviewReport.Monitor.LLMDropped = llmDropped
	reviewReport.Monitor.RiskScore = riskScore.Score
	reviewReport.Monitor.RiskGrade = riskScore.Grade

	// 填充沙箱执行记录
	reviewReport.SandboxRuns = make([]report.SandboxRun, len(counters.sandboxRuns))
	var sandboxSuccess, sandboxFailed int
	for i, run := range counters.sandboxRuns {
		reviewReport.SandboxRuns[i] = report.SandboxRun{
			Command:   run.Command,
			Backend:   run.Backend,
			ExitCode:  run.ExitCode,
			Output:    run.Output,
			Truncated: run.Truncated,
			Duration:  run.Duration,
		}
		if run.ExitCode == 0 {
			sandboxSuccess++
		} else {
			sandboxFailed++
		}
	}

	// 填充治理拦截摘要
	var deniedCommands []string
	totalPermChecks := len(counters.permissionDecisions)
	permAllowed := 0
	permDenied := 0
	permAsk := 0
	for _, dec := range counters.permissionDecisions {
		switch safety.Decision(dec.Action) {
		case safety.DecisionAllow:
			permAllowed++
		case safety.DecisionDeny:
			permDenied++
			deniedCommands = append(deniedCommands, dec.Command)
		case safety.DecisionAsk:
			permAsk++
		}
	}
	reviewReport.SetGovernance(totalPermChecks, permAllowed, permDenied, permAsk, deniedCommands)

	// 填充沙箱执行摘要
	reviewReport.SetSandboxSummary(
		len(counters.sandboxRuns),
		sandboxSuccess,
		sandboxFailed,
		counters.sandboxTimedOut,
		counters.sandboxDuration.Round(time.Millisecond).String(),
	)

	reviewReport.Finalize(start)

	// M1-B1：加载 CR Skill 元数据写入报告（探测不到 skills 目录则不写该字段）
	if skillsDir := resolveSkillsDir(opts.SkillsDir); skillsDir != "" {
		reviewReport.Skill = LoadSkillMeta(skillsDir)
	}

	var jsonPath, mdPath string
	// writeReports 把（当前内存状态的）报告写盘；产物计数更新后需再次调用，
	// 保证文件与库中的 artifacts_saved/rejected 是最终值。
	writeReports := func() error {
		jsonPath = filepath.Join(opts.OutputDir, "review_report.json")
		if err := reviewReport.WriteJSON(jsonPath); err != nil {
			return fmt.Errorf("写入 JSON 报告失败: %w", err)
		}
		mdPath = filepath.Join(opts.OutputDir, "review_report.md")
		if err := reviewReport.WriteMarkdown(mdPath); err != nil {
			return fmt.Errorf("写入 Markdown 报告失败: %w", err)
		}
		if opts.Verbose {
			fmt.Printf("📝 报告已生成: %s, %s\n", jsonPath, mdPath)
		}
		return nil
	}

	// ========== Step 7: 存储到数据库 + 产物入库 ==========
	if !opts.DryRun {
		store, err := storage.NewSQLiteStore(opts.DBPath)
		if err != nil {
			return nil, fmt.Errorf("打开数据库失败: %w", err)
		}
		defer store.Close()

		// 保存任务
		task := &storage.ReviewTask{
			TaskID:       taskID,
			Status:       storage.TaskStatusCompleted,
			InputType:    inputType,
			InputPath:    inputPath,
			FilesCount:   len(files),
			GoFilesCount: len(goFiles),
			StartedAt:    start,
		}
		if err := store.CreateTask(task); err != nil {
			return nil, fmt.Errorf("保存任务失败: %w", err)
		}

		// 保存 findings
		if err := store.SaveFindings(taskID, dedupResult.Findings); err != nil {
			return nil, fmt.Errorf("保存 findings 失败: %w", err)
		}

		// 保存沙箱执行记录
		for _, run := range counters.sandboxRuns {
			if err := store.SaveSandboxRun(&run); err != nil {
				log.Printf("⚠️ 保存沙箱执行记录失败: %v", err)
			}
		}

		// 保存权限决策记录
		for _, dec := range counters.permissionDecisions {
			if err := store.SavePermissionDecision(&dec); err != nil {
				log.Printf("⚠️ 保存权限决策记录失败: %v", err)
			}
		}

		// M1-B5：产物入库（报告 JSON/MD + 沙箱输出），带数量/大小/扩展名限制。
		// 顺序：先入库产物 → 更新 Monitor 计数 → 重新序列化写文件/落报告，
		// 保证报告里的 artifacts_saved/rejected 是最终值。
		jsonData, err := json.MarshalIndent(reviewReport, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("序列化 JSON 报告失败: %w", err)
		}
		arts := collectArtifacts(taskID, jsonData, []byte(reviewReport.ToMarkdown()), counters.sandboxRuns)
		saved, rejected := saveArtifacts(store, arts)
		reviewReport.Monitor.ArtifactsSaved = saved
		reviewReport.Monitor.ArtifactsRejected = len(rejected)
		for _, reason := range rejected {
			log.Printf("⚠️ 产物被拒: %s", reason)
		}
		if opts.Verbose {
			fmt.Printf("📦 产物入库: %d 保存, %d 被拒\n", saved, len(rejected))
		}

		// 重新序列化（带产物计数）后写文件 + 落报告
		if err := writeReports(); err != nil {
			return nil, err
		}
		jsonData, err = os.ReadFile(jsonPath)
		if err != nil {
			return nil, fmt.Errorf("读取报告失败: %w", err)
		}
		mdData, err := os.ReadFile(mdPath)
		if err != nil {
			return nil, fmt.Errorf("读取报告失败: %w", err)
		}
		if err := store.SaveReport(taskID, string(jsonData), string(mdData)); err != nil {
			return nil, fmt.Errorf("保存报告失败: %w", err)
		}

		if opts.Verbose {
			fmt.Printf("💾 已保存到数据库: %s (task: %s)\n", opts.DBPath, taskID)
		}
	} else {
		// dry-run：只写报告文件，不落库、不入产物
		if err := writeReports(); err != nil {
			return nil, err
		}
		if opts.Verbose {
			fmt.Println("⏭️  dry-run 模式，跳过数据库写入")
		}
	}

	// ========== Step 8: 汇总 ==========
	if opts.Verbose {
		duration := time.Since(start)
		fmt.Printf("\n✅ 审查完成！耗时: %s\n", duration.Round(time.Millisecond))
		fmt.Printf("   风险评分: %.0f/100 (%s)\n", riskScore.Score, riskScore.Grade)
		fmt.Printf("   报告: %s\n", jsonPath)
		if len(counters.sandboxRuns) > 0 {
			fmt.Printf("   沙箱执行: %d 次, 耗时 %s\n", len(counters.sandboxRuns), counters.sandboxDuration.Round(time.Millisecond))
		}
		if counters.permissionDenied > 0 {
			fmt.Printf("   权限拦截: %d 次\n", counters.permissionDenied)
		}
	}

	return reviewReport, nil
}

// reviewCounters 汇总一次审查中的沙箱/治理监控计数。
type reviewCounters struct {
	sandboxDuration     time.Duration
	permissionDenied    int
	exceptions          int
	sandboxTimedOut     int
	sandboxRuns         []storage.SandboxRun
	permissionDecisions []storage.PermissionDecision
	toolFindings        []findings.Finding // 沙箱内静态工具产出（M3-D2 staticcheck）
}

// runSandbox 对仓库执行 go vet / go test：命令先过安全过滤器，
// allow 才进沙箱；deny/ask 记录进权限决策。全程更新监控计数。
func runSandbox(ctx context.Context, opts Options, sandboxMode, taskID string, c *reviewCounters) {
	if opts.Verbose {
		fmt.Printf("🧪 开始沙箱执行 (%s 模式)...\n", sandboxMode)
	}

	// 安全审计日志默认落盘（修 P1-6：验收要求的 tool_safety_audit.jsonl 此前不产生）
	filterCfg := safety.DefaultConfig()
	filterCfg.LogFile = ResolveAuditPath(opts.AuditFile, opts.OutputDir)
	filter := safety.NewSafetyFilter(filterCfg)
	// M1-B2：SafetyFilter 适配为框架权限策略，治理决策走框架语义
	policy := filter.AsPermissionPolicy()

	var sb sandbox.Sandbox

	switch sandboxMode {
	case "container":
		var err error
		sb, err = sandbox.NewContainerSandbox("")
		if err != nil {
			log.Printf("⚠️ 创建容器沙箱失败，回退到本地: %v", err)
			c.exceptions++
			sb, err = sandbox.NewLocalSandbox("")
			if err != nil {
				log.Printf("⚠️ 创建本地沙箱也失败: %v", err)
				c.exceptions++
			}
		}
	case "container-fx":
		// M3-B3：框架 codeexecutor/container 子模块后端
		var err error
		sb, err = sandbox.NewFrameworkContainerSandbox(opts.RepoPath)
		if err != nil {
			log.Printf("⚠️ 创建框架容器沙箱失败，回退到本地: %v", err)
			c.exceptions++
			sb, err = sandbox.NewLocalSandbox("")
			if err != nil {
				log.Printf("⚠️ 创建本地沙箱也失败: %v", err)
				c.exceptions++
			}
		}
	case "e2b":
		// M3-B4：E2B 云沙箱（需 E2B_API_KEY；默认模板无 Go 工具链，需 E2B_TEMPLATE）
		var err error
		sb, err = sandbox.NewE2BSandbox()
		if err != nil {
			log.Printf("⚠️ 创建 E2B 沙箱失败，回退到本地: %v", err)
			c.exceptions++
			sb, err = sandbox.NewLocalSandbox("")
			if err != nil {
				log.Printf("⚠️ 创建本地沙箱也失败: %v", err)
				c.exceptions++
			}
		}
	default: // "local"
		var err error
		sb, err = sandbox.NewLocalSandbox("")
		if err != nil {
			log.Printf("⚠️ 创建本地沙箱失败: %v", err)
			c.exceptions++
		}
	}

	if sb == nil {
		return
	}
	defer sb.Close()

	// 对 Go 项目执行 go vet 和 go test
	// M3-D2：staticcheck 已预装在沙箱镜像（Dockerfile）；本地模式未安装时该命令失败但不影响流程
	sandboxCmds := []string{"go vet ./...", "go test -count=1 -timeout=30s ./...", "staticcheck ./..."}

	for _, cmd := range sandboxCmds {
		// M1-B6：每条沙箱命令一个子 span，决策/退出码/超时全部落 span 属性
		cmdCtx, cmdSpan := tracer().Start(ctx, "sandbox.exec", trace.WithAttributes(
			attribute.String("sandbox.command", cmd),
			attribute.String("sandbox.backend", sb.Name()),
		))

		// M1-B2：命令先经框架权限体系检查（SafetyFilter 适配为 tool.PermissionPolicy）
		reqArgs, _ := json.Marshal(map[string]string{"command": cmd})
		fwDecision, err := policy.CheckToolPermission(cmdCtx, &tool.PermissionRequest{
			ToolName:  "sandbox",
			Arguments: reqArgs,
		})
		if err != nil {
			c.exceptions++
			cmdSpan.RecordError(err)
			cmdSpan.SetStatus(codes.Error, err.Error())
			cmdSpan.End()
			log.Printf("⚠️ 权限检查失败 (%s): %v", cmd, err)
			continue
		}
		cmdSpan.SetAttributes(attribute.String("tool.safety.decision", string(fwDecision.Action)))

		// 框架决策 → 审计记录（deny/ask 不进沙箱，全部落库可查）
		c.permissionDecisions = append(c.permissionDecisions, storage.PermissionDecision{
			TaskID:    taskID,
			ToolName:  "sandbox",
			Command:   cmd,
			Action:    string(fwDecision.Action),
			Reason:    fwDecision.Reason,
			DecidedAt: time.Now(),
		})

		switch fwDecision.Action {
		case tool.PermissionActionDeny:
			c.permissionDenied++
			cmdSpan.End()
			if opts.Verbose {
				fmt.Printf("  🚫 命令被拒绝: %s (%s)\n", cmd, fwDecision.Reason)
			}
			continue
		case tool.PermissionActionAsk:
			cmdSpan.End()
			if opts.Verbose {
				fmt.Printf("  ❓ 命令需要人工确认: %s (%s)\n", cmd, fwDecision.Reason)
			}
			continue
		}

		// 执行沙箱命令
		sandboxStart := time.Now()
		result, err := sb.Execute(cmdCtx, sandbox.ExecuteOptions{
			Command:   cmd,
			WorkDir:   opts.RepoPath,
			Timeout:   30 * time.Second,
			MaxOutput: 1024 * 1024,
		})
		c.sandboxDuration += time.Since(sandboxStart)

		// 记录沙箱执行
		run := storage.SandboxRun{
			TaskID:    taskID,
			Command:   cmd,
			Backend:   sb.Name(),
			StartedAt: sandboxStart,
		}

		if err != nil {
			c.exceptions++
			run.ExitCode = -1
			run.Output = fmt.Sprintf("执行错误: %v", err)
			run.Duration = time.Since(sandboxStart).Round(time.Millisecond).String()
			log.Printf("⚠️ 沙箱执行失败 (%s): %v", cmd, err)
		} else {
			run.ExitCode = result.ExitCode
			run.Output = result.Output
			run.Truncated = result.Truncated
			run.Duration = result.Duration

			if result.TimedOut {
				c.exceptions++
				c.sandboxTimedOut++
				if opts.Verbose {
					fmt.Printf("  ⏰ 命令超时: %s\n", cmd)
				}
			}
		}

		// M1-B6：结果落子 span
		if err != nil {
			cmdSpan.RecordError(err)
			cmdSpan.SetStatus(codes.Error, err.Error())
		} else {
			cmdSpan.SetAttributes(
				attribute.Int("sandbox.exit_code", result.ExitCode),
				attribute.Bool("sandbox.timed_out", result.TimedOut),
			)
		}
		cmdSpan.End()

		// M3-D2：staticcheck 输出解析为 findings（exit 0=无问题 / 1=发现问题，>1=工具错误）
		if cmd == "staticcheck ./..." && err == nil && (result.ExitCode == 0 || result.ExitCode == 1) {
			parsed := parseStaticcheckOutput(result.Output)
			c.toolFindings = append(c.toolFindings, parsed...)
			if opts.Verbose {
				fmt.Printf("  🔧 staticcheck: %s\n", staticcheckSummary(parsed))
			}
		}

		c.sandboxRuns = append(c.sandboxRuns, run)

		if opts.Verbose {
			status := "✅"
			if run.ExitCode != 0 {
				status = "❌"
			}
			fmt.Printf("  %s %s (退出码: %d, 耗时: %s)\n", status, cmd, run.ExitCode, run.Duration)
		}
	}
}

// EnsureOutputDir 确保报告输出目录存在，不存在则自动创建（M0-A2）。
func EnsureOutputDir(dir string) error {
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("创建输出目录 %s 失败: %w", dir, err)
	}
	return nil
}

// ResolveAuditPath 解析审计日志路径：空串表示禁用；
// 纯文件名（不含目录部分）落到输出目录下，与报告同处；含目录部分的路径按用户指定使用。
func ResolveAuditPath(auditFlag, outputDir string) string {
	if auditFlag == "" {
		return ""
	}
	if filepath.Dir(auditFlag) == "." {
		return filepath.Join(outputDir, auditFlag)
	}
	return auditFlag
}
