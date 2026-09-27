// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//
// Package llmreview 用 LLM 复核规则引擎产出的候选 findings（M4-C1/C2）。
//
// 设计原则：
//   - 规则保证召回，LLM 保证精度：DENY 的候选被剔除（降误报），CONFIRM 保留（不降召回）
//   - LLM 不在默认链路：Mode 为空时完全不介入，纯规则行为与 M2 数据集门禁一致
//   - 复核是增强不是依赖：模型不可用/输出不可解析时保守保留候选，审查永不因 LLM 失败而失败
//   - 证据先行脱敏：送入 prompt 的 evidence 已经统一 Redactor 脱敏
package llmreview

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"trpc.group/trpc-go/trpc-agent-go/model"

	"code-review-agent/findings"
)

// Mode LLM 复核模式。
type Mode string

const (
	ModeOff    Mode = ""       // 关闭（默认）
	ModeFake   Mode = "fake"   // 内置确定性假模型：无网络、全链路可复现（M4-C2）
	ModeOpenAI Mode = "openai" // OpenAI 兼容 API（--llm-base-url 可指向 ollama/vLLM 等兼容端点）
)

// Stats 复核统计（进报告 Monitor）。
type Stats struct {
	Mode     string `json:"mode"`            // 复核模式（空 = 未启用）
	Reviewed int    `json:"reviewed"`        // 送审候选数
	Dropped  int    `json:"dropped"`         // 被 LLM 否决剔除的候选数
	Error    string `json:"error,omitempty"` // 复核失败原因（失败时保守保留全部候选）
}

// Options 复核配置。
type Options struct {
	Mode      Mode
	ModelName string // openai 模式的模型名（默认 gpt-4o-mini）
	BaseURL   string // openai 兼容端点（如 ollama: http://localhost:11434/v1）
	APIKey    string // openai 模式必填
}

// verdictRe 解析单个判定行：`12. DENY: 理由`。
var verdictRe = regexp.MustCompile(`^(\d+)\.\s*(CONFIRM|DENY)\b[:\s]*(.*)$`)

// buildPrompt 构建批量复核 prompt（一次请求复核全部候选，降低成本与延迟）。
// evidence 已在 findings 出口统一脱敏，这里不再二次处理。
func buildPrompt(cands []findings.Finding) string {
	var b strings.Builder
	b.WriteString("你是代码审查复核员。下面是静态规则引擎产出的候选问题清单。\n")
	b.WriteString("逐条判断是否为真实问题，严格按以下格式输出（每条一行，共 ")
	b.WriteString(strconv.Itoa(len(cands)))
	b.WriteString(" 行，不要输出任何其他内容）：\n")
	b.WriteString("序号. CONFIRM: 理由   或   序号. DENY: 理由\n\n候选清单：\n")
	for i, f := range cands {
		fmt.Fprintf(&b, "%d. [%s][%s] %s:%d %s\n", i+1, f.RuleID, f.Severity, f.File, f.Line, f.Title)
		if ev := strings.TrimSpace(f.Evidence); ev != "" {
			fmt.Fprintf(&b, "   证据: %s\n", oneLine(ev))
		}
	}
	return b.String()
}

// oneLine 压缩多行证据为单行（prompt 整洁 + 防 prompt 注入换行）。
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ⏎ ")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// parseVerdicts 从模型输出解析判定表（按候选序号索引）。
// 无法解析的候选不出现在返回表中——调用方对缺失序号保守保留。
func parseVerdicts(content string, n int) map[int]bool {
	verdicts := make(map[int]bool) // idx(0-based) -> true=CONFIRM
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		m := verdictRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		idx, err := strconv.Atoi(m[1])
		if err != nil || idx < 1 || idx > n {
			continue
		}
		verdicts[idx-1] = m[2] == "CONFIRM"
	}
	return verdicts
}

// collectContent 汇总流式响应的全部文本（跨 chunk 拼接）。
func collectContent(ch <-chan *model.Response, err error) (string, error) {
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for resp := range ch {
		if resp == nil {
			continue
		}
		if resp.Error != nil {
			return "", fmt.Errorf("模型返回错误: %s", resp.Error.Message)
		}
		for _, choice := range resp.Choices {
			if choice.Delta.Content != "" {
				b.WriteString(choice.Delta.Content)
			}
			if choice.Message.Content != "" {
				b.WriteString(choice.Message.Content)
			}
		}
	}
	return b.String(), nil
}

// Review 批量复核候选 findings。
//
// 语义：DENY → 剔除；CONFIRM → 保留；输出缺失/不可解析 → 保守保留。
// 返回保留的候选列表与统计。LLM 调用失败时原样返回全部候选（审查不因 LLM 失败而失败）。
func Review(ctx context.Context, mdl model.Model, cands []findings.Finding) ([]findings.Finding, Stats) {
	stats := Stats{Reviewed: len(cands)}
	if len(cands) == 0 || mdl == nil {
		return cands, stats
	}

	req := &model.Request{
		Messages: []model.Message{
			{Role: model.RoleUser, Content: buildPrompt(cands)},
		},
	}
	content, err := collectContent(mdl.GenerateContent(ctx, req))
	if err != nil {
		stats.Error = err.Error()
		return cands, stats
	}

	verdicts := parseVerdicts(content, len(cands))
	kept := make([]findings.Finding, 0, len(cands))
	for i, f := range cands {
		confirm, ok := verdicts[i]
		if !ok {
			kept = append(kept, f) // 保守保留
			continue
		}
		if confirm {
			kept = append(kept, f)
			continue
		}
		stats.Dropped++
	}
	return kept, stats
}
