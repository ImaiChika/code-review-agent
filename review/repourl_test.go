// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
package review

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

	files, stats, err := readFullRepoFiles(root, repoMaxFilesFullScan, nil)
	if err != nil {
		t.Fatalf("含符号链接/不可读文件的目录不应整体失败: %v", err)
	}
	// M9-G4：跳过明细应可解释（symlink 必跳过；unreadable 在非 root 下跳过）
	if stats == nil || stats.skipped["symlink"] != 1 {
		t.Errorf("符号链接应记入跳过明细，实际 %v", stats.skipped)
	}
	if os.Getuid() != 0 && stats.skipped["unreadable"] != 1 {
		t.Errorf("不可读文件应记入跳过明细（非 root 环境），实际 %v", stats.skipped)
	}
	if stats.total != stats.skipped["symlink"]+stats.skipped["unreadable"]+stats.skipped["binary"]+stats.skipped["oversized"] {
		t.Errorf("跳过总数应等于各原因之和: total=%d, %v", stats.total, stats.skipped)
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

// TestReadFullRepoFiles_Progress M9-G5：大仓库采集分段进度——每过
// collectProgressInterval 个文件回调一次（600 文件 → 恰好 1 次回调）。
func TestReadFullRepoFiles_Progress(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "pkg")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 600; i++ {
		name := filepath.Join(sub, fmt.Sprintf("f%03d.go", i))
		if err := os.WriteFile(name, []byte("package pkg\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	callbacks := 0
	lastSeen := 0
	files, _, err := readFullRepoFiles(root, repoMaxFilesFullScan, func(collected int) {
		callbacks++
		lastSeen = collected
	})
	if err != nil {
		t.Fatalf("采集失败: %v", err)
	}
	if len(files) != 600 {
		t.Fatalf("应采集 600 个文件，实际 %d", len(files))
	}
	if callbacks != 1 || lastSeen != 500 {
		t.Errorf("进度回调应为 1 次@500，实际 %d 次@%d", callbacks, lastSeen)
	}
}

// TestReadFullRepoFiles_LimitTrustBoundary M9-G5：文件数上限按输入来源区分
// 信任边界（repo_url 1000 / 本地全量 5000）——maxFiles 是参数，达上限报可读错误。
func TestReadFullRepoFiles_LimitTrustBoundary(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "pkg")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := os.WriteFile(filepath.Join(sub, fmt.Sprintf("f%d.go", i)), []byte("package pkg\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	_, _, err := readFullRepoFiles(root, 2, nil)
	if err == nil || !strings.Contains(err.Error(), "超过上限") {
		t.Errorf("超过 maxFiles 应报可读错误: %v", err)
	}
	files, _, err := readFullRepoFiles(root, 3, nil)
	if err != nil || len(files) != 3 {
		t.Errorf("等于上限应放行: err=%v files=%d", err, len(files))
	}
}
