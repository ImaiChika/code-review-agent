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
