// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//
// M7-F6（D6）：单文件 HTML 交互式报告。
//
// 自包含约束：无外部资源引用（无 CDN/无图片/无外链字体），CSS 与 JS 全部内联，
// 离线可直接发人。交互用原生 <details>/<summary> 折叠 + ~15 行筛选 JS。
// 脱敏纪律：finding 的 evidence 在 findings.NewFinding 出口已统一脱敏，
// 此处只做 HTML 转义，不引入新的明文来源。
package report

import (
	"fmt"
	"html"
	"sort"
	"strings"
	"time"

	"code-review-agent/findings"
)

// ToHTML 渲染自包含 HTML 报告。
func (rep *ReviewReport) ToHTML() string {
	var b strings.Builder
	b.WriteString(htmlHead(rep))
	b.WriteString(htmlSummary(rep))
	b.WriteString(htmlDimensions(rep))
	b.WriteString(htmlRepoScan(rep))
	b.WriteString(htmlFindings(rep))
	b.WriteString(htmlFooter(rep))
	return b.String()
}

func htmlHead(rep *ReviewReport) string {
	title := html.EscapeString("代码审查报告 " + rep.TaskID)
	return `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>` + title + `</title>
<style>
  :root { --ink:#1a2233; --dim:#6b7280; --line:#e5e7eb; --accent:#4f6bed;
    --red:#dc2626; --red-soft:#fef2f2; --orange:#d97706; --orange-soft:#fffbeb;
    --blue:#2563eb; --blue-soft:#eff6ff; --green:#16a34a; --violet:#7c3aed; }
  * { box-sizing:border-box; margin:0; padding:0; }
  body { font-family:-apple-system,"PingFang SC","Microsoft YaHei",sans-serif;
    color:var(--ink); background:#f7f8fa; padding:32px 16px; }
  .wrap { max-width:900px; margin:0 auto; }
  .card { background:#fff; border:1px solid var(--line); border-radius:12px; padding:20px 24px; margin-bottom:16px; }
  h1 { font-size:20px; margin-bottom:4px; }
  .meta { color:var(--dim); font-size:12px; line-height:1.8; }
  .mono { font-family:"SF Mono",Menlo,Consolas,monospace; font-size:12px; }
  .hero { display:flex; align-items:center; gap:24px; }
  .score { font-size:44px; font-weight:700; line-height:1; }
  .score small { font-size:14px; color:var(--dim); font-weight:400; }
  .grade { font-size:22px; font-weight:700; }
  .chips { display:flex; flex-wrap:wrap; gap:8px; margin-top:10px; }
  .chip { font-size:12px; padding:3px 10px; border-radius:99px; border:1px solid var(--line);
    background:#fff; cursor:pointer; user-select:none; }
  .chip.on { border-color:var(--accent); color:var(--accent); background:#eef1fe; }
  .stat { color:var(--dim); font-size:12px; }
  .stat b { color:var(--ink); font-size:15px; }
  details { border:1px solid var(--line); border-radius:8px; margin-bottom:8px; background:#fff; overflow:hidden; }
  summary { padding:10px 14px; cursor:pointer; display:flex; align-items:center; gap:10px; font-size:13px; list-style:none; }
  summary::-webkit-details-marker { display:none; }
  summary:hover { background:#fafbfc; }
  .badge { font-size:11px; padding:2px 8px; border-radius:99px; flex-shrink:0; }
  .b-high { color:var(--red); background:var(--red-soft); }
  .b-medium { color:var(--orange); background:var(--orange-soft); }
  .b-low { color:var(--blue); background:var(--blue-soft); }
  .b-info { color:var(--dim); background:#f3f4f6; }
  .b-warn { color:var(--violet); background:#f5f3ff; }
  .loc { color:var(--dim); font-family:Menlo,Consolas,monospace; font-size:12px; flex-shrink:0; }
  .ftitle { flex:1; min-width:0; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
  .fmeta { color:var(--dim); font-size:11px; flex-shrink:0; }
  .fbody { padding:12px 14px; border-top:1px solid var(--line); }
  pre { background:#0f172a; color:#e2e8f0; padding:12px 14px; border-radius:8px;
    overflow-x:auto; font-size:12px; line-height:1.6; font-family:Menlo,Consolas,monospace; white-space:pre-wrap; word-break:break-all; }
  .redacted { color:#fbbf24; }
  .rec { margin-top:10px; font-size:13px; line-height:1.7; }
  .rec b { color:var(--green); }
  .bar-row { display:flex; align-items:center; gap:10px; margin-bottom:8px; font-size:12px; }
  .bar-label { width:110px; color:var(--dim); text-align:right; flex-shrink:0; }
  .bar-track { flex:1; height:8px; background:#f1f2f4; border-radius:99px; overflow:hidden; }
  .bar-fill { height:100%; border-radius:99px; background:var(--accent); }
  .bar-num { width:64px; flex-shrink:0; }
  .empty { color:var(--dim); font-size:13px; padding:14px 0; text-align:center; }
  footer { color:var(--dim); font-size:11px; text-align:center; margin-top:24px; line-height:1.8; }
  @media print { body { background:#fff; padding:0; } .card { break-inside:avoid; } }
</style>
</head>
<body><div class="wrap">
<div class="card hero">
  <div>
    <div class="score">` + fmt.Sprintf("%.0f", rep.Monitor.RiskScore) + `<small> /100</small></div>
    <div class="grade">` + html.EscapeString(rep.Monitor.RiskGrade) + ` 级</div>
  </div>
  <div style="flex:1">
    <h1>代码审查报告</h1>
    <div class="meta mono">任务 ` + html.EscapeString(rep.TaskID) + ` · ` +
		html.EscapeString(InputTypeLabel(rep.InputType)) + ` · ` + html.EscapeString(DisplayInputPath(rep.InputPath)) + `<br>
    ` + html.EscapeString(rep.StartTime) + ` → 耗时 ` + html.EscapeString(rep.Duration) +
		` · 扫描 ` + fmt.Sprintf("%d", rep.FilesCount) + ` 个文件（Go ` + fmt.Sprintf("%d", rep.GoFilesCount) + `）</div>
    <div class="chips">` + severityChips(rep.Summary.BySeverity) + `</div>
  </div>
</div>
`
}

