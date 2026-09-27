// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
package findings

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNewFinding(t *testing.T) {
	f := NewFinding(
		SeverityHigh,
		CategorySecurity,
		"SEC-001",
		"Hardcoded API key",
		"config.go",
		10,
		`APIKey = "sk-abc123"`,
		"Use environment variable",
		0.95,
		"rule:hardcoded_secret",
	)

	if f.Severity != SeverityHigh {
		t.Errorf("Severity = %q, 期望 %q", f.Severity, SeverityHigh)
	}
	if f.Category != CategorySecurity {
		t.Errorf("Category = %q, 期望 %q", f.Category, CategorySecurity)
	}
	if f.RuleID != "SEC-001" {
		t.Errorf("RuleID = %q, 期望 %q", f.RuleID, "SEC-001")
	}
	if f.File != "config.go" {
		t.Errorf("File = %q, 期望 %q", f.File, "config.go")
	}
	if f.Line != 10 {
		t.Errorf("Line = %d, 期望 %d", f.Line, 10)
	}
	if f.Timestamp == "" {
		t.Error("Timestamp 不应为空")
	}
	if f.dedupKey == "" {
		t.Error("dedupKey 不应为空")
	}
}

func TestDedupKey(t *testing.T) {
	f := NewFinding(
		SeverityHigh, CategorySecurity, "SEC-001", "title",
		"config.go", 10, "evidence", "rec", 0.9, "source",
	)

	key := f.DedupKey()
	expected := "config.go:10:security:SEC-001"
	if key != expected {
		t.Errorf("DedupKey() = %q, 期望 %q", key, expected)
	}
}

func TestIsHighConfidence(t *testing.T) {
	tests := []struct {
		confidence float64
		want       bool
	}{
		{1.0, true},
		{0.95, true},
		{0.7, true},
		{0.69, false},
		{0.5, false},
		{0.0, false},
	}

	for _, tt := range tests {
		f := &Finding{Confidence: tt.confidence}
		got := f.IsHighConfidence()
		if got != tt.want {
			t.Errorf("IsHighConfidence(%.2f) = %v, 期望 %v", tt.confidence, got, tt.want)
		}
	}
}

func TestSeverityOrder(t *testing.T) {
	tests := []struct {
		severity Severity
		want     int
	}{
		{SeverityHigh, 4},
		{SeverityMedium, 3},
		{SeverityLow, 2},
		{SeverityInfo, 1},
		{"unknown", 0},
	}

	for _, tt := range tests {
		f := &Finding{Severity: tt.severity}
		got := f.SeverityOrder()
		if got != tt.want {
			t.Errorf("SeverityOrder(%q) = %d, 期望 %d", tt.severity, got, tt.want)
		}
	}
}

func TestFindingString(t *testing.T) {
	f := &Finding{
		Severity: SeverityHigh,
		Category: CategorySecurity,
		File:     "config.go",
		Line:     10,
		Title:    "Hardcoded API key",
	}

	s := f.String()
	expected := "[high][security] config.go:10 - Hardcoded API key"
	if s != expected {
		t.Errorf("String() = %q, 期望 %q", s, expected)
	}
}

// TestNewFinding_MasksSensitiveEvidence 验证统一 Redactor（M0-A1）：
// 含明文密钥的 evidence 在 NewFinding 出口被脱敏，报告和 DB 不会有明文。
func TestNewFinding_MasksSensitiveEvidence(t *testing.T) {
	tests := []struct {
		name     string
		evidence string
		secret   string // 不应出现在结果 evidence 中的敏感字面量
	}{
		{
			name:     "硬编码密钥赋值",
			evidence: `var apiKey = "sk-live-q1w2e3r4t5y6u7i8o9p0"`,
			secret:   "sk-live-q1w2e3r4t5y6u7i8o9p0",
		},
		{
			name:     "DB 连接串含密码",
			evidence: `var dbURL = "mysql://root:Hunter2Secret@prod-db:3306/app"`,
			secret:   "Hunter2Secret",
		},
		{
			name:     "AWS Key",
			evidence: `var k1 = "AKIAIOSFODNN7EXAMPLE"`,
			secret:   "AKIAIOSFODNN7EXAMPLE",
		},
		{
			name:     "密码字段赋值",
			evidence: `var dbPassword = "HardcodedPass2026"`,
			secret:   "HardcodedPass2026",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := NewFinding(
				SeverityHigh, CategorySecurity, "SEC-001", "title",
				"creds.go", 3, tt.evidence, "修复它", 0.9, "test",
			)
			if strings.Contains(f.Evidence, tt.secret) {
				t.Errorf("evidence 泄漏明文敏感字面量 %q: %q", tt.secret, f.Evidence)
			}
			if !strings.Contains(f.Evidence, "REDACTED") {
				t.Errorf("evidence 应包含脱敏标记，得到 %q", f.Evidence)
			}
		})
	}
}

