// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//
// code-review-agent 是一个自动代码审查系统。
//
// 两种运行形态（业务管线同一套，见 review 包）：
//
//  1. CLI 一次性审查：
//     code-review-agent --diff-file changes.diff
//     code-review-agent --repo-path /path/to/repo
//
//  2. HTTP 服务（内嵌 Web 前端 + REST API，供浏览器使用）：
//     code-review-agent serve --port 8080
package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"code-review-agent/review"
	"code-review-agent/server"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "serve" {
		runServe(os.Args[2:])
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "mcp" {
		runMCP(os.Args[2:])
		return
	}
	runCLI(os.Args[1:])
}

// runCLI 一次性审查：解析参数 → review.Run → 打印摘要。
// llmModeOf 合并 --fake-model 与 --llm 标志（fake 优先，用于可复现测试）。
func llmModeOf(fake bool, llm string) string {
	if fake {
		return "fake"
	}
	return llm
}

func runCLI(args []string) {
	fs := flag.NewFlagSet("code-review-agent", flag.ExitOnError)
	diffFile := fs.String("diff-file", "", "diff 文件路径")
	diffFiles := fs.String("files", "", "文件路径列表（逗号分隔，M2-D5：整体按新增行审查）")
	repoPath := fs.String("repo-path", "", "git 仓库路径")
	rulesDir := fs.String("rules-dir", "", "自定义 YAML 规则目录")
	dbPath := fs.String("db", "review.db", "SQLite 数据库路径")
	outputDir := fs.String("output", ".", "报告输出目录")
	sandboxMode := fs.String("sandbox", "container", "沙箱模式：container（手写 docker run）/ container-fx（框架 Docker SDK，M3-B3）/ e2b（云沙箱，需 E2B_API_KEY）/ local（仅开发 fallback）")
	fakeModel := fs.Bool("fake-model", false, "启用内置确定性假模型复核（M4-C2：无 API Key 跑全链路，输出可复现）")
	llmMode := fs.String("llm", "", "LLM 复核降噪（M4-C1）：openai（兼容 API，可用 --llm-base-url 指向 ollama/vLLM）")
	llmModel := fs.String("llm-model", "", "LLM 模型名（默认 gpt-4o-mini）")
	llmBaseURL := fs.String("llm-base-url", "", "OpenAI 兼容端点（如 ollama: http://localhost:11434/v1）")
	auditFile := fs.String("audit-file", "tool_safety_audit.jsonl", "安全审计日志 JSONL 路径（纯文件名落在 --output 目录下，传空禁用）")
	dryRun := fs.Bool("dry-run", false, "dry-run 模式（不写数据库、不执行沙箱）")
	verbose := fs.Bool("verbose", false, "详细输出")
	fs.Parse(args)

	if *diffFile == "" && *diffFiles == "" && *repoPath == "" {
		fmt.Fprintln(os.Stderr, "错误：必须指定 --diff-file / --files / --repo-path 之一")
		fs.Usage()
		os.Exit(1)
	}

	var fileList []string
	if *diffFiles != "" {
		for _, p := range strings.Split(*diffFiles, ",") {
			if p = strings.TrimSpace(p); p != "" {
				fileList = append(fileList, p)
			}
		}
	}

	rep, err := review.Run(review.Options{
		DiffFile:     *diffFile,
		Files:        fileList,
		RepoPath:     *repoPath,
		RulesDir:     *rulesDir,
		DBPath:       *dbPath,
		OutputDir:    *outputDir,
		SandboxMode:  *sandboxMode,
		LLMMode:      llmModeOf(*fakeModel, *llmMode),
		LLMModelName: *llmModel,
		LLMBaseURL:   *llmBaseURL,
		AuditFile:    *auditFile,
		DryRun:       *dryRun,
		Verbose:      *verbose,
	})
	if err != nil {
		if errors.Is(err, review.ErrNoChanges) {
			fmt.Println("没有变更文件，退出。")
			os.Exit(0)
		}
		log.Fatal(err)
	}

	// 汇总输出
	jsonPath := filepath.Join(*outputDir, "review_report.json")
	bySeverity := rep.Summary.BySeverity
	fmt.Printf("\n✅ 审查完成！耗时: %s\n", rep.Duration)
	fmt.Printf("   高危: %d  中危: %d  低危: %d  信息: %d\n",
		bySeverity["high"], bySeverity["medium"], bySeverity["low"], bySeverity["info"])
	fmt.Printf("   风险评分: %.0f/100 (%s)\n", rep.Monitor.RiskScore, rep.Monitor.RiskGrade)
	fmt.Printf("   报告: %s\n", jsonPath)
	if rep.SandboxSummary.TotalRuns > 0 {
		fmt.Printf("   沙箱执行: %d 次, 耗时 %s\n", rep.SandboxSummary.TotalRuns, rep.SandboxSummary.TotalDuration)
	}
	if rep.Monitor.PermissionDenied > 0 {
		fmt.Printf("   权限拦截: %d 次\n", rep.Monitor.PermissionDenied)
	}
}

