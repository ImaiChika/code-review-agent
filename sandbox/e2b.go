// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"trpc.group/trpc-go/trpc-agent-go/codeexecutor"
	"trpc.group/trpc-go/trpc-agent-go/codeexecutor/e2b"

	"code-review-agent/safety"
)

// E2BSandbox 基于 E2B 云沙箱的后端（M3-B4）。
//
// 与容器后端的本质差异：E2B 是云端沙箱，无法 bind mount 本地目录，
// 仓库需要先经 PutDirectory 上传（staging）再执行——因此只适合
// 仓库规模可控的场景；首次 Execute 会触发一次全量上传。
//
// 环境要求：
//   - E2B_API_KEY 必须设置（未设置时构造返回错误，由调用方降级 container → local）
//   - 默认模板 code-interpreter-v1 是 Python 环境，没有 Go 工具链；
//     要跑 go vet / go test 需要用 e2b.WithTemplate 指定带 Go 的自定义模板
//     （通过环境变量 E2B_TEMPLATE 配置）
type E2BSandbox struct {
	exec *e2b.CodeExecutor

	ws        codeexecutor.Workspace
	stagedSrc string // 已上传的本地仓库路径（避免重复 staging）
}

// NewE2BSandbox 创建 E2B 云沙箱。
//
// 模板选择：E2B_TEMPLATE 环境变量（默认框架内置模板，无 Go 工具链）。
func NewE2BSandbox() (*E2BSandbox, error) {
	apiKey := os.Getenv("E2B_API_KEY")
	if apiKey == "" {
		return nil, errors.New("E2B_API_KEY 未设置，无法使用 E2B 云沙箱")
	}

	opts := []e2b.Option{e2b.WithAPIKey(apiKey)}
	if tpl := os.Getenv("E2B_TEMPLATE"); tpl != "" {
		opts = append(opts, e2b.WithTemplate(tpl))
	}

	exec, err := e2b.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("创建 E2B 沙箱失败: %w", err)
	}

	return &E2BSandbox{exec: exec}, nil
}

// Name 返回沙箱后端名称。
func (s *E2BSandbox) Name() string {
	return "e2b"
}

// Execute 在 E2B 沙箱中执行命令。
//
// 首次执行时把 opts.WorkDir 指向的本地目录上传到沙箱工作区的 repo/ 子目录，
// 后续执行复用（同一 WorkDir 不重复上传）。
func (s *E2BSandbox) Execute(ctx context.Context, opts ExecuteOptions) (*ExecuteResult, error) {
	if err := opts.Validate(); err != nil {
		return nil, err
	}

	if err := s.ensureStaged(ctx, opts.WorkDir); err != nil {
		return nil, err
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	spec := codeexecutor.RunProgramSpec{
		Cmd:     "sh",
		Args:    []string{"-c", opts.Command},
		Cwd:     "repo", // 相对 ws.Path，见框架 workspace runtime 的 cwd 解析
		Env:     opts.Env,
		Timeout: opts.Timeout,
	}

	start := time.Now()
	runResult, runErr := s.exec.RunProgram(timeoutCtx, s.ws, spec)
	if runErr != nil {
		return nil, fmt.Errorf("E2B 沙箱执行失败: %w", runErr)
	}
	duration := time.Since(start)

	result := &ExecuteResult{
		ExitCode: runResult.ExitCode,
		Stdout:   runResult.Stdout,
		Stderr:   runResult.Stderr,
		Duration: duration.Round(time.Millisecond).String(),
		Backend:  "e2b",
	}
	result.Output = runResult.Stdout
	if runResult.Stderr != "" {
		result.Output += "\n" + runResult.Stderr
	}
	result.TimedOut = runResult.TimedOut
	if result.TimedOut {
		result.ExitCode = -1
		result.Output += "\n[sandbox] 命令超时，已被终止"
	}

	// 先脱敏，再截断
	result.Output = safety.MaskSensitiveInfo(result.Output)
	result.Stdout = safety.MaskSensitiveInfo(result.Stdout)
	result.Stderr = safety.MaskSensitiveInfo(result.Stderr)

	if opts.MaxOutput > 0 && len(result.Output) > opts.MaxOutput {
		result.Output = result.Output[:opts.MaxOutput]
		result.Truncated = true
		result.Output += "\n[sandbox] 输出已被截断"
	}

	return result, nil
}

// ensureStaged 把仓库目录上传到沙箱（首次或 WorkDir 变化时）。
func (s *E2BSandbox) ensureStaged(ctx context.Context, repoPath string) error {
	if repoPath == "" {
		return errors.New("E2B 沙箱需要仓库路径（WorkDir）才能执行")
	}
	if s.ws.Path != "" && s.stagedSrc == repoPath {
		return nil
	}

	ws, err := s.exec.CreateWorkspace(ctx, fmt.Sprintf("cra-%d", time.Now().UnixNano()),
		codeexecutor.WorkspacePolicy{})
	if err != nil {
		return fmt.Errorf("创建 E2B 工作区失败: %w", err)
	}
	if err := s.exec.PutDirectory(ctx, ws, repoPath, "repo"); err != nil {
		_ = s.exec.Cleanup(ctx, ws)
		return fmt.Errorf("上传仓库到 E2B 沙箱失败: %w", err)
	}

	s.ws = ws
	s.stagedSrc = repoPath
	return nil
}

// Close 关闭 E2B 沙箱（释放云端资源）。
func (s *E2BSandbox) Close() error {
	return s.exec.Close()
}