// severityChips 严重度统计 chips（非交互，纯展示）。
func severityChips(bySev map[string]int) string {
	labels := [][3]string{ // id, label, class
		{"high", "高危", "b-high"}, {"medium", "中危", "b-medium"},
		{"low", "低危", "b-low"}, {"info", "信息", "b-info"},
	}
	var out strings.Builder
	for _, l := range labels {
		n := bySev[l[0]]
		out.WriteString(fmt.Sprintf(`<span class="badge %s">%s <b>%d</b></span>`, l[2], l[1], n))
	}
	if bySev == nil || total(bySev) == 0 {
		out.WriteString(`<span class="badge" style="color:var(--green);background:#f0fdf4">未发现问题</span>`)
	}
	return out.String()
}

func total(m map[string]int) int {
	s := 0
	for _, v := range m {
		s += v
	}
	return s
}

func htmlSummary(rep *ReviewReport) string {
	var b strings.Builder
	b.WriteString(`<div class="card"><div class="chips" style="margin-top:0">`)
	b.WriteString(fmt.Sprintf(`<span class="stat">发现 <b>%d</b></span>`, rep.Summary.TotalFindings))
	b.WriteString(fmt.Sprintf(`<span class="stat">警告 <b>%d</b></span>`, rep.Summary.TotalWarnings))
	b.WriteString(fmt.Sprintf(`<span class="stat">去重移除 <b>%d</b></span>`, rep.Summary.DedupRemoved))
	if rep.SandboxSummary.TotalRuns > 0 {
		b.WriteString(fmt.Sprintf(`<span class="stat">沙箱 <b>%d/%d</b> 通过</span>`,
			rep.SandboxSummary.Successful, rep.SandboxSummary.TotalRuns))
	}
	if rep.Monitor.LLMMode != "" {
		b.WriteString(fmt.Sprintf(`<span class="stat">LLM 复核 <b>%s</b>（剔除 %d）</span>`,
			html.EscapeString(rep.Monitor.LLMMode), rep.Monitor.LLMDropped))
	}
	if rep.Skill != nil && rep.Skill.Loaded {
		b.WriteString(fmt.Sprintf(`<span class="stat mono">审查引擎 <b>%s %s</b></span>`,
			html.EscapeString(rep.Skill.Name), html.EscapeString(rep.Skill.Version)))
	}
	b.WriteString(`</div></div>`)
	return b.String()
}

