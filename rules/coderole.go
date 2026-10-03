// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// 代码角色（真实仓库误报猎捕产出，2026-10-03）：
// 整仓审查（repo_url）把全部文件按新增行审查时，规则原本面向"生产代码 diff"
// 的置信度假设不再成立——测试 fixture 的假密钥、连本地 testserver 的
// InsecureSkipVerify、循环内 defer 清理，都是对应角色里的正确写法。
// 与其为每条规则叠"惯用法补丁"，统一引入代码角色概念：
//
//	production  生产代码（默认，不干预）
//	test        _test.go / testdata/ 下的测试代码与测试数据
//	example     examples/ / example/ 下的示例代码
//
// 非生产角色的命中一律压置信度（cap 0.65 < 0.7 阈值）进入 warnings 通道：
// 降级不删除——人仍然看得到，只是不再按生产代码标准计入风险分。
package rules

import (
	"regexp"
	"strings"

	"code-review-agent/diff"
	"code-review-agent/findings"
)

// CodeRole 文件的代码角色。
type CodeRole string

const (
	RoleProduction CodeRole = "production"
	RoleTest       CodeRole = "test"
	RoleExample    CodeRole = "example"
)

// nonProductionConfCap 非生产角色命中的置信度上限（压到 warnings 通道）。
const nonProductionConfCap = 0.65

// DetectCodeRole 按路径判定代码角色。
//
// test 判定覆盖多语言惯例（Go _test.go、Python test_*.py/*_test.py 与 tests/ 目录、
// JS *.test.js/*.spec.js、Java *Test.java）与通用 testdata/；psf/requests 整仓
// 审查中 30+ 条测试 fixture 误报的根因就是此前只认 Go 形态。
// 文档（.md/.rst/.txt 及 README/HISTORY/CHANGELOG）归入 example 角色——文档
// 中的 URL/凭据写法是讲解示例而非真实配置，降级复核而非按生产代码上报。
func DetectCodeRole(path string) CodeRole {
	lowerPath := strings.ToLower(path)
	wrapped := "/" + lowerPath + "/"
	base := lowerPath
	if idx := strings.LastIndex(lowerPath, "/"); idx >= 0 {
		base = lowerPath[idx+1:]
	}

	// 测试目录优先（tests/、test/、testdata/、__tests__/、spec/）
	for _, d := range []string{"/tests/", "/test/", "/testdata/", "/__tests__/", "/spec/"} {
		if strings.Contains(wrapped, d) {
			return RoleTest
		}
	}
	// 测试文件名形态（多语言惯例）
	switch {
	case strings.HasSuffix(base, "_test.go"),
		strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".py"),
		strings.HasSuffix(base, "_test.py"),
		strings.HasSuffix(base, ".test.js"), strings.HasSuffix(base, ".spec.js"),
		strings.HasSuffix(base, ".test.ts"), strings.HasSuffix(base, ".spec.ts"),
		strings.HasSuffix(base, "test.java"):
		return RoleTest
	}
	// 文档形态 → example（示例语义）
	switch {
	case strings.HasSuffix(lowerPath, ".md") || strings.HasSuffix(lowerPath, ".rst") || strings.HasSuffix(lowerPath, ".txt"):
		return RoleExample
	case strings.HasPrefix(base, "readme") || strings.HasPrefix(base, "history") ||
		strings.HasPrefix(base, "changelog") || strings.HasPrefix(base, "contributing"):
		return RoleExample
	}
	if strings.Contains(wrapped, "/examples/") || strings.Contains(wrapped, "/example/") {
		return RoleExample
	}
	return RoleProduction
}

// dampenFindingByRole 单条 finding 的角色降噪（引擎层逐条调用）。
// 只调整已产出 finding 的置信分层，不删除（降级不删除的项目纪律）。
func dampenFindingByRole(f *findings.Finding, role CodeRole) *findings.Finding {
	if role == RoleProduction {
		return f
	}
	if f.Confidence > nonProductionConfCap {
		f.Confidence = nonProductionConfCap
		f.Severity = findings.SeverityLow
		f.Title = f.Title + "（" + string(role) + " 代码，降级人工复核）"
	}
	return f
}

// nolintRe 行内 //nolint 指令（golangci-lint 契约：//nolint 或 //nolint:rule1,rule2）。
var nolintRe = regexp.MustCompile(`//\s*nolint(?::|\s|$)`)

// nolintLineNos 收集文件中带 //nolint 指令的新增行号集合。
// 该集合内的行是作者显式声明"已知晓并抑制本行告警"，各 lint 工具一律尊重；
// 审查器把它当成与作者意图冲突的噪音直接剔除。
func nolintLineNos(fd diff.FileDiff) map[int]bool {
	lines := make(map[int]bool)
	for _, hunk := range fd.Hunks {
		for _, line := range hunk.Lines {
			if line.Type != diff.LineAdded || line.NewLine == 0 {
				continue
			}
			if nolintRe.MatchString(line.Content) {
				lines[line.NewLine] = true
			}
		}
	}
	return lines
}

// dropNolintFindings 剔除命中 //nolint 行的 findings。
func dropNolintFindings(fs []findings.Finding, nolint map[int]bool) []findings.Finding {
	if len(nolint) == 0 {
		return fs
	}
	out := fs[:0]
	for _, f := range fs {
		if nolint[f.Line] {
			continue
		}
		out = append(out, f)
	}
	return out
}
