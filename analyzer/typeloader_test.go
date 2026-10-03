// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
package analyzer

import (
	"os"
	"path/filepath"
	"testing"
)

// buildTempModule 造一个最小 go module：stdlib 导入，覆盖 D3 的三类判定。
// 行号锚点：discard() 体在 22-28 行（sizes=22, open=23, ctx=24, query=26, doErr=27, 字面量=28）。
func buildTempModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module d3test\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(dir, "pkg"), 0755)
	src := `package pkg

import (
	"context"
	"database/sql"
	"os"
)

func open(p string) (*os.File, error) {
	return os.Open(p)
}

func query(db *sql.DB) (*sql.Rows, error) {
	return db.Query("SELECT 1")
}

func sizes() (int64, bool) { return 0, false }

func doErr() error { return nil }

func discard() {
	_, _ = sizes()
	_, _ = open("x")
	c, _ := context.WithCancel(context.Background())
	_ = c
	_, _ = query(nil)
	_ = doErr()
	_ = 42
}
`
	if err := os.WriteFile(filepath.Join(dir, "pkg/a.go"), []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLoadRepoTypes_LHSPositionType(t *testing.T) {
	dir := buildTempModule(t)
	rt := LoadRepoTypes(dir, []string{"pkg/a.go"})
	if rt == nil {
		t.Fatal("类型加载失败（fail-open 也不应发生在 stdlib 场景）")
	}

	cases := []struct {
		line int
		want string
	}{
		{22, "int64"},              // _, _ = sizes() 第一位
		{23, "*os.File"},           // _, _ = open("x") 第一位
		{26, "*database/sql.Rows"}, // _, _ = query(nil) 第一位
		{27, "error"},              // _ = doErr() 唯一返回
	}
	for _, c := range cases {
		got := rt.LHSPositionType("pkg/a.go", c.line, 0)
		if got == nil {
			t.Errorf("L%d 类型未知", c.line)
			continue
		}
		if got.String() != c.want {
			t.Errorf("L%d 类型 = %q, 期望 %q", c.line, got.String(), c.want)
		}
	}
	// 第二位
	if got := rt.LHSPositionType("pkg/a.go", 22, 1); got == nil || got.String() != "bool" {
		t.Errorf("L22 第二位类型 = %v, 期望 bool", got)
	}
	if got := rt.LHSPositionType("pkg/a.go", 23, 1); got == nil || !IsErrorType(got) {
		t.Errorf("L23 第二位应为 error, got %v", got)
	}
	// CTX 构造第二位
	if got := rt.LHSPositionType("pkg/a.go", 24, 1); got == nil || got.String() != "context.CancelFunc" {
		t.Errorf("L24 第二位应为 context.CancelFunc, got %v", got)
	}
	// 字面量赋值：类型非 error
	if got := rt.LHSPositionType("pkg/a.go", 28, 0); got != nil && IsErrorType(got) {
		t.Errorf("L28 字面量不应为 error")
	}
}

func TestLoadRepoTypes_ImplementsCloser(t *testing.T) {
	dir := buildTempModule(t)
	rt := LoadRepoTypes(dir, []string{"pkg/a.go"})
	if rt == nil {
		t.Fatal("类型加载失败")
	}
	impl, known := rt.FirstLHSImplementsCloser("pkg/a.go", 23) // _, _ = open("x") → *os.File
	if !known || !impl {
		t.Errorf("os.File 应实现 io.Closer (known=%v impl=%v)", known, impl)
	}
	impl2, known2 := rt.FirstLHSImplementsCloser("pkg/a.go", 22) // _, _ = sizes() → int64
	if !known2 || impl2 {
		t.Errorf("int64 不应实现 io.Closer (known=%v impl=%v)", known2, impl2)
	}
}

func TestLoadRepoTypes_FailOpen(t *testing.T) {
	// 非 Go 目录：不 panic、返回 nil
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("hi"), 0644)
	if rt := LoadRepoTypes(dir, []string{"readme.txt"}); rt != nil {
		t.Error("非 Go 目录应返回 nil")
	}
	// 不存在的文件：返回 nil
	if rt := LoadRepoTypes(dir, []string{"nope/a.go"}); rt != nil {
		t.Error("文件不存在应返回 nil")
	}
}

// TestLoadRepoTypes_FailCache M9-G5：类型检查失败的包不重试——同进程
// 第二次加载命中缓存；修复代码后签名变化，重试成功。
func TestLoadRepoTypes_FailCache(t *testing.T) {
	dir := t.TempDir()
	// 一个正常包目录（每次审查都重新解析，不进缓存——成功包不缓存防数据陈旧）
	if err := os.MkdirAll(filepath.Join(dir, "pkg"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pkg", "a.go"), []byte("package pkg\n\nfunc A() int { return 1 }\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// 一个语法坏包目录（全部文件解析失败 → 记入失败缓存）
	brokenDir := filepath.Join(dir, "broken")
	if err := os.MkdirAll(brokenDir, 0755); err != nil {
		t.Fatal(err)
	}
	brokenFile := filepath.Join(brokenDir, "bad.go")
	if err := os.WriteFile(brokenFile, []byte("package broken\nfunc (===\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// 首次：broken 解析失败记入缓存，pkg 正常加载
	rt := LoadRepoTypes(dir, []string{"pkg/a.go", "broken/bad.go"})
	if rt == nil {
		t.Fatal("正常包应产出类型信息")
	}
	if rt.DirsLoaded != 1 {
		t.Errorf("首次应加载 1 个包目录，实际 %d", rt.DirsLoaded)
	}
	if rt.DirsCachedFailed != 0 {
		t.Errorf("首次不应有缓存命中，实际 %d", rt.DirsCachedFailed)
	}

	// 二次：broken 同内容 → 命中缓存不重试
	rt2 := LoadRepoTypes(dir, []string{"pkg/a.go", "broken/bad.go"})
	if rt2 == nil {
		t.Fatal("二次加载不应因坏包缓存而失败")
	}
	if rt2.DirsCachedFailed != 1 {
		t.Errorf("二次应命中 1 个失败缓存，实际 %d", rt2.DirsCachedFailed)
	}

	// 修复 broken.go：签名变化 → 重试成功
	if err := os.WriteFile(brokenFile, []byte("package broken\n\nfunc B() int { return 2 }\n"), 0644); err != nil {
		t.Fatal(err)
	}
	rt3 := LoadRepoTypes(dir, []string{"pkg/a.go", "broken/bad.go"})
	if rt3 == nil {
		t.Fatal("修复后加载不应失败")
	}
	if rt3.DirsLoaded != 2 || rt3.DirsCachedFailed != 0 {
		t.Errorf("修复后应重试成功（加载 2 个、缓存命中 0），实际 loaded=%d cached=%d",
			rt3.DirsLoaded, rt3.DirsCachedFailed)
	}
}
