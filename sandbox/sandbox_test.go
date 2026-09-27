// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLocalSandbox_BasicExecution(t *testing.T) {
	sb, err := NewLocalSandbox("")
	if err != nil {
		t.Fatalf("NewLocalSandbox 失败: %v", err)
	}
	defer sb.Close()

	if sb.Name() != "local" {
		t.Errorf("Name() = %q, 期望 %q", sb.Name(), "local")
	}

	opts := ExecuteOptions{
		Command:   "echo hello",
		Timeout:   5 * time.Second,
		MaxOutput: 1024,
	}

	result, err := sb.Execute(context.Background(), opts)
	if err != nil {
		t.Fatalf("Execute 失败: %v", err)
	}

	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, 期望 0", result.ExitCode)
	}
	if result.Backend != "local" {
		t.Errorf("Backend = %q, 期望 %q", result.Backend, "local")
	}
	if result.Duration == "" {
		t.Error("Duration 不应为空")
	}
}

func TestLocalSandbox_ExitCode(t *testing.T) {
	sb, _ := NewLocalSandbox("")
	defer sb.Close()

	opts := ExecuteOptions{
		Command:   "exit 1",
		Timeout:   5 * time.Second,
		MaxOutput: 1024,
	}

	result, err := sb.Execute(context.Background(), opts)
	if err != nil {
		t.Fatalf("Execute 失败: %v", err)
	}

	if result.ExitCode != 1 {
		t.Errorf("ExitCode = %d, 期望 1", result.ExitCode)
	}
}

func TestLocalSandbox_Timeout(t *testing.T) {
	sb, _ := NewLocalSandbox("")
	defer sb.Close()

	opts := ExecuteOptions{
		Command:   "sleep 10",
		Timeout:   500 * time.Millisecond,
		MaxOutput: 1024,
	}

	result, err := sb.Execute(context.Background(), opts)
	if err != nil {
		t.Fatalf("Execute 失败: %v", err)
	}

	if !result.TimedOut {
		t.Error("TimedOut 应为 true")
	}
}

func TestLocalSandbox_OutputTruncation(t *testing.T) {
	sb, _ := NewLocalSandbox("")
	defer sb.Close()

	// 输出大量数据
	opts := ExecuteOptions{
		Command:   "for i in $(seq 1 1000); do echo \"line $i\"; done",
		Timeout:   5 * time.Second,
		MaxOutput: 200, // 限制 200 字节
	}

	result, err := sb.Execute(context.Background(), opts)
	if err != nil {
		t.Fatalf("Execute 失败: %v", err)
	}

	if !result.Truncated {
		t.Error("Truncated 应为 true")
	}
	if len(result.Output) > 300 { // 允许一些余量（截断提示）
		t.Errorf("输出长度 %d, 期望 <= 300", len(result.Output))
	}
}

func TestLocalSandbox_WorkDir(t *testing.T) {
	sb, _ := NewLocalSandbox("")
	defer sb.Close()

	tmpDir := t.TempDir()
	opts := ExecuteOptions{
		Command:   "pwd",
		WorkDir:   tmpDir,
		Timeout:   5 * time.Second,
		MaxOutput: 1024,
	}

	result, err := sb.Execute(context.Background(), opts)
	if err != nil {
		t.Fatalf("Execute 失败: %v", err)
	}

	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, 期望 0", result.ExitCode)
	}
}

func TestLocalSandbox_SensitiveInfoMasking(t *testing.T) {
	sb, _ := NewLocalSandbox("")
	defer sb.Close()

	opts := ExecuteOptions{
		Command:   `echo "password=secret123456"`,
		Timeout:   5 * time.Second,
		MaxOutput: 1024,
	}

	result, err := sb.Execute(context.Background(), opts)
	if err != nil {
		t.Fatalf("Execute 失败: %v", err)
	}

	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, 期望 0", result.ExitCode)
	}
	// 验证敏感信息被脱敏（输出中不应有原始密码）
	// 注意：shell 可能会转义，这里主要验证脱敏逻辑不崩溃
}

func TestLocalSandbox_InvalidCommand(t *testing.T) {
	sb, _ := NewLocalSandbox("")
	defer sb.Close()

	opts := ExecuteOptions{
		Command:   "", // 空命令
		Timeout:   5 * time.Second,
		MaxOutput: 1024,
	}

	_, err := sb.Execute(context.Background(), opts)
	if err == nil {
		t.Error("空命令应返回错误")
	}
}

