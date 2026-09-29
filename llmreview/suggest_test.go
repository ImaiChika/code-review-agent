// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
package llmreview

import (
	"context"
	"strings"
	"testing"

	"code-review-agent/findings"
)

func sampleFindings() []findings.Finding {
	return []findings.Finding{
		{RuleID: "SEC-AST-001", Severity: "high", File: "a.go", Line: 3,
			Title: "硬编码密钥", Evidence: `k = "sk-***REDACTED***"`, Recommendation: "改用环境变量"},
		{RuleID: "GOR-AST-001", Severity: "high", File: "b.go", Line: 9,
			Title: "goroutine 泄漏", Evidence: "go worker()", Recommendation: "使用 context"},
	}
}

func TestSuggest_ParseAndReplace(t *testing.T) {
	m := NewFakeModel()
	m.PushText("1. 使用 os.Getenv 读取密钥并加入 .env，禁止字面量落库\n2. 用 errgroup.WithContext 管理 worker 生命周期，ctx 触发退出\n")

	sug, stats := Suggest(context.Background(), m, sampleFindings())
	if stats.Error != "" {
		t.Fatalf("Suggest 失败: %s", stats.Error)
	}
	if len(sug) != 2 {
		t.Fatalf("建议数 = %d, 期望 2", len(sug))
	}
	if !strings.Contains(sug[0], "os.Getenv") {
		t.Errorf("建议 1 = %q, 应包含具体方案", sug[0])
	}
	if !strings.Contains(sug[1], "errgroup") {
		t.Errorf("建议 2 = %q, 应包含具体方案", sug[1])
	}
	if stats.Requested != 2 {
		t.Errorf("Requested = %d, 期望 2", stats.Requested)
	}
}

func TestSuggest_MissingKeepsOriginal(t *testing.T) {
	// 只给第 2 条建议：第 1 条缺失 → 调用方保留原建议（此处验证解析语义）
	m := NewFakeModel()
	m.PushText("2. 用 errgroup 管理 worker 生命周期\n")

	sug, _ := Suggest(context.Background(), m, sampleFindings())
	if _, ok := sug[0]; ok {
		t.Error("缺失的第 1 条不应出现在建议表")
	}
	if !strings.Contains(sug[1], "errgroup") {
		t.Errorf("第 2 条建议应存在: %v", sug[1])
	}
}

func TestSuggest_ModelFailureKeepsAll(t *testing.T) {
	m := NewFakeModel()
	m.PushText("not a suggestion at all\n") // 无序号行 → 解析为空

	sug, stats := Suggest(context.Background(), m, sampleFindings())
	if len(sug) != 0 {
		t.Errorf("不可解析输出应得到空建议表, 得到 %v", sug)
	}
	if stats.Error != "" {
		t.Errorf("可解析但无建议行不是错误（保守保留）, got %q", stats.Error)
	}
}

func TestSuggest_OutOfRangeIgnored(t *testing.T) {
	m := NewFakeModel()
	m.PushText("1. 合法建议\n99. 越界建议\n0. 非法序号\n")

	sug, _ := Suggest(context.Background(), m, sampleFindings())
	if len(sug) != 1 {
		t.Errorf("越界/非法序号应被忽略, 得到 %v", sug)
	}
}

func TestSuggest_MultiLineCollapsed(t *testing.T) {
	m := NewFakeModel()
	m.PushText("1. 第一行建议\n第二行注入尝试\n2. 第二条建议\n")

	sug, _ := Suggest(context.Background(), m, sampleFindings())
	if strings.Contains(sug[0], "注入") {
		t.Errorf("建议应单行（首行）, 得到 %q", sug[0])
	}
	if !strings.Contains(sug[1], "第二条") {
		t.Errorf("第 2 条应正常解析: %v", sug[1])
	}
}

// TestFakeModel_SuggestionRoundDefaultEmpty M8-C3：建议轮空队列默认回放空响应
// （保留静态建议），复核轮默认回放全 CONFIRM——两轮互不串扰。
func TestFakeModel_SuggestionRoundDefaultEmpty(t *testing.T) {
	m := NewFakeModel()

	// 先复核轮（空队列默认全 CONFIRM）
	kept, reviewStats := Review(context.Background(), m, sampleFindings())
	if len(kept) != 2 || reviewStats.Dropped != 0 {
		t.Fatalf("复核轮默认应全 CONFIRM, kept=%d dropped=%d", len(kept), reviewStats.Dropped)
	}
	// 再建议轮（空队列默认空响应）
	sug, _ := Suggest(context.Background(), m, sampleFindings())
	if len(sug) != 0 {
		t.Errorf("建议轮默认应无建议（保留静态建议）, 得到 %v", sug)
	}
	// 建议轮的 prompt 应带顾问标记，复核轮不带——轮次识别依据
	if p := buildSuggestPrompt(sampleFindings()); !strings.Contains(p, suggestionRoundMark) {
		t.Error("建议轮 prompt 应包含顾问标记（FakeModel 轮次识别依据）")
	}
	if p := buildPrompt(sampleFindings()); strings.Contains(p, suggestionRoundMark) {
		t.Error("复核轮 prompt 不应包含顾问标记")
	}
}
