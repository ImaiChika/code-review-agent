// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
package rules

import (
	"fmt"
	"log"
	"strings"

	"code-review-agent/analyzer"
	"code-review-agent/diff"
	"code-review-agent/findings"
)

// RuleEngine 是规则引擎，负责注册和执行所有审查规则。
type RuleEngine struct {
	rules []Rule
}

// NewEngine 创建一个新的规则引擎。
func NewEngine() *RuleEngine {
	return &RuleEngine{}
}

// Register 注册一条规则。
//
// 示例：
//
//	engine := rules.NewEngine()
//	engine.Register(&HardcodedSecretRule{})
//	engine.Register(&GoroutineLeakRule{})
func (e *RuleEngine) Register(rule Rule) {
	e.rules = append(e.rules, rule)
}

// TypeAware 支持 repo 模式类型信息注入的规则（D3：RES/ERR 从"猜"变"知道"）。
type TypeAware interface {
	SetRepoTypes(*analyzer.RepoTypes)
}

// SetRepoTypes 把 repo 模式类型信息注入所有支持 TypeAware 的规则。
// 非 repo 模式传 nil——规则退回词法行为。
func (e *RuleEngine) SetRepoTypes(rt *analyzer.RepoTypes) {
	for _, r := range e.rules {
		if ta, ok := r.(TypeAware); ok {
			ta.SetRepoTypes(rt)
		}
	}
}

// RegisterAll 批量注册规则。
func (e *RuleEngine) RegisterAll(rules ...Rule) {
	for _, r := range rules {
		e.Register(r)
	}
}

// Rules 返回所有已注册的规则。
func (e *RuleEngine) Rules() []Rule {
	return e.rules
}

// Run 对一组文件 diff 执行所有已注册的规则。
//
// 执行流程：
//  1. 过滤生成文件（M2：Code generated / .pb.go 等不参与审查）
//  2. 先执行多文件规则（CheckFiles），可以跨文件关联信息
//  3. 再遍历每个文件 × 每条单文件规则（Check）
//  4. 收集所有 findings
//
// 返回所有规则发现的问题的合并列表。
func (e *RuleEngine) Run(files []diff.FileDiff) ([]findings.Finding, error) {
	var allFindings []findings.Finding

	// 第零步：排除生成文件（M2 规则深化）——生成代码中的测试向量/描述符
	// 不是真实泄漏，逐文件规则会大量误报
	reviewable := make([]diff.FileDiff, 0, len(files))
	for _, fd := range files {
		if IsGeneratedFile(fd) {
			log.Printf("[规则引擎] 跳过生成文件: %s", fd.NewPath)
			continue
		}
		reviewable = append(reviewable, fd)
	}
	files = reviewable

	// 第一步：执行多文件规则
	for _, rule := range e.rules {
		if mfr, ok := rule.(MultiFileRule); ok {
			results, err := mfr.CheckFiles(files)
			if err != nil {
				log.Printf("[规则引擎] 多文件规则 %s(%s) 执行出错: %v",
					rule.Name(), rule.ID(), err)
				continue
			}
			allFindings = append(allFindings, results...)
		}
	}

	// 第二步：执行单文件规则
	for _, fd := range files {
		for _, rule := range e.rules {
			// 多文件规则已经在上面执行过，跳过
			if _, ok := rule.(MultiFileRule); ok {
				continue
			}
			results, err := rule.Check(fd)
			if err != nil {
				log.Printf("[规则引擎] 规则 %s(%s) 在文件 %s 执行出错: %v",
					rule.Name(), rule.ID(), fd.NewPath, err)
				continue
			}
			allFindings = append(allFindings, results...)
		}
	}

	return allFindings, nil
}

// generatedSuffixes 生成文件的文件名后缀模式（M2）。
var generatedSuffixes = []string{
	".pb.go",        // protoc-gen-go
	".pb.gw.go",     // grpc-gateway
	"_gen.go",       // go:generate 约定
	".gen.go",       // 通用生成后缀
	".generated.go", // 通用生成后缀
	"_string.go",    // go generate stringer
}

// IsGeneratedFile 判断是否为生成文件（M2 规则深化）。
//
// 两类依据：
//  1. 文件名后缀（.pb.go / _gen.go / ...）
//  2. Go 官方约定标记：注释行同时包含 "code generated" 与 "do not edit"
//     （大小写不敏感，取自 //go:generate 生态的 Code generated ... DO NOT EDIT. 约定）
func IsGeneratedFile(fd diff.FileDiff) bool {
	for _, suffix := range generatedSuffixes {
		if strings.HasSuffix(fd.NewPath, suffix) {
			return true
		}
	}

	// 内容标记：扫描前 10 行非空行（含上下文与新增）
	scanned := 0
	for _, hunk := range fd.Hunks {
		for _, line := range hunk.Lines {
			content := strings.TrimSpace(line.Content)
			if content == "" {
				continue
			}
			lower := strings.ToLower(content)
			if strings.HasPrefix(content, "//") &&
				strings.Contains(lower, "code generated") &&
				strings.Contains(lower, "do not edit") {
				return true
			}
			scanned++
			if scanned >= 10 {
				return false
			}
		}
	}
	return false
}

// RunOnSingleFile 对单个文件执行所有规则（方便测试）。
func (e *RuleEngine) RunOnSingleFile(fd diff.FileDiff) ([]findings.Finding, error) {
	return e.Run([]diff.FileDiff{fd})
}

// Summary 返回引擎的规则摘要（用于日志和调试）。
func (e *RuleEngine) Summary() string {
	s := fmt.Sprintf("规则引擎: 已注册 %d 条规则\n", len(e.rules))
	for i, r := range e.rules {
		s += fmt.Sprintf("  %d. [%s] %s (%s, %s)\n",
			i+1, r.ID(), r.Name(), r.Severity(), r.Category())
	}
	return s
}