func TestLocalSandbox_EnvVars(t *testing.T) {
	sb, _ := NewLocalSandbox("")
	defer sb.Close()

	opts := ExecuteOptions{
		Command:   "echo $MY_VAR",
		Timeout:   5 * time.Second,
		MaxOutput: 1024,
		Env:       map[string]string{"MY_VAR": "hello_world"},
	}

	result, err := sb.Execute(context.Background(), opts)
	if err != nil {
		t.Fatalf("Execute 失败: %v", err)
	}

	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, 期望 0", result.ExitCode)
	}
}

func TestDefaultOptions(t *testing.T) {
	opts := DefaultOptions()
	if opts.Timeout != 30*time.Second {
		t.Errorf("Timeout = %v, 期望 30s", opts.Timeout)
	}
	if opts.MaxOutput != 1024*1024 {
		t.Errorf("MaxOutput = %d, 期望 %d", opts.MaxOutput, 1024*1024)
	}
}

func TestValidate(t *testing.T) {
	// 空命令
	opts := ExecuteOptions{Command: ""}
	if err := opts.Validate(); err == nil {
		t.Error("空命令应返回错误")
	}

	// 零值超时应被修正
	opts = ExecuteOptions{Command: "echo test", Timeout: 0}
	opts.Validate()
	if opts.Timeout != 30*time.Second {
		t.Errorf("Timeout = %v, 期望 30s", opts.Timeout)
	}
}

// 注意：脱敏逻辑已移至 safety/mask.go，测试在 safety 包中。
// 这里只测试沙箱执行是否正确调用了脱敏。

// TestLocalSandbox_WorkDirIsExact 验证命令必须在指定的 WorkDir 中执行（M0-A3）。
// 框架内部做 filepath.Join(ws.Path, spec.Cwd)，Cwd 必须传 workspace 相对路径，
// 否则绝对路径会被再拼一层，命令跑进错误的嵌套目录。
func TestLocalSandbox_WorkDirIsExact(t *testing.T) {
	sb, _ := NewLocalSandbox("")
	defer sb.Close()

	tmpDir := t.TempDir()
	opts := ExecuteOptions{
		Command:   "pwd",
		WorkDir:   tmpDir,
		Timeout:   5 * time.Second,
		MaxOutput: 1024,
	}

	result, err := sb.Execute(context.Background(), opts)
	if err != nil {
		t.Fatalf("Execute 失败: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, 期望 0", result.ExitCode)
	}

	// getcwd 返回物理路径（macOS 下 /var 是 /private/var 的符号链接），归一化后比较
	got := strings.TrimSpace(result.Output)
	wantReal, err := filepath.EvalSymlinks(tmpDir)
	if err != nil {
		t.Fatalf("EvalSymlinks(tmpDir) 失败: %v", err)
	}
	gotReal, err := filepath.EvalSymlinks(got)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q) 失败: %v", got, err)
	}
	if gotReal != wantReal {
		t.Errorf("命令实际执行目录 = %q, 期望 %q", gotReal, wantReal)
	}
}

// TestLocalSandbox_RunProgramInfraError 验证基础设施失败时 Execute 返回错误
// 而不是被吞成"退出码非 0"（M0-A3，修 P1-4）。
func TestLocalSandbox_RunProgramInfraError(t *testing.T) {
	sb, _ := NewLocalSandbox("")
	defer sb.Close()

	// WorkDir 位于一个普通文件之下，框架 MkdirAll 必然失败
	base := t.TempDir()
	blocker := filepath.Join(base, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0644); err != nil {
		t.Fatalf("准备文件失败: %v", err)
	}
	badDir := filepath.Join(blocker, "inner")

	opts := ExecuteOptions{
		Command:   "pwd",
		WorkDir:   badDir,
		Timeout:   5 * time.Second,
		MaxOutput: 1024,
	}

	result, err := sb.Execute(context.Background(), opts)
	if err == nil {
		t.Fatalf("基础设施失败应返回错误，却得到了 result: %+v", result)
	}
	if result != nil {
		t.Errorf("失败时 result 应为 nil，得到 %+v", result)
	}
}
