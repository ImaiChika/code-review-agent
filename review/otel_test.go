// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
package review

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace/noop"
)

func spanNames(spans []sdktrace.ReadOnlySpan) []string {
	names := make([]string, 0, len(spans))
	for _, sp := range spans {
		names = append(names, sp.Name())
	}
	return names
}

func execGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func osWriteFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0644)
}

func appendFile(path, s string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(s)
	return err
}

// setupSpanRecorder 注入内存 SpanProcessor，测试结束恢复 noop。
func setupSpanRecorder(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		otel.SetTracerProvider(noop.NewTracerProvider())
		_ = tp.Shutdown(context.Background())
	})
	return sr
}

func attrsOf(sp sdktrace.ReadOnlySpan) map[string]string {
	m := make(map[string]string, len(sp.Attributes()))
	for _, kv := range sp.Attributes() {
		m[string(kv.Key)] = kv.Value.Emit()
	}
	return m
}

func findSpan(spans []sdktrace.ReadOnlySpan, name string) sdktrace.ReadOnlySpan {
	for _, sp := range spans {
		if sp.Name() == name {
			return sp
		}
	}
	return nil
}

// TestRun_OtelSpans 主流程 span：名称、task_id、评分属性齐全（M1-B6）。
func TestRun_OtelSpans(t *testing.T) {
	sr := setupSpanRecorder(t)

	rep, err := Run(Options{
		DiffFile:    "../testdata/security_issue.diff",
		OutputDir:   t.TempDir(),
		SandboxMode: SandboxOff,
	})
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}

	spans := sr.Ended()
	sp := findSpan(spans, "review.run")
	if sp == nil {
		t.Fatalf("应存在 review.run span, 实际: %v", spanNames(spans))
	}
	attrs := attrsOf(sp)
	if attrs["review.task_id"] != rep.TaskID {
		t.Errorf("review.task_id = %q, 期望 %q", attrs["review.task_id"], rep.TaskID)
	}
	if attrs["review.input_type"] != "diff_file" {
		t.Errorf("review.input_type = %q", attrs["review.input_type"])
	}
	if attrs["review.risk_grade"] != rep.Monitor.RiskGrade {
		t.Errorf("review.risk_grade = %q, 期望 %q", attrs["review.risk_grade"], rep.Monitor.RiskGrade)
	}
	if attrs["review.risk_score"] == "" || attrs["review.risk_score"] == "0" {
		t.Errorf("review.risk_score 应非零, 得到 %q", attrs["review.risk_score"])
	}
	if attrs["review.findings_total"] == "" {
		t.Error("review.findings_total 缺失")
	}
}

// TestRun_OtelSandboxSpans 沙箱子 span：命令/后端/退出码/安全决策属性（M1-B6）。
func TestRun_OtelSandboxSpans(t *testing.T) {
	sr := setupSpanRecorder(t)

	// 搭一个带未提交变更的临时 Go 仓库
	repo := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		out, err := execGit(repo, args...)
		if err != nil {
			t.Fatalf("git %v 失败: %v (%s)", args, err, out)
		}
	}
	runGit("init", "-q")
	runGit("config", "user.email", "t@t")
	runGit("config", "user.name", "t")
	if err := osWriteFile(filepath.Join(repo, "go.mod"), []byte("module e2e\n\ngo 1.21\n")); err != nil {
		t.Fatal(err)
	}
	if err := osWriteFile(filepath.Join(repo, "main.go"),
		[]byte("package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hi\")\n}\n")); err != nil {
		t.Fatal(err)
	}
	runGit("add", ".")
	runGit("commit", "-qm", "chore: baseline")
	if err := appendFile(filepath.Join(repo, "main.go"), "\nfunc Unused() {}\n"); err != nil {
		t.Fatal(err)
	}

	rep, err := Run(Options{
		RepoPath:    repo,
		OutputDir:   t.TempDir(),
		SandboxMode: "local",
	})
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}

	spans := sr.Ended()
	if findSpan(spans, "review.run") == nil {
		t.Fatal("应存在 review.run span")
	}

	var sandboxSpans []sdktrace.ReadOnlySpan
	for _, sp := range spans {
		if sp.Name() == "sandbox.exec" {
			sandboxSpans = append(sandboxSpans, sp)
		}
	}
	// M3-D2 起沙箱命令为 3 条（go vet / go test / staticcheck）
	if len(sandboxSpans) != 3 {
		t.Fatalf("应有 3 个 sandbox.exec span (go vet / go test / staticcheck), 得到 %d", len(sandboxSpans))
	}
	for _, sp := range sandboxSpans {
		attrs := attrsOf(sp)
		if attrs["sandbox.command"] == "" {
			t.Error("sandbox.command 缺失")
		}
		if attrs["sandbox.backend"] != "local" {
			t.Errorf("sandbox.backend = %q, 期望 local", attrs["sandbox.backend"])
		}
		if attrs["tool.safety.decision"] != "allow" {
			t.Errorf("tool.safety.decision = %q, 期望 allow", attrs["tool.safety.decision"])
		}
		// staticcheck 在本地环境可能未安装（exit 127，属正常降级），其余命令必须成功
		if strings.HasPrefix(attrs["sandbox.command"], "staticcheck") {
			continue
		}
		if attrs["sandbox.exit_code"] != "0" {
			t.Errorf("%s: exit_code = %q, 期望 0（cwd 修复后 go vet/go test 应通过）",
				attrs["sandbox.command"], attrs["sandbox.exit_code"])
		}
	}
	_ = rep
}

// TestRun_OtelErrorStatus 失败路径：span 应记录错误状态。
func TestRun_OtelErrorStatus(t *testing.T) {
	sr := setupSpanRecorder(t)

	_, err := Run(Options{DiffContent: "plain text, not a diff", OutputDir: t.TempDir(), SandboxMode: SandboxOff})
	if err == nil {
		t.Fatal("应返回 ErrNoChanges")
	}

	sp := findSpan(sr.Ended(), "review.run")
	if sp == nil {
		t.Fatal("失败路径也应产生 span")
	}
	if sp.Status().Code.String() != "Error" {
		t.Errorf("span 状态 = %q, 期望 Error", sp.Status().Code)
	}
}
