// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
package rules

import (
	"regexp"
	"strings"

	"code-review-agent/diff"
	"code-review-agent/findings"
)

// ========== SEC-AST-003: SQL 拼接注入检测（R1，竞品 ≥4 家实现的广度缺口） ==========
//
// 检测策略（取 #2240 的泛化双条件，不写死变量名）：
//
//	条件一：本行字符串字面量中出现 SQL 语句形态（SELECT..FROM / INSERT INTO /
//	        UPDATE..SET / DELETE FROM / DROP / TRUNCATE）
//	条件二：同行走拼接/格式化手法——`"..." + 标识符`、`标识符 + "..."`，
//	        或 fmt.Sprintf 且格式串含 %s/%v
//	豁免：参数化占位符（? / $N）在 SQL 字面量内 → 放行；注释行放行。
//
// 陷阱覆盖（dataset neg_r1_sql_const_concat_001）：纯常量片段拼接
// （`baseSQL + " ORDER BY id"`，拼接侧字面量无 SQL 语句形态）不报——
// 关键字形态必须出现在含拼接/格式化的那一行的字面量里。
type TokenSQLInjectionRule struct{}

// NewTokenSQLInjectionRule 创建 SQL 拼接注入检测规则实例。
func NewTokenSQLInjectionRule() *TokenSQLInjectionRule { return &TokenSQLInjectionRule{} }

func (r *TokenSQLInjectionRule) ID() string                  { return "SEC-AST-003" }
func (r *TokenSQLInjectionRule) Name() string                { return "Token 感知的 SQL 拼接注入检测" }
func (r *TokenSQLInjectionRule) Severity() findings.Severity { return findings.SeverityHigh }
func (r *TokenSQLInjectionRule) Category() findings.Category { return findings.CategorySecurity }

// sqlStatementRe SQL 语句形态：必须在字符串字面量内命中才视为 SQL。
var sqlStatementRe = regexp.MustCompile(`(?i)(select\b[^"]{0,200}?\bfrom\b|insert\s+into\b|update\b\s+\S+\s+set\b|delete\s+from\b|drop\s+(?:table|database)\b|truncate\s+table\b)`)

// 拼接手法：字符串字面量后接标识符/括号，或标识符/右括号/数字后接字符串字面量。
var (
	concatAfterLiteralRe  = regexp.MustCompile(`"\s*\+\s*[A-Za-z_(]`)
	concatBeforeLiteralRe = regexp.MustCompile(`[A-Za-z_)\d]\s*\+\s*"`)
	sprintfCallRe         = regexp.MustCompile(`fmt\.Sprintf\(`)
	// 参数化占位符（? / $1）：SQL 字面量内出现即放行
	paramPlaceholderRe = regexp.MustCompile(`\?|\$\d`)
)

func (r *TokenSQLInjectionRule) Check(fd diff.FileDiff) ([]findings.Finding, error) {
	var result []findings.Finding

	for _, line := range collectAddedLines(fd) {
		content := line.Content
		if isCommentLine(content) {
			continue
		}
		if !sqlStatementRe.MatchString(content) {
			continue
		}
		// 提取含 SQL 形态的字面量，参数化占位符豁免
		for _, lit := range sqlLiterals(content) {
			if !sqlStatementRe.MatchString(lit) {
				continue
			}
			if paramPlaceholderRe.MatchString(lit) {
				continue
			}
			// 条件二：拼接或格式化
			dynamic := concatAfterLiteralRe.MatchString(content) ||
				concatBeforeLiteralRe.MatchString(content) ||
				sprintfCallRe.MatchString(content)
			if !dynamic {
				continue
			}
			f := findings.NewFinding(
				r.Severity(), r.Category(), r.ID(),
				"Token 感知：SQL 语句疑似拼接外部输入（SQL 注入）",
				fd.NewPath, line.NewLine,
				content,
				"使用参数化查询（占位符 ? / $1 + 参数绑定），不要把变量拼进 SQL 字符串",
				0.75,
				"token:sql_injection",
			)
			f.EvidenceChain = findings.BuildEvidenceChain(
				fd.NewPath, line.NewLine, r.ID(),
				"sql_statement_literal + concat/sprintf with non-literal, no placeholder",
				0.75,
			)
			result = append(result, *f)
			break // 一行只报一次
		}
	}

	return result, nil
}

// sqlLiterals 提取行内双引号字符串字面量（SQL 检测用；不做转义态机，够用于行级启发）。
func sqlLiterals(line string) []string {
	var out []string
	for {
		start := strings.Index(line, `"`)
		if start < 0 {
			break
		}
		end := strings.Index(line[start+1:], `"`)
		if end < 0 {
			break
		}
		out = append(out, line[start+1:start+1+end])
		line = line[start+1+end+1:]
	}
	return out
}
