// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code-review-agent/diff"
	"code-review-agent/findings"
	"code-review-agent/rules"
)

// TestIntegration_AllFixtures 遍历 testdata/*.diff，执行完整审查流程。
func TestIntegration_AllFixtures(t *testing.T) {
	fixtures, err := filepath.Glob("testdata/*.diff")
	if err != nil {
		t.Fatalf("查找 fixture 失败: %v", err)
	}
	if len(fixtures) == 0 {
		t.Fatal("testdata/ 中没有 .diff 文件")
	}

	// 创建规则引擎
	engine := rules.NewEngine()
	engine.Register(rules.NewTokenSecretRule())
	engine.Register(rules.NewTokenLeakRule())
	engine.Register(rules.NewTokenGoroutineRule())
	engine.Register(rules.NewTokenResourceRule())
	engine.Register(rules.NewTokenErrorRule())
	engine.Register(rules.NewTokenMissingTestRule())
	engine.Register(rules.NewTokenDBLifecycleRule())      // M2-D4
	engine.Register(rules.NewTokenContextCancelRule())    // R1：CTX-AST-001
	engine.Register(rules.NewTokenSQLInjectionRule())     // R1：SEC-AST-003
	engine.Register(rules.NewTokenCommandInjectionRule()) // R1：SEC-AST-004
	engine.Register(rules.NewTokenInsecureTLSRule())      // R2：SEC-AST-005
	engine.Register(rules.NewTokenContextRootRule())      // R2：CTX-AST-002
	engine.Register(rules.NewTokenMutexRule())            // R2：CON-AST-001
	engine.Register(rules.NewTokenLoopTimerRule())        // R2：RES-AST-002
	engine.Register(rules.NewTokenRowsErrRule())          // R2：DB-AST-002
	engine.Register(rules.NewGeneralSecretRule())         // W1：SEC-GEN-001
	engine.Register(rules.NewGeneralLargeDeleteRule())    // W1：DEL-GEN-001

	for _, fixture := range fixtures {
		name := filepath.Base(fixture)
		t.Run(name, func(t *testing.T) {
			// 解析 diff
			files, err := diff.ReadFromFile(fixture)
			if err != nil {
				t.Fatalf("解析 diff 失败: %v", err)
			}

			// 执行审查
			allFindings, err := engine.Run(files)
			if err != nil {
				t.Fatalf("规则执行失败: %v", err)
			}

			// 去重
			result := findings.Deduplicate(allFindings)

			// 验证每个 fixture 的预期行为
			switch name {
			case "no_issue.diff":
				if len(result.Findings) != 0 {
					t.Errorf("no_issue 应无 findings，得到 %d", len(result.Findings))
				}

			case "security_issue.diff":
				if len(result.Findings) == 0 {
					t.Error("security_issue 应有 findings")
				}
				hasHigh := false
				for _, f := range result.Findings {
					if f.Severity == findings.SeverityHigh {
						hasHigh = true
					}
				}
				if !hasHigh {
					t.Error("security_issue 应有 high severity finding")
				}

			case "goroutine_leak.diff":
				if len(result.Findings) == 0 {
					t.Error("goroutine_leak 应有 findings")
				}

			case "resource_leak.diff":
				if len(result.Findings) == 0 {
					t.Error("resource_leak 应有 findings")
				}

			case "sensitive_info.diff":
				if len(result.Findings) == 0 {
					t.Error("sensitive_info 应有 findings")
				}
				// 应该检测到多种敏感信息
				if len(result.Findings) < 3 {
					t.Errorf("sensitive_info 应检测到至少 3 个问题，得到 %d", len(result.Findings))
				}

			case "duplicate_finding.diff":
				// 去重后数量应 <= 原始数量
				if len(result.Findings) > len(allFindings) {
					t.Errorf("去重后 findings 不应增加: %d > %d", len(result.Findings), len(allFindings))
				}

			default:
				// 其他 fixture 只要不崩溃就行
				t.Logf("%s: %d findings, %d warnings", name, len(result.Findings), len(result.Warnings))
			}
		})
	}
}

// TestIntegration_AllFixturesCount 验证 fixture 数量。
func TestIntegration_AllFixturesCount(t *testing.T) {
	fixtures, _ := filepath.Glob("testdata/*.diff")
	if len(fixtures) < 8 {
		t.Errorf("应有至少 8 个 fixture，实际 %d", len(fixtures))
	}
}

// TestIntegration_ReportGeneration 验证报告生成。
func TestIntegration_ReportGeneration(t *testing.T) {
	// 解析 diff
	files, err := diff.ReadFromFile("testdata/security_issue.diff")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}

	// 执行审查
	engine := rules.NewEngine()
	engine.Register(rules.NewTokenSecretRule())
	allFindings, _ := engine.Run(files)
	result := findings.Deduplicate(allFindings)

	// 验证 findings 不为空
	if len(result.Findings) == 0 {
		t.Fatal("应有 findings")
	}

	// 验证 finding 结构完整
	f := result.Findings[0]
	if f.File == "" {
		t.Error("finding.File 不应为空")
	}
	if f.Line == 0 {
		t.Error("finding.Line 不应为 0")
	}
	if f.Severity == "" {
		t.Error("finding.Severity 不应为空")
	}
	if f.Category == "" {
		t.Error("finding.Category 不应为空")
	}
	if f.RuleID == "" {
		t.Error("finding.RuleID 不应为空")
	}
	if f.Title == "" {
		t.Error("finding.Title 不应为空")
	}
	if f.Evidence == "" {
		t.Error("finding.Evidence 不应为空")
	}
	if f.Recommendation == "" {
		t.Error("finding.Recommendation 不应为空")
	}
	if f.Confidence <= 0 || f.Confidence > 1 {
		t.Errorf("finding.Confidence = %.2f, 应在 0-1 范围", f.Confidence)
	}
}

// TestIntegration_SensitiveInfoDetection 验证敏感信息检测覆盖率。
func TestIntegration_SensitiveInfoDetection(t *testing.T) {
	data, err := os.ReadFile("testdata/sensitive_info.diff")
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}

	content := string(data)

	// 验证 diff 中包含各种敏感信息
	sensitivePatterns := []string{
		"AKIA",                  // AWS Key
		"ghp_",                  // GitHub Token
		"SuperSecret",           // 密码
		"postgres://",           // 数据库连接串
		"eyJ",                   // JWT
		"BEGIN RSA PRIVATE KEY", // 私钥
	}

	for _, pattern := range sensitivePatterns {
		if !strings.Contains(content, pattern) {
			t.Errorf("sensitive_info.diff 应包含 %q", pattern)
		}
	}
}
