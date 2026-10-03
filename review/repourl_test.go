// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
package review

import (
	"os"
	"path/filepath"
	"testing"
)

// TestReadFullRepoFiles_SymlinkAndUnreadable 采集健壮性（psf/requests 整仓失败教训）：
// 指向目录的符号链接、不可读文件都不应让整仓审查失败——跳过它们继续采集。
func TestReadFullRepoFiles_SymlinkAndUnreadable(t *testing.T) {
	root := t.TempDir()
	// 正常文件
	if err := os.MkdirAll(filepath.Join(root, "pkg"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pkg", "a.go"), []byte("package pkg\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// 目录 + 指向目录的符号链接（requests tests/certs 的 ca -> ../../expired/ca/ 形态）
	if err := os.MkdirAll(filepath.Join(root, "expired", "ca"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "expired", "ca"), filepath.Join(root, "ca-link")); err != nil {
		t.Skipf("当前环境不支持创建符号链接: %v", err)
	}
	// 不可读文件（模拟权限异常；root 用户下 chmod 不生效则跳过该断言路径）
	unreadable := filepath.Join(root, "secret.go")
	if err := os.WriteFile(unreadable, []byte("package x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unreadable, 0000); err == nil {
		defer os.Chmod(unreadable, 0644)
		if os.Getuid() == 0 {
			// root 忽略权限位，无法构造不可读场景
		}
	}

	files, err := readFullRepoFiles(root)
	if err != nil {
		t.Fatalf("含符号链接/不可读文件的目录不应整体失败: %v", err)
	}
	found := false
	for _, fd := range files {
		if filepath.Base(fd.NewPath) == "a.go" {
			found = true
		}
		if base := filepath.Base(fd.NewPath); base == "ca-link" || base == "secret.go" {
			t.Errorf("不应采集异常条目: %s", base)
		}
	}
	if !found {
		t.Errorf("正常文件应被采集，实际 %d 个文件", len(files))
	}
}
