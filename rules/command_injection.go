// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
package rules

import (
	"strings"

	"code-review-agent/diff"
	"code-review-agent/findings"
)

// ========== SEC-AST-004: 命令注入检测（R1，竞品 ≥4 家实现的广度缺口） ==========
//
// 检测策略（#2318 的"payload 实参是否字面量"为核心，行级词法实现）：
//
//	形态一：exec.Command / exec.CommandContext 的可执行文件参数是变量
//	        （要执行的程序来自外部输入）
//	形态二：可执行文件是 shell 字面量（sh/bash/zsh/cmd/powershell/ksh），
//	        但后续参数（如 -c 的脚本内容）是变量/拼接——shell 会再解析一次，
//	        等价于把输入当代码执行
//	形态三：syscall.Exec 的任一参数是变量
//
// 豁免：全字面量调用（exec.Command("ls", "-la")、sh -c "echo hi"）——
// 固定命令无注入面，见 dataset neg_r1_cmd_literal_001。
type TokenCommandInjectionRule struct{}

// NewTokenCommandInjectionRule 创建命令注入检测规则实例。
func NewTokenCommandInjectionRule() *TokenCommandInjectionRule {
	return &TokenCommandInjectionRule{}
}

func (r *TokenCommandInjectionRule) ID() string                  { return "SEC-AST-004" }
func (r *TokenCommandInjectionRule) Name() string                { return "Token 感知的命令注入检测" }
func (r *TokenCommandInjectionRule) Severity() findings.Severity { return findings.SeverityHigh }
func (r *TokenCommandInjectionRule) Category() findings.Category { return findings.CategorySecurity }

// shellBinaries 常见 shell：这些程序的 -c 参数会被再解析一次，等价 eval。
var shellBinaries = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "ksh": true,
	"cmd": true, "cmd.exe": true, "powershell": true, "powershell.exe": true,
}

// commandExecPrefixes 命令执行调用家族。
var commandExecPrefixes = []string{"exec.CommandContext(", "exec.Command("}

func (r *TokenCommandInjectionRule) Check(fd diff.FileDiff) ([]findings.Finding, error) {
	var result []findings.Finding

	for _, line := range collectAddedLines(fd) {
		content := line.Content
		if isCommentLine(content) {
			continue
		}

		for _, prefix := range commandExecPrefixes {
			idx := strings.Index(content, prefix)
			if idx < 0 {
				continue
			}
			args := splitTopLevelArgs(content[idx+len(prefix):])
			if len(args) == 0 {
				continue
			}
			first := strings.TrimSpace(args[0])

			if isStringLiteralArg(first) {
				bin := strings.Trim(first, "\"`")
				if !shellBinaries[strings.ToLower(bin)] {
					continue // 非 shell 可执行文件，本规则不覆盖
				}
				// shell 字面量：检查后续参数是否含动态内容（-c 的脚本等）
				dynamic := false
				for _, a := range args[1:] {
					if isDynamicArg(a) {
						dynamic = true
						break
					}
				}
				if !dynamic {
					continue
				}
				result = append(result, *r.buildFinding(fd, line, "shell -c 参数来自变量，注入即任意代码执行"))
			} else {
				result = append(result, *r.buildFinding(fd, line, "要执行的可执行文件来自变量"))
			}
			break // 一行只报一次
		}

		// 形态三：syscall.Exec 任一参数动态
		if idx := strings.Index(content, "syscall.Exec("); idx >= 0 {
			args := splitTopLevelArgs(content[idx+len("syscall.Exec("):])
			for _, a := range args {
				if isDynamicArg(a) {
					result = append(result, *r.buildFinding(fd, line, "syscall.Exec 参数来自变量"))
					break
				}
			}
		}
	}

	return result, nil
}

func (r *TokenCommandInjectionRule) buildFinding(fd diff.FileDiff, line diff.Line, why string) *findings.Finding {
	f := findings.NewFinding(
		r.Severity(), r.Category(), r.ID(),
		"Token 感知：命令执行疑似注入外部输入（命令注入）",
		fd.NewPath, line.NewLine,
		line.Content,
		"避免把外部输入传给 shell；必要时用白名单校验或 exec.Command 的参数数组形式（不经 shell 解析）",
		0.80,
		"token:command_injection",
	)
	f.EvidenceChain = findings.BuildEvidenceChain(
		fd.NewPath, line.NewLine, r.ID(),
		"exec family call with non-literal argument ("+why+")",
		0.80,
	)
	return f
}

// isStringLiteralArg 参数是否为纯字符串字面量（可带引号前缀空格）。
func isStringLiteralArg(arg string) bool {
	a := strings.TrimSpace(arg)
	return strings.HasPrefix(a, `"`) || strings.HasPrefix(a, "`")
}

// isDynamicArg 参数是否含动态内容：非引号开头，或引号内含 + 拼接。
// 标志位字面量（"-c"、"--flag"）是字面量，不算动态。
func isDynamicArg(arg string) bool {
	a := strings.TrimSpace(arg)
	if a == "" {
		return false
	}
	if isStringLiteralArg(a) {
		// 字面量内的 + 拼接（如 "echo " + userInput）仍是动态
		inner := a[1:]
		if strings.Contains(inner, "+") {
			afterPlus := inner[strings.Index(inner, "+")+1:]
			t := strings.TrimSpace(afterPlus)
			if t != "" && !strings.HasPrefix(t, `"`) && !strings.HasPrefix(t, "`") {
				return true
			}
		}
		return false
	}
	return true // 变量、函数调用、下标表达式等都是动态
}

// splitTopLevelArgs 按顶层逗号切分参数（忽略引号/括号/下标内的逗号）。
func splitTopLevelArgs(s string) []string {
	var args []string
	depth := 0
	inStr := byte(0)
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr != 0 {
			if c == inStr {
				inStr = 0
			}
			continue
		}
		switch c {
		case '"', '`':
			inStr = c
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			// 调用自身的收尾括号之后的内容（如 .Output()）不再属于参数
			if depth == 0 {
				return append(args, strings.TrimSpace(s[start:i]))
			}
			depth--
		case ',':
			if depth == 0 {
				args = append(args, s[start:i])
				start = i + 1
			}
		}
	}
	if t := strings.TrimSpace(s[start:]); t != "" {
		args = append(args, t)
	}
	return args
}
