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

// TestParseStaticcheckOutput M3-D2：staticcheck 输出 → findings。
func TestParseStaticcheckOutput(t *testing.T) {
	output := `foo/bar.go:10:5: this value of err is never used (SA4006)
main.go:3:1: should not use dot imports (ST1001)
` // 末尾夹杂非匹配行
	output += "go: downloading honnef.co/go/tools v0.4.7\n"

	fs := parseStaticcheckOutput(output)
	if len(fs) != 2 {
		t.Fatalf("应解析出 2 条 findings, 得到 %d", len(fs))
	}

	first := fs[0]
	if first.File != "foo/bar.go" || first.Line != 10 {
		t.Errorf("定位 = %s:%d, 期望 foo/bar.go:10", first.File, first.Line)
	}
	if first.RuleID != "STATICCHECK-SA4006" {
		t.Errorf("RuleID = %q, 期望 STATICCHECK-SA4006", first.RuleID)
	}
	if first.Source != "tool:staticcheck" {
		t.Errorf("Source = %q, 期望 tool:staticcheck", first.Source)
	}
	if first.Severity != "low" {
		t.Errorf("Severity = %q, 期望 low", first.Severity)
	}
	if first.Category != "quality" {
		t.Errorf("Category = %q, 期望 quality", first.Category)
	}
	if first.Confidence < 0.7 {
		t.Errorf("置信度应进 findings 桶（≥0.7）, 得到 %v", first.Confidence)
	}
	if !strings.Contains(first.Evidence, "never used") {
		t.Errorf("evidence 应含消息, 得到 %q", first.Evidence)
	}
	if len(first.EvidenceChain) != 4 {
		t.Errorf("证据链应 4 步, 得到 %d", len(first.EvidenceChain))
	}

	if fs[1].RuleID != "STATICCHECK-ST1001" {
		t.Errorf("第二条 RuleID = %q", fs[1].RuleID)
	}
}

func TestParseStaticcheckOutput_Empty(t *testing.T) {
	if fs := parseStaticcheckOutput(""); len(fs) != 0 {
		t.Errorf("空输出应得 0 findings, 得到 %d", len(fs))
	}
	if fs := parseStaticcheckOutput("go: cannot find main module\n"); len(fs) != 0 {
		t.Errorf("非匹配行应得 0 findings, 得到 %d", len(fs))
	}
}

func TestStaticcheckSummary(t *testing.T) {
	fs := parseStaticcheckOutput("a.go:1:1: x (SA1)\na.go:2:1: y (SA1)\nb.go:3:1: z (ST2)\n")
	got := staticcheckSummary(fs)
	if !strings.Contains(got, "3 findings") || !strings.Contains(got, "SA1×2") {
		t.Errorf("summary = %q", got)
	}
	if staticcheckSummary(nil) != "0 findings" {
		t.Error("空汇总应为 0 findings")
	}
}
