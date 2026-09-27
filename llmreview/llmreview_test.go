// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
package llmreview

import (
	"context"
	"errors"
	"strings"
	"testing"

	"trpc.group/trpc-go/trpc-agent-go/model"

	"code-review-agent/findings"
)

func mkCands() []findings.Finding {
	a := findings.NewFinding(findings.SeverityHigh, findings.CategorySecurity, "SEC-AST-001",
		"疑似硬编码密钥", "auth.go", 4, `var cred = "sk-***REDACTED..."`, "用环境变量", 0.9, "test")
	b := findings.NewFinding(findings.SeverityMedium, findings.CategoryResource, "RES-AST-001",
		"资源可能未关闭", "pool.go", 6, "db, err := sql.Open(...)", "defer db.Close()", 0.8, "test")
	return []findings.Finding{*a, *b}
}

// TestBuildPrompt M4-C1：prompt 含候选清单与格式要求，索引对齐。
func TestBuildPrompt(t *testing.T) {
	cands := mkCands()
	p := buildPrompt(cands)
	if !strings.Contains(p, "共 2 行") {
		t.Errorf("prompt 应注明候选数, 得到:\n%s", p)
	}
	if !strings.Contains(p, "1. [SEC-AST-001][high] auth.go:4") {
		t.Errorf("prompt 应含候选 1 定位:\n%s", p)
	}
	if !strings.Contains(p, "2. [RES-AST-001]") {
		t.Errorf("prompt 应含候选 2:\n%s", p)
	}
	if strings.Count(p, "\n") > 0 && strings.Contains(p, "sk-live") {
		t.Error("prompt 不应含明文密钥（evidence 已脱敏）")
	}
}

// TestParseVerdicts 判定表解析：正常/越界/垃圾行。
func TestParseVerdicts(t *testing.T) {
	content := "1. CONFIRM: 真实密钥\n2. deny: 误报（小写应忽略）\n3. DENY: 不成立\n垃圾行\n99. CONFIRM: 越界"
	v := parseVerdicts(content, 3)
	if len(v) != 2 {
		t.Fatalf("应解析出 2 条（大写 CONFIRM/DENY + 越界丢弃）, 得到 %d: %v", len(v), v)
	}
	if !v[0] {
		t.Error("候选 1 应为 CONFIRM")
	}
	if v[2] {
		t.Error("候选 3 应为 DENY")
	}
}

// TestReview_DenyDrops C1 核心语义：DENY 剔除、CONFIRM 保留。
func TestReview_DenyDrops(t *testing.T) {
	cands := mkCands()
	fm := NewFakeModel()
	fm.PushText("1. CONFIRM: 真实问题\n2. DENY: 连接已在上文关闭\n")

	kept, stats := Review(context.Background(), fm, cands)
	if stats.Dropped != 1 || stats.Reviewed != 2 {
		t.Fatalf("stats = %+v, 期望 reviewed=2 dropped=1", stats)
	}
	if len(kept) != 1 || kept[0].RuleID != "SEC-AST-001" {
		t.Fatalf("应保留候选 1, 得到 %d 条", len(kept))
	}
}

// TestReview_MissingVerdictKeeps 输出缺失序号时保守保留。
func TestReview_MissingVerdictKeeps(t *testing.T) {
	cands := mkCands()
	fm := NewFakeModel()
	fm.PushText("1. CONFIRM: ok\n") // 候选 2 无判定

	kept, stats := Review(context.Background(), fm, cands)
	if len(kept) != 2 || stats.Dropped != 0 {
		t.Fatalf("缺失判定应保守保留, 得到 kept=%d dropped=%d", len(kept), stats.Dropped)
	}
}

// failingModel 始终返回系统级错误的模型（测 Review 的失败保守路径）。
type failingModel struct{}

func (failingModel) GenerateContent(context.Context, *model.Request) (<-chan *model.Response, error) {
	return nil, errors.New("模拟网络故障")
}
func (failingModel) Info() model.Info { return model.Info{Name: "failing"} }

// TestReview_ModelErrorKeepsAll 模型失败：审查不因 LLM 失败而失败。
func TestReview_ModelErrorKeepsAll(t *testing.T) {
	cands := mkCands()

	kept, stats := Review(context.Background(), failingModel{}, cands)
	if len(kept) != 2 {
		t.Fatalf("模型失败应原样保留全部候选, 得到 %d", len(kept))
	}
	if stats.Error == "" {
		t.Error("应记录失败原因")
	}
}

// TestFakeModel_Sequence 按序回放 + 耗尽报错（C2 假模型语义）。
func TestFakeModel_Sequence(t *testing.T) {
	fm := NewFakeModel()
	fm.PushText("first")
	fm.PushText("second")

	// 耗尽后的探测请求带 2 条候选形态的 prompt → 默认回放 2 行全 CONFIRM
	prompt := "候选清单：\n1. [SEC-AST-001][high] auth.go:4 t\n2. [RES-AST-001][medium] pool.go:6 t\n"
	req := &model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: prompt}}}
	first, err := collectContent(fm.GenerateContent(context.Background(), req))
	if err != nil || first != "first" {
		t.Fatalf("第一条 = %q err=%v", first, err)
	}
	second, err := collectContent(fm.GenerateContent(context.Background(), req))
	if err != nil || second != "second" {
		t.Fatalf("第二条 = %q err=%v", second, err)
	}
	if fm.Consumed() != 2 {
		t.Fatalf("consumed = %d, 期望 2", fm.Consumed())
	}
	// 队列耗尽 → 默认全 CONFIRM（--fake-model 自包含语义）
	def, err := collectContent(fm.GenerateContent(context.Background(), req))
	if err != nil {
		t.Fatalf("耗尽后应回放默认响应: %v", err)
	}
	if !strings.Contains(def, "1. CONFIRM") || !strings.Contains(def, "2. CONFIRM") {
		t.Errorf("默认响应应为按候选数的全 CONFIRM 文本, 得到 %q", def)
	}
}
