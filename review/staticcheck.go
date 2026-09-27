// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
package review

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"code-review-agent/findings"
)

// staticcheckLineRe 解析 staticcheck 输出行：`path:line:col: message (CODE)`。
var staticcheckLineRe = regexp.MustCompile(`^([^:\s]+):(\d+):(\d+):\s+(.+?)\s+\(([A-Z]+\d+)\)$`)

// parseStaticcheckOutput 把 staticcheck 标准输出解析为 findings（M3-D2）。
//
// staticcheck 行格式：`foo/bar.go:10:5: this value of err is never used (SA4006)`
//   - file 取输出中的相对路径（沙箱内以仓库根为工作目录）
//   - RuleID = "STATICCHECK-<code>"（不同检查码可共存于同一行，不互相去重）
//   - source = "tool:staticcheck"，severity low，confidence 0.95
//
// 输出应是已脱敏的（沙箱 Execute 统一脱敏后再解析）。
func parseStaticcheckOutput(output string) []findings.Finding {
	var result []findings.Finding
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		m := staticcheckLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		file := m[1]
		lineNo, err1 := strconv.Atoi(m[2])
		_, err2 := strconv.Atoi(m[3])
		if err1 != nil || err2 != nil {
			continue
		}
		message, code := m[4], m[5]

		f := findings.NewFinding(
			findings.SeverityLow,
			findings.CategoryQuality,
			"STATICCHECK-"+code,
			"staticcheck: "+code,
			file, lineNo,
			message,
			"根据 staticcheck 文档修复（https://staticcheck.dev/docs/checks/#"+code+"）",
			0.95,
			"tool:staticcheck",
		)
		f.EvidenceChain = findings.BuildEvidenceChain(
			file, lineNo, "STATICCHECK-"+code,
			"tool output: "+code+" (external static analysis)", 0.95,
		)
		result = append(result, *f)
	}
	return result
}

// staticcheckSummary 汇总 staticcheck 的解析结果（沙箱摘要日志用）。
func staticcheckSummary(fs []findings.Finding) string {
	if len(fs) == 0 {
		return "0 findings"
	}
	codes := map[string]int{}
	for _, f := range fs {
		codes[f.RuleID]++
	}
	parts := make([]string, 0, len(codes))
	for code, n := range codes {
		parts = append(parts, fmt.Sprintf("%s×%d", strings.TrimPrefix(code, "STATICCHECK-"), n))
	}
	return fmt.Sprintf("%d findings (%s)", len(fs), strings.Join(parts, ", "))
}