// runMCP 以 stdio 模式运行 MCP 服务端（M5-C6）：供 Claude Code/Cursor 等
// MCP 客户端直连 code_review 工具。日志一律走 stderr（stdout 是协议通道）。
func runMCP(args []string) {
	fs := flag.NewFlagSet("mcp", flag.ExitOnError)
	dbPath := fs.String("db", "review.db", "SQLite 数据库路径")
	dataDir := fs.String("data-dir", ".run", "审查产物目录（报告 / 审计日志）")
	rulesDir := fs.String("rules-dir", "", "YAML 自定义规则目录")
	sandboxMode := fs.String("sandbox", "off", "仓库审查的沙箱模式：off / container / container-fx / e2b / local")
	fs.Parse(args)

	mcpSrv, err := server.NewMCPServer(server.Config{
		DBPath:      *dbPath,
		DataDir:     *dataDir,
		RulesDir:    *rulesDir,
		SandboxMode: *sandboxMode,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "初始化 MCP 服务失败: %v\n", err)
		os.Exit(1)
	}
	defer mcpSrv.Close()

	fmt.Fprintln(os.Stderr, "code-review-agent MCP server ready (stdio)")
	if err := mcpSrv.Serve(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "MCP 服务退出: %v\n", err)
		os.Exit(1)
	}
}

// runServe 启动 HTTP 服务：REST API + 内嵌 Web 前端。
func runServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	port := fs.Int("port", 8080, "HTTP 监听端口")
	dbPath := fs.String("db", "review.db", "SQLite 数据库路径")
	dataDir := fs.String("data-dir", ".run", "审查产物目录（报告 / 审计日志；默认 .run，保持工作区干净）")
	rulesDir := fs.String("rules-dir", "", "YAML 自定义规则目录")
	sandboxMode := fs.String("sandbox", "off", "仓库审查的沙箱模式：off / container / local")
	sampleDir := fs.String("sample-dir", "testdata", "示例 diff 目录（前端一键演示用，不存在则隐藏示例）")
	workers := fs.Int("queue-workers", 1, "异步审查并发 worker 数（M7-F1；默认 1 串行，SQLite 单写最稳）")
	taskTimeout := fs.Duration("task-timeout", 10*time.Minute, "单个审查任务的看门狗上限（超时标记 failed）")
	authToken := fs.String("auth-token", "", "写操作认证 token（M7-F2；设置后 POST 需携带 Authorization: Bearer <token> 或 X-Auth-Token，浏览公开；前端用户用 http://host/?token=<token> 链接自动保存）")
	fs.Parse(args)

	srv, err := server.New(server.Config{
		Port:        *port,
		DBPath:      *dbPath,
		DataDir:     *dataDir,
		RulesDir:    *rulesDir,
		SandboxMode: *sandboxMode,
		SampleDir:   *sampleDir,
		Workers:     *workers,
		TaskTimeout: *taskTimeout,
		AuthToken:   *authToken,
	})
	if err != nil {
		log.Fatalf("初始化服务失败: %v", err)
	}

	fmt.Printf("🚀 Code Review Agent 服务已启动: http://localhost:%d\n", *port)
	fmt.Printf("   数据库: %s | 产物目录: %s | 审查队列: %d worker, 单任务上限 %s\n",
		*dbPath, *dataDir, *workers, *taskTimeout)
	if *authToken != "" {
		fmt.Printf("   🔐 认证已启用：写操作需 token，带 token 的前端入口 http://localhost:%d/?token=<token>\n", *port)
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("HTTP 服务退出: %v", err)
	}
}

// trigger non-empty diff
