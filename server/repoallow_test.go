// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newAllowListServer 启动配置了仓库白名单的测试服务。
func newAllowListServer(t *testing.T, prefixes ...string) (*Server, *httptest.Server) {
	t.Helper()
	tmp := t.TempDir()
	s, err := New(Config{
		Port:         0,
		DBPath:       filepath.Join(tmp, "review.db"),
		DataDir:      filepath.Join(tmp, "data"),
		SampleDir:    "../testdata",
		SandboxMode:  "off",
		AllowedRepos: prefixes,
	})
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() {
		ts.Close()
		_ = s.Close()
	})
	return s, ts
}

func TestRepoAllow_InList(t *testing.T) {
	// 白名单内的真实仓库目录 → 通过预检（进队列）
	root := t.TempDir() // 规范化路径本身就在白名单内
	repo := filepath.Join(root, "myrepo")
	if err := os.MkdirAll(repo, 0755); err != nil {
		t.Fatal(err)
	}
	s, ts := newAllowListServer(t, root)

	if err := s.checkRepoAllowed(repo); err != nil {
		t.Errorf("白名单内路径应放行: %v", err)
	}
	// 白名单根目录本身也允许
	if err := s.checkRepoAllowed(root); err != nil {
		t.Errorf("白名单根路径应放行: %v", err)
	}
	_ = ts
}

func TestRepoAllow_OutOfList403(t *testing.T) {
	allowed := t.TempDir()
	s, ts := newAllowListServer(t, allowed)

	// 白名单外（真实存在的路径）→ 403
	other := t.TempDir()
	if err := s.checkRepoAllowed(other); err == nil {
		t.Error("白名单外路径应拒绝")
	}

	// HTTP 层：不存在的白名单外路径应 403（策略拒绝先于存在性检查）
	res, err := http.Post(ts.URL+"/api/reviews", "application/json",
		strings.NewReader(`{"repo_path":"/nonexistent/repo"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("白名单外 status = %d, 期望 403", res.StatusCode)
	}

	// 白名单内但不存在的路径 → 400（策略过了，输入无效）
	res2, err := http.Post(ts.URL+"/api/reviews", "application/json",
		strings.NewReader(`{"repo_path":"`+filepath.Join(allowed, "no-such-repo")+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Body.Close()
	if res2.StatusCode != http.StatusBadRequest {
		t.Errorf("白名单内不存在路径 status = %d, 期望 400", res2.StatusCode)
	}
}

func TestRepoAllow_DirectoryBoundary(t *testing.T) {
	// /tmp/xyz 允许 /tmp/xyz/myrepo，但不放行 /tmp/xyzfoo（前缀必须落在目录边界上）
	root := t.TempDir()
	base := filepath.Base(root)
	sibling := filepath.Join(filepath.Dir(root), base+"foo")

	s, _ := newAllowListServer(t, root)

	if err := s.checkRepoAllowed(sibling); err == nil {
		t.Errorf("目录边界外的前缀相似路径 %s 不应放行", sibling)
	}
	sub := filepath.Join(root, "deep", "nested", "repo")
	if err := s.checkRepoAllowed(sub); err != nil {
		t.Errorf("白名单深层子目录应放行: %v", err)
	}
}

func TestRepoAllow_TraversalNormalized(t *testing.T) {
	// 穿越路径规范化后落点必须重查白名单
	allowed := t.TempDir()
	s, _ := newAllowListServer(t, allowed)

	// allowed/sub/.. 规范化后回到 allowed 本身 → 放行
	sneakyIn := filepath.Join(allowed, "sub", "..")
	if err := s.checkRepoAllowed(sneakyIn); err != nil {
		t.Errorf("规范化后仍在白名单内的路径应放行: %v", err)
	}
	// allowed/sub/../../other 规范化后落到白名单外 → 拒绝
	sneakyOut := filepath.Join(allowed, "sub", "..", "..", "other")
	if err := s.checkRepoAllowed(sneakyOut); err == nil {
		t.Errorf("穿越到白名单外的路径应拒绝: %s", sneakyOut)
	}
}

func TestRepoAllow_MultiplePrefixes(t *testing.T) {
	a := t.TempDir()
	b := t.TempDir()
	s, _ := newAllowListServer(t, a, b)

	if err := s.checkRepoAllowed(filepath.Join(b, "repo")); err != nil {
		t.Errorf("第二个白名单前缀应放行: %v", err)
	}
	c := t.TempDir()
	if err := s.checkRepoAllowed(filepath.Join(c, "repo")); err == nil {
		t.Error("第三个目录不在白名单应拒绝")
	}
}

func TestRepoAllow_UnsetBehaviorUnchanged(t *testing.T) {
	// 未配置白名单：任意路径不触发 403（本地模式行为不变；不存在路径仍是 400）
	s, ts := newAllowListServer(t)

	if err := s.checkRepoAllowed("/any/path/here"); err != nil {
		t.Errorf("未配置白名单应放行任意路径: %v", err)
	}
	res, err := http.Post(ts.URL+"/api/reviews", "application/json",
		strings.NewReader(`{"repo_path":"/nonexistent/repo"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("未配置白名单时不存在路径 status = %d, 期望 400（保持 F8 语义）", res.StatusCode)
	}
}
