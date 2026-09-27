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
	"regexp"

	"trpc.group/trpc-go/trpc-agent-go/skill"

	"code-review-agent/report"
)

// defaultSkillName 本仓库 CR Skill 的目录名（skills/code-review/）。
const defaultSkillName = "code-review"

// versionRe 从 SKILL.md front matter 提取 version 字段（框架 Summary 不含 version）。
var versionRe = regexp.MustCompile(`(?m)^version:\s*(\S+)\s*$`)

// LoadSkillMeta 用框架 skill.NewFSRepository 真正加载 SKILL.md（M1-B1）。
//
// 降级策略：加载失败不阻塞审查，返回 Loaded=false + Error 原因，
// 报告照常生成——skill 元数据是增强信息，不是审查的前置条件。
func LoadSkillMeta(skillsDir string) *report.SkillInfo {
	info := &report.SkillInfo{Name: defaultSkillName, Source: skillsDir}

	repo, err := skill.NewFSRepository(skillsDir)
	if err != nil {
		info.Error = fmt.Sprintf("创建 skill 仓库失败: %v", err)
		return info
	}

	sk, err := repo.Get(defaultSkillName)
	if err != nil {
		info.Error = fmt.Sprintf("加载 skill %q 失败: %v", defaultSkillName, err)
		return info
	}

	info.Loaded = true
	info.Name = sk.Summary.Name
	info.Description = sk.Summary.Description

	// version 记录在 SKILL.md front matter，框架 Summary 只暴露 name/description，
	// 从原始文件补齐（读不到就留空，不算失败）
	if dir, err := repo.Path(defaultSkillName); err == nil {
		if data, err := os.ReadFile(filepath.Join(dir, "SKILL.md")); err == nil {
			if m := versionRe.FindSubmatch(data); m != nil {
				info.Version = string(m[1])
			}
		}
	}
	return info
}

// resolveSkillsDir 解析 skills 目录：显式指定优先；未指定时探测 ./skills。
func resolveSkillsDir(skillsDir string) string {
	if skillsDir != "" {
		return skillsDir
	}
	if st, err := os.Stat("skills"); err == nil && st.IsDir() {
		return "skills"
	}
	return ""
}