// TestNewFinding_MasksRecommendation 验证 recommendation 同样过脱敏。
func TestNewFinding_MasksRecommendation(t *testing.T) {
	f := NewFinding(
		SeverityHigh, CategorySecurity, "SEC-001", "title",
		"creds.go", 3, "some code", `配置 dsn: mysql://root:SuperSecret9@db/`, 0.9, "test",
	)
	if strings.Contains(f.Recommendation, "SuperSecret9") {
		t.Errorf("recommendation 泄漏明文: %q", f.Recommendation)
	}
}

// TestNewFinding_KeepsBenignEvidence 验证普通代码行 evidence 不被误伤。
func TestNewFinding_KeepsBenignEvidence(t *testing.T) {
	benign := []string{
		"f, err := os.Open(path)",
		"if err != nil { return nil }",
		"resp, err := http.Get(url)",
		"go func() { defer wg.Done() }()",
		"total := len(items) + 1",
	}
	for _, ev := range benign {
		f := NewFinding(SeverityLow, CategoryResource, "RES-001", "t", "a.go", 1, ev, "r", 0.8, "test")
		if f.Evidence != ev {
			t.Errorf("普通代码行被误改: 期望 %q, 得到 %q", ev, f.Evidence)
		}
	}
}

// TestBuildEvidenceChain M2 证据链构建器：四步结构 + 不携带内容值。
func TestBuildEvidenceChain(t *testing.T) {
	chain := BuildEvidenceChain("auth.go", 4, "SEC-AST-002",
		"string literal matches known leak pattern", 0.85)
	if len(chain) != 4 {
		t.Fatalf("链应 4 步, 得到 %d", len(chain))
	}
	if !strings.HasPrefix(chain[0], "hunk: auth.go@L4") {
		t.Errorf("第 1 步应为 hunk 定位, 得到 %q", chain[0])
	}
	if !strings.HasPrefix(chain[1], "fact: ") {
		t.Errorf("第 2 步应为 fact, 得到 %q", chain[1])
	}
	if chain[2] != "rule: SEC-AST-002" {
		t.Errorf("第 3 步应为 rule, 得到 %q", chain[2])
	}
	if chain[3] != "confidence: 0.85" {
		t.Errorf("第 4 步应为置信度, 得到 %q", chain[3])
	}
	// 链条只含定位/事实类型/规则/置信度，不含代码内容值
	if strings.Contains(strings.Join(chain, " | "), "AKIA") {
		t.Error("链条不得携带密钥字面量")
	}
}

// TestFindingEvidenceChainJSON 验证证据链随 finding JSON 序列化（omitempty）。
func TestFindingEvidenceChainJSON(t *testing.T) {
	f := NewFinding(SeverityHigh, CategorySecurity, "SEC-AST-001", "t", "a.go", 1,
		"evidence", "rec", 0.9, "test")
	f.EvidenceChain = BuildEvidenceChain("a.go", 1, "SEC-AST-001", "fact", 0.9)
	data, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"evidence_chain"`) {
		t.Error("JSON 应包含 evidence_chain 字段")
	}

	// 未设置链时字段省略
	f2 := NewFinding(SeverityHigh, CategorySecurity, "SEC-AST-001", "t", "a.go", 1,
		"evidence", "rec", 0.9, "test")
	data2, _ := json.Marshal(f2)
	if strings.Contains(string(data2), "evidence_chain") {
		t.Error("未设置链时 JSON 应省略 evidence_chain")
	}
}
