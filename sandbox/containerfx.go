// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
package sandbox

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"trpc.group/trpc-go/trpc-agent-go/codeexecutor"
	fxcontainer "trpc.group/trpc-go/trpc-agent-go/codeexecutor/container"

	dockercontainer "github.com/docker/docker/api/types/container"

	"code-review-agent/safety"
)

// FrameworkContainerSandbox 基于框架 codeexecutor/container 子模块的容器沙箱（M3-B3）。
//
// 与手写版 ContainerSandbox（exec docker run，--sandbox container）并存、互为对照：
//   - container    手写版：每次 Execute 起一个一次性容器（--rm），安全 flags 显式
//   - container-fx 框架版：Docker SDK 常驻容器（tail -f 保活），同一套 Sandbox 接口，
//     安全配置等价（network=none / 内存 CPU 限额 / 只读 rootfs / tmpfs / 非 root / 只读挂载）
//
// 安全配置（与手写版逐项对齐）：
//   - NetworkMode "none"：禁止网络
//   - Memory 512m / NanoCPUs 1：资源限额
//   - ReadonlyRootfs + Tmpfs /tmp：只读文件系统 + 临时可写目录
//   - User 65532:65532：非 root 执行
//   - Binds repoPath:/workspace:ro：仓库只读挂载
type FrameworkContainerSandbox struct {
	exec *fxcontainer.CodeExecutor
	name string // 容器名（Close 时显式 stop）
}

// NewFrameworkContainerSandbox 创建框架容器沙箱。
//
// 参数 repoPath 是宿主机仓库路径，将以只读方式挂载到容器 /workspace。
func NewFrameworkContainerSandbox(repoPath string) (*FrameworkContainerSandbox, error) {
	if strings.TrimSpace(repoPath) == "" {
		return nil, fmt.Errorf("框架容器沙箱需要仓库路径（bind mount 源）")
	}
	if err := checkDockerAvailable(); err != nil {
		return nil, fmt.Errorf("Docker 不可用: %w", err)
	}

	name := fmt.Sprintf("cra-fx-%d", time.Now().UnixNano()%1e9)
	exec, err := fxcontainer.New(
		fxcontainer.WithContainerName(name),
		fxcontainer.WithContainerConfig(dockercontainer.Config{
			Image:      "golang:1.21-alpine",
			WorkingDir: "/workspace",
			Cmd:        []string{"tail", "-f", "/dev/null"},
			Tty:        true,
			OpenStdin:  true,
			User:       "65532:65532", // 非 root
		}),
		// WithHostConfig 整体替换，安全项全部显式给出（默认 NetworkMode=none 会被覆盖，须重申）
		fxcontainer.WithHostConfig(dockercontainer.HostConfig{
			AutoRemove:     true,
			Privileged:     false,
			NetworkMode:    "none",
			ReadonlyRootfs: true,
			Tmpfs:          map[string]string{"/tmp": "size=256m,mode=1777"},
			Resources: dockercontainer.Resources{
				Memory:   512 << 20, // 512m
				NanoCPUs: 1e9,       // 1 CPU
			},
			Binds: []string{repoPath + ":/workspace:ro"},
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("创建框架容器沙箱失败: %w", err)
	}

	return &FrameworkContainerSandbox{exec: exec, name: name}, nil
}

// Name 返回沙箱后端名称。
func (s *FrameworkContainerSandbox) Name() string {
	return "container-fx"
}

// Execute 在框架容器沙箱中执行命令。
//
// 仓库以只读方式挂载在 /workspace，命令以 /workspace 为工作目录
// （spec.Cwd 留空 = ws.Path 本身，见框架 workspace_runtime 的 cwd 解析）。
func (s *FrameworkContainerSandbox) Execute(ctx context.Context, opts ExecuteOptions) (*ExecuteResult, error) {
	if err := opts.Validate(); err != nil {
		return nil, err
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	ws := codeexecutor.Workspace{Path: "/workspace"}
	spec := codeexecutor.RunProgramSpec{
		Cmd:     "sh",
		Args:    []string{"-c", opts.Command},
		Env:     defaultSandboxEnv(opts.Env),
		Timeout: opts.Timeout,
	}

	start := time.Now()
	runResult, runErr := s.exec.RunProgram(timeoutCtx, ws, spec)
	if runErr != nil {
		return nil, fmt.Errorf("框架容器沙箱执行失败: %w", runErr)
	}
	duration := time.Since(start)

	result := &ExecuteResult{
		ExitCode: runResult.ExitCode,
		Stdout:   runResult.Stdout,
		Stderr:   runResult.Stderr,
		Duration: duration.Round(time.Millisecond).String(),
		Backend:  "container-fx",
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

// Close 显式停掉常驻容器（AutoRemove 会随后移除；
// 框架 CodeExecutor 自身只有 GC finalizer 兜底，这里主动收尾避免容器滞留）。
func (s *FrameworkContainerSandbox) Close() error {
	if s.name == "" {
		return nil
	}
	cmd := exec.Command("docker", "stop", "-t", "2", s.name)
	_ = cmd.Run()
	s.name = ""
	return nil
}