// htmlDimensions 六维评分横条（无 breakdown 时整块省略）。
func htmlDimensions(rep *ReviewReport) string {
	if len(rep.Monitor.RiskBreakdown) == 0 {
		return ""
	}
	type row struct {
		name   string
		score  float64
		weight float64
	}
	rows := make([]row, 0, len(rep.Monitor.RiskBreakdown))
	for _, d := range rep.Monitor.RiskBreakdown {
		rows = append(rows, row{d.Name, d.Score, d.Weight})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].weight > rows[j].weight })

	var b strings.Builder
	b.WriteString(`<div class="card"><div class="meta" style="margin-bottom:12px"><b style="color:var(--ink)">风险评分维度</b>（加权求和 0-100）</div>`)
	for _, r := range rows {
		pct := r.score // 0-100
		b.WriteString(`<div class="bar-row">
  <div class="bar-label">` + html.EscapeString(r.name) + `</div>
  <div class="bar-track"><div class="bar-fill" style="width:` + fmt.Sprintf("%.1f", pct) + `%"></div></div>
  <div class="bar-num">` + fmt.Sprintf("%.0f 分 · 权重 %.0f%%", r.score, r.weight*100) + `</div>
</div>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

// htmlRepoScan 整查聚合卡（M9-G4：仅整查模式产生，其余模式整块省略）。
// 目录分布复用六维评分的横条样式，条长 = 该目录问题数占比。
func htmlRepoScan(rep *ReviewReport) string {
	agg := rep.RepoScan
	if agg == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<div class="card"><div class="meta" style="margin-bottom:12px"><b style="color:var(--ink)">文件风险分布</b> · 采集 ` +
		fmt.Sprintf("%d", agg.FilesCollected) + ` 个文件`)
	if agg.FilesSkipped > 0 {
		b.WriteString(fmt.Sprintf(` · 跳过 <b>%d</b>`, agg.FilesSkipped))
		if len(agg.SkipReasons) > 0 {
			keys := make([]string, 0, len(agg.SkipReasons))
			for k := range agg.SkipReasons {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				b.WriteString(fmt.Sprintf(` <span class="badge b-info">%s %d</span>`,
					html.EscapeString(skipReasonLabel(k)), agg.SkipReasons[k]))
			}
		}
	}
	b.WriteString(`</div>`)

	if len(agg.ByDirectory) > 0 {
		maxIssues := 1
		for _, d := range agg.ByDirectory {
			if n := d.Findings + d.Warnings; n > maxIssues {
				maxIssues = n
			}
		}
		b.WriteString(`<div class="meta" style="margin-bottom:8px">目录分布（按问题数排序）</div>`)
		for _, d := range agg.ByDirectory {
			issues := d.Findings + d.Warnings
			pct := float64(issues) / float64(maxIssues) * 100
			b.WriteString(`<div class="bar-row">
  <div class="bar-label" style="width:170px;text-align:left;overflow:hidden;text-overflow:ellipsis;white-space:nowrap" title="` +
				html.EscapeString(dirDisplay(d.Dir)) + `">` + html.EscapeString(dirDisplay(d.Dir)) + `</div>
  <div class="bar-track"><div class="bar-fill" style="width:` + fmt.Sprintf("%.1f", pct) + `%"></div></div>
  <div class="bar-num">` + fmt.Sprintf("%d 问题 · %d 文件", issues, d.Files) + `</div>
</div>`)
		}
	}

	if len(agg.TopFiles) > 0 {
		b.WriteString(`<div class="meta" style="margin:12px 0 8px">Top 风险文件</div>`)
		for _, f := range agg.TopFiles {
			b.WriteString(`<div class="bar-row">
  <div class="bar-label" style="width:220px;text-align:left;overflow:hidden;text-overflow:ellipsis;white-space:nowrap" title="` +
				html.EscapeString(f.File) + `">` + html.EscapeString(f.File) + `</div>
  <div class="bar-num" style="width:auto">发现 <b>` + fmt.Sprintf("%d", f.Findings) + `</b> · 警告 ` + fmt.Sprintf("%d", f.Warnings) + `</div>
</div>`)
		}
	}

	b.WriteString(`</div>`)
	return b.String()
}

