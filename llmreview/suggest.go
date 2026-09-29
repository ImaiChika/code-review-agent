// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//
// M8-C3：LLM 修复建议生成。
//
// 对复核确认保留的 findings，让 LLM 基于上下文生成一行可执行的修复建议，
// 替换静态 recommendation 文案。语义与 Review 一致——增强不是依赖：
// 建议缺失/解析失败/模型失败时保守保留原静态建议，审查永不因 LLM 失败而失败。
// 替换后的建议由调用方统一过 Redactor（与 finding 出口同一脱敏纪律）。
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

// SuggestStats 建议生成统计（进报告 Monitor）。
type SuggestStats struct {
	Requested int    `json:"-"`               // 送出建议生成的条数
	Error     string `json:"error,omitempty"` // 失败原因（失败时全部保留原建议）
}

// suggestRe 解析单条建议行：`12. 建议文本`。
// 与判定协议（CONFIRM/DENY）不同的宽松格式——建议是自由文本，
// 但仍限定单行（模型输出的多行建议只有首行被采纳，防注入与格式漂移）。
var suggestRe = regexp.MustCompile(`^(\d+)\.\s*(.+)$`)

// buildSuggestPrompt 构建批量建议 prompt。evidence 已在 findings 出口统一脱敏。
// 固定开头 "你是代码审查修复顾问" 是 FakeModel 识别建议轮的标记（建议轮默认
// 回放空响应 → 保留静态建议，保证 --fake-model 自包含语义）。
func buildSuggestPrompt(fs []findings.Finding) string {
	var b strings.Builder
	b.WriteString("你是代码审查修复顾问。下面是已确认的代码问题清单（含现有修复建议）。\n")
	b.WriteString("针对每条问题给出更具体、可执行的修复建议（说明用什么 API/模式、注意事项）；\n")
	b.WriteString("每条建议必须独立完整，不要用「同上」等引用其他条目的写法（建议会逐条独立展示）。\n")
	b.WriteString("严格按以下格式输出（每条一行，共 ")
	b.WriteString(strconv.Itoa(len(fs)))
	b.WriteString(" 行，不要输出任何其他内容）：\n")
	b.WriteString("序号. 一行修复建议\n\n问题清单：\n")
	for i, f := range fs {
		fmt.Fprintf(&b, "%d. [%s] %s:%d %s\n", i+1, f.RuleID, f.File, f.Line, f.Title)
		if ev := strings.TrimSpace(f.Evidence); ev != "" {
			fmt.Fprintf(&b, "   证据: %s\n", oneLine(ev))
		}
		if rec := strings.TrimSpace(f.Recommendation); rec != "" {
			fmt.Fprintf(&b, "   现有建议: %s\n", oneLine(rec))
		}
	}
	return b.String()
}

// parseSuggestions 从模型输出解析建议表（按序号索引，0-based）。
// 缺失序号不出现在返回表中——调用方对缺失条目保留原静态建议。
func parseSuggestions(content string, n int) map[int]string {
	out := make(map[int]string)
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		m := suggestRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		idx, err := strconv.Atoi(m[1])
		if err != nil || idx < 1 || idx > n {
			continue
		}
		text := strings.TrimSpace(m[2])
		if text == "" {
			continue
		}
		if _, dup := out[idx-1]; !dup { // 首个匹配优先
			out[idx-1] = oneLine(text)
		}
	}
	return out
}

// Suggest 批量生成修复建议。
//
// 返回 idx(0-based) → 新建议文本；缺失的 idx 表示保留原建议。
// 模型调用失败时返回空表 + Error（调用方全部保留原建议）。
func Suggest(ctx context.Context, mdl model.Model, fs []findings.Finding) (map[int]string, SuggestStats) {
	stats := SuggestStats{Requested: len(fs)}
	if len(fs) == 0 || mdl == nil {
		return nil, stats
	}

	req := &model.Request{
		Messages: []model.Message{
			{Role: model.RoleUser, Content: buildSuggestPrompt(fs)},
		},
	}
	content, err := collectContent(mdl.GenerateContent(ctx, req))
	if err != nil {
		stats.Error = err.Error()
		return nil, stats
	}
	return parseSuggestions(content, len(fs)), stats
}
