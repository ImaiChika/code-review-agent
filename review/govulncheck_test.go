// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
package review

import (
	"strings"
	"testing"
)

// 模拟 govulncheck -json 流式输出片段（config/osv/进度行混合）
const govulnJSON = `{"config":{"protocol_version":"v1.0.0"}}
{"progress":{"message":"Scanning your code and P packages across M dependent modules for known vulnerabilities..."}}
{"osv":{"id":"GO-2024-1234","summary":"SQL injection in example/lib","details":"Long details...","aliases":["CVE-2024-0001"]}}
{"osv":{"id":"GO-2024-5678","summary":"Path traversal in example/fs","aliases":[]}}
{"progress":{"message":"Done"}}`

func TestParseGovulncheckOutput(t *testing.T) {
	fs := parseGovulncheckOutput(govulnJSON)
	if len(fs) != 2 {
		t.Fatalf("应解析出 2 个 OSV, got %d", len(fs))
	}
	if fs[0].RuleID != "GOV-GEN-001" || fs[0].Severity != "low" {
		t.Errorf("rule/severity 不符: %+v", fs[0])
	}
	if !strings.Contains(fs[0].Title, "SQL injection in example/lib") {
		t.Errorf("标题应取 summary: %q", fs[0].Title)
	}
	if !strings.Contains(fs[0].Evidence, "CVE-2024-0001") {
		t.Errorf("evidence 应含别名: %q", fs[0].Evidence)
	}
	if fs[0].Category != "quality" {
		t.Errorf("依赖漏洞应为 quality 分类（不参与评分）: %v", fs[0].Category)
	}
}

func TestParseGovulncheckOutput_EmptyAndGarbage(t *testing.T) {
	if len(parseGovulncheckOutput("")) != 0 {
		t.Error("空输出应返回空")
	}
	if len(parseGovulncheckOutput("not json at all\nrandom lines")) != 0 {
		t.Error("非 JSON 行应被忽略")
	}
}
