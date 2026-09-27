// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
package review

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadSkillMeta 验证框架 skill.NewFSRepository 真加载（M1-B1）。
func TestLoadSkillMeta(t *testing.T) {
	// 本仓库 skills/ 目录（测试运行于 review/ 包目录）
	info := LoadSkillMeta("../skills")
	if !info.Loaded {
		t.Fatalf("应成功加载 skill, 错误: %s", info.Error)
	}
	if info.Name != "code-review" {
		t.Errorf("Name = %q, 期望 code-review", info.Name)
	}
	if info.Version != "1.0.0" {
		t.Errorf("Version = %q, 期望 1.0.0（来自 SKILL.md front matter）", info.Version)
	}
	if info.Description == "" {
		t.Error("Description 不应为空")
	}
	if info.Error != "" {
		t.Errorf("成功加载不应有 Error, 得到 %q", info.Error)
	}
	if !strings.Contains(info.Source, "skills") {
		t.Errorf("Source = %q", info.Source)
	}
}

// TestLoadSkillMeta_MissingDir 降级路径：目录不存在不 panic、Loaded=false、有错误原因。
func TestLoadSkillMeta_MissingDir(t *testing.T) {
	info := LoadSkillMeta(filepath.Join(t.TempDir(), "no-such-skills"))
	if info.Loaded {
		t.Error("不存在的目录不应加载成功")
	}
	if info.Error == "" {
		t.Error("应记录失败原因")
	}
}

// TestResolveSkillsDir 显式指定优先，未指定时探测 ./skills。
func TestResolveSkillsDir(t *testing.T) {
	if got := resolveSkillsDir("/explicit"); got != "/explicit" {
		t.Errorf("显式指定应优先, 得到 %q", got)
	}
	// review/ 包目录下无 ./skills（在上级），探测应返回空
	if got := resolveSkillsDir(""); got != "" {
		t.Errorf("无 ./skills 时应返回空, 得到 %q", got)
	}
}

// TestRun_ReportsSkillMeta 审查报告应携带 skill 元数据（M1-B1 退出标准）。
func TestRun_ReportsSkillMeta(t *testing.T) {
	outDir := t.TempDir()
	rep, err := Run(Options{
		DiffFile:    "../testdata/no_issue.diff",
		OutputDir:   outDir,
		SandboxMode: SandboxOff,
		SkillsDir:   "../skills",
	})
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if rep.Skill == nil {
		t.Fatal("报告应包含 skill 元数据")
	}
	if !rep.Skill.Loaded {
		t.Errorf("skill 应加载成功: %s", rep.Skill.Error)
	}
	if rep.Skill.Name != "code-review" || rep.Skill.Version != "1.0.0" {
		t.Errorf("skill 元数据 = %q/%q", rep.Skill.Name, rep.Skill.Version)
	}
}

// TestRun_NoSkillsDir 探测不到 skills 目录时报告不含 skill 字段（JSON omitempty）。
func TestRun_NoSkillsDir(t *testing.T) {
	outDir := t.TempDir()
	rep, err := Run(Options{
		DiffFile:    "../testdata/no_issue.diff",
		OutputDir:   outDir,
		SandboxMode: SandboxOff,
		// SkillsDir 空 → review/ 包下无 ./skills → 不加载
	})
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if rep.Skill != nil {
		t.Errorf("无 skills 目录时 Skill 应为 nil, 得到 %+v", rep.Skill)
	}
}