func htmlFindings(rep *ReviewReport) string {
	var b strings.Builder

	b.WriteString(`<div class="card">
<div class="chips" style="margin-top:0;margin-bottom:14px" id="filters">
  <span class="chip on" data-f="all" onclick="flt('all',this)">全部</span>
  <span class="chip" data-f="high" onclick="flt('high',this)">高危</span>
  <span class="chip" data-f="medium" onclick="flt('medium',this)">中危</span>
  <span class="chip" data-f="low" onclick="flt('low',this)">低危</span>
  <span class="chip" data-f="info" onclick="flt('info',this)">信息</span>
  <span class="chip" data-f="warning" onclick="flt('warning',this)">警告</span>
</div>
<div id="list">`)

	b.WriteString(findingsHTML(rep.Findings, false))
	b.WriteString(`<div data-sev="warning-sep"` + warnSepAttr(rep) + `></div>`)
	b.WriteString(findingsHTML(rep.Warnings, true))

	if len(rep.Findings) == 0 && len(rep.Warnings) == 0 {
		b.WriteString(`<div class="empty">本次审查未发现问题</div>`)
	}
	b.WriteString(`</div></div>`)

	b.WriteString(`<script>
function flt(f, el) {
  document.querySelectorAll('#filters .chip').forEach(function (c) { c.classList.remove('on'); });
  el.classList.add('on');
  document.querySelectorAll('#list details').forEach(function (d) {
    var sev = d.getAttribute('data-sev');
    d.style.display = (f === 'all' || sev === f || (f === 'high' && sev === 'high')) ? '' : 'none';
  });
  var sep = document.querySelector('[data-sev="warning-sep"]');
  if (sep) sep.style.display = (f === 'all' || f === 'warning') ? '' : 'none';
}
</script>`)
	return b.String()
}

// warnSepAttr 有警告时才渲染警告分隔标题。
func warnSepAttr(rep *ReviewReport) string {
	if len(rep.Warnings) == 0 {
		return ` style="display:none"`
	}
	return ` style="display:none"` // 分隔由 warning badge 本身区分，占位保持筛选逻辑简单
}

// findingsHTML 渲染一组 finding 为可折叠 details。
func findingsHTML(list []findings.Finding, warning bool) string {
	var b strings.Builder
	for i := range list {
		f := list[i]
		cls := string(f.Severity)
		if warning {
			cls = "warning"
		}
		b.WriteString(`<details data-sev="` + html.EscapeString(cls) + `">
<summary>
  <span class="badge ` + badgeClass(cls) + `">` + html.EscapeString(sevLabel(cls)) + `</span>
  <span class="loc">` + html.EscapeString(f.File) + `:` + fmt.Sprintf("%d", f.Line) + `</span>
  <span class="ftitle">` + html.EscapeString(f.Title) + `</span>
  <span class="fmeta">conf ` + fmt.Sprintf("%.2f", f.Confidence) + ` · ` + html.EscapeString(f.RuleID) + `</span>
</summary>
<div class="fbody">
  <pre>` + redactHighlight(f.Evidence) + `</pre>
  <div class="rec"><b>修复建议</b> ` + html.EscapeString(f.Recommendation) + `</div>
</div>
</details>`)
	}
	return b.String()
}

// badgeClass 把严重级别映射为徽章样式类。
func badgeClass(sev string) string {
	switch sev {
	case "high":
		return "b-high"
	case "medium":
		return "b-medium"
	case "low":
		return "b-low"
	case "warning":
		return "b-warn"
	default:
		return "b-info"
	}
}

func sevLabel(sev string) string {
	switch sev {
	case "high":
		return "高危"
	case "medium":
		return "中危"
	case "low":
		return "低危"
	case "warning":
		return "警告"
	default:
		return "信息"
	}
}

// redactHighlight 把脱敏占位标黄（evidence 已在出口统一脱敏，此处只做展示增强）。
func redactHighlight(evidence string) string {
	e := html.EscapeString(evidence)
	e = strings.ReplaceAll(e, "***REDACTED***", `<span class="redacted">***REDACTED***</span>`)
	e = strings.ReplaceAll(e, "***PRIVATE_KEY_REMOVED***", `<span class="redacted">***PRIVATE_KEY_REMOVED***</span>`)
	return e
}

func htmlFooter(rep *ReviewReport) string {
	gen := time.Now().Format("2006-01-02 15:04:05")
	return `<footer>
本报告由 code-review-agent 自动生成 · 生成时间 ` + gen + ` · 任务 ` + html.EscapeString(rep.TaskID) + `<br>
代码证据中的敏感信息已自动脱敏（` + `<span class="redacted" style="font-size:11px">***REDACTED***</span>` + `）；低置信度条目标记为「警告」，需人工复核。
</footer>
</div></body></html>`
}
