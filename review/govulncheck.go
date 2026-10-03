// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
package review

import (
	"encoding/json"
	"fmt"
	"strings"

	"code-review-agent/findings"
)

// ========== G3：govulncheck 依赖漏洞检测（M9） ==========
//
// 沙箱内运行官方 govulncheck -json ./...，解析其流式 JSON 输出为 findings。
// govulncheck 输出是多行 JSON 对象流（每行一个对象），我们关心其中的
// "osv" 条目（每个含一个已知漏洞）——提取 id/summary/details 与受影响模块。
//
// 边界：
//   - 需要网络下载漏洞库：container 后端网络隔离下不可用（exit 127/错误时
//     静默跳过，不影响主流程——与 staticcheck 同纪律）；
//   - severity 沿用 findings 低危 + quality 分类（依赖漏洞是供应链信息，
//     不参与风险评分维度——与 staticcheck 的 quality 分类同策略）。

// govulncheckEntry govulncheck -json 流中的单条 OSV 记录。
type govulncheckEntry struct {
	OSV struct {
		ID      string   `json:"id"`
		Summary string   `json:"summary"`
		Details string   `json:"details"`
		Aliases []string `json:"aliases"`
	} `json:"osv"`
}

// parseGovulncheckOutput 解析 govulncheck -json 流式输出，每个 OSV 条目
// 产出一条 finding（source: tool:govulncheck，quality 分类不参与评分）。
// 解析失败/无漏洞返回空切片——工具失败由调用方的 exit code 记录处理。
func parseGovulncheckOutput(output string) []findings.Finding {
	var result []findings.Finding
	dec := json.NewDecoder(strings.NewReader(output))
	for dec.More() {
		var entry govulncheckEntry
		if err := dec.Decode(&entry); err != nil {
			break // 流末尾或非 JSON 行，忽略
		}
		if entry.OSV.ID == "" {
			continue
		}
		title := strings.TrimSpace(entry.OSV.Summary)
		if title == "" {
			title = strings.TrimSpace(entry.OSV.Details)
		}
		if len(title) > 120 {
			title = title[:120]
		}
		alias := ""
		if len(entry.OSV.Aliases) > 0 {
			alias = " (" + strings.Join(entry.OSV.Aliases, ", ") + ")"
		}
		f := findings.NewFinding(
			findings.SeverityLow, findings.CategoryQuality, "GOV-GEN-001",
			"依赖漏洞: "+title,
			"go.mod", 1,
			entry.OSV.ID+alias,
			"升级受影响的依赖模块到修复版本；参考 "+entry.OSV.ID+" 的官方修复建议",
			0.90,
			"tool:govulncheck",
		)
		f.EvidenceChain = findings.BuildEvidenceChain(
			"go.mod", 1, "GOV-GEN-001",
			"osv id="+entry.OSV.ID+" detected by govulncheck", 0.90)
		result = append(result, *f)
	}
	return result
}

// govulncheckSummary 汇总描述（Verbose 日志用）。
func govulncheckSummary(fs []findings.Finding) string {
	if len(fs) == 0 {
		return "未发现已知依赖漏洞"
	}
	ids := make([]string, 0, len(fs))
	for _, f := range fs {
		ids = append(ids, strings.TrimPrefix(f.Title, "依赖漏洞: "))
	}
	return fmt.Sprintf("发现 %d 个已知依赖漏洞: %s", len(fs), strings.Join(ids, ", "))
}
