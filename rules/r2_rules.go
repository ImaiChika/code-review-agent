// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
package rules

import (
	"regexp"
	"strings"

	"code-review-agent/diff"
	"code-review-agent/findings"
)

// ========== R2：单行规则批量（§8.4 第二批） ==========
//
// 偏离说明：§8.4 原计划本批三条走 YAML DSL，但 DSL 仅在 --rules-dir 显式指定时
// 加载（默认关闭），数据集门禁无法覆盖其检测行为——按 §8.6"DSL 写不出（或无门禁）
// 就升级内置"的精神全部落内置，逐条仍走标注先行的六步流程。
//
// 本批统一纪律（§8.6 红线）：单行/窄上下文规则是误报率红线的主战场，
// confidence 一律 ≤ 0.85，其中 context.Background 误报风险最高的压 0.7。

// ---------- SEC-AST-005: TLS 证书校验关闭 ----------

type TokenInsecureTLSRule struct{}

// NewTokenInsecureTLSRule 创建 InsecureSkipVerify 检测规则实例。
func NewTokenInsecureTLSRule() *TokenInsecureTLSRule { return &TokenInsecureTLSRule{} }

func (r *TokenInsecureTLSRule) ID() string                  { return "SEC-AST-005" }
func (r *TokenInsecureTLSRule) Name() string                { return "Token 感知的 TLS 校验关闭检测" }
func (r *TokenInsecureTLSRule) Severity() findings.Severity { return findings.SeverityHigh }
func (r *TokenInsecureTLSRule) Category() findings.Category { return findings.CategorySecurity }

func (r *TokenInsecureTLSRule) Check(fd diff.FileDiff) ([]findings.Finding, error) {
	var result []findings.Finding

	// 通用文件门控：本规则检查的是 Go 语义，非 Go 文本（Markdown/模板/配置）不适用。
	if !fd.IsGoFile() {
		return nil, nil
	}
	for _, line := range collectAddedLines(fd) {
		if isCommentLine(line.Content) {
			continue
		}
		if strings.Contains(line.Content, "InsecureSkipVerify") && strings.Contains(line.Content, "true") {
			f := findings.NewFinding(
				r.Severity(), r.Category(), r.ID(),
				"Token 感知：TLS 证书校验被关闭（InsecureSkipVerify）",
				fd.NewPath, line.NewLine,
				line.Content,
				"生产代码不要跳过证书校验；内联测试环境确需跳过时收窄作用域并加注释说明",
				0.85,
				"token:insecure_tls",
			)
			f.EvidenceChain = findings.BuildEvidenceChain(fd.NewPath, line.NewLine, r.ID(),
				"InsecureSkipVerify = true in tls config", 0.85)
			result = append(result, *f)
		}
	}
	return result, nil
}

// ---------- CTX-AST-002: 签名带 ctx 的函数内使用 context.Background/TODO ----------

type TokenContextRootRule struct{}

// NewTokenContextRootRule 创建 context 根替换检测规则实例。
func NewTokenContextRootRule() *TokenContextRootRule { return &TokenContextRootRule{} }

func (r *TokenContextRootRule) ID() string                  { return "CTX-AST-002" }
func (r *TokenContextRootRule) Name() string                { return "Token 感知的 context 根替换检测" }
func (r *TokenContextRootRule) Severity() findings.Severity { return findings.SeverityLow }
func (r *TokenContextRootRule) Category() findings.Category { return findings.CategoryConcurrency }

var ctxRootCallRe = regexp.MustCompile(`context\.(?:Background|TODO)\(`)

// funcHasContextParam 判断函数签名行是否带 context.Context 参数。
// 注意：必须出现在参数区（第一个右括号之前）——返回值位置的
// `func f() context.Context` 不算接收了调用方 ctx。
func funcHasContextParam(sigLine string) bool {
	firstClose := strings.Index(sigLine, ")")
	if firstClose < 0 {
		return false
	}
	if !strings.Contains(sigLine[:firstClose], "context.Context") {
		return false
	}
	t := strings.TrimSpace(sigLine)
	// main / init 没有 ctx 参数语义，即便写法命中也排除
	name := extractFuncName(t)
	return name != "main" && name != "init"
}

// extractFuncName 提取 func 行的函数名（含接收者形态 func (r *T) Name）。
func extractFuncName(line string) string {
	t := strings.TrimSpace(line)
	if !strings.HasPrefix(t, "func") {
		return ""
	}
	if strings.HasPrefix(t, "func (") || strings.HasPrefix(t, "func(") {
		// 方法：") " 之后的名称段
		if i := strings.Index(t, ") "); i >= 0 {
			rest := t[i+2:]
			o := strings.Index(rest, "(")
			if o < 0 {
				return ""
			}
			return strings.TrimPrefix(strings.TrimSpace(rest[:o]), "*")
		}
		return ""
	}
	rest := strings.TrimSpace(strings.TrimPrefix(t, "func"))
	o := strings.Index(rest, "(")
	if o < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:o])
}

func (r *TokenContextRootRule) Check(fd diff.FileDiff) ([]findings.Finding, error) {
	if strings.HasSuffix(fd.NewPath, "_test.go") || !fd.IsGoFile() {
		return nil, nil
	}
	var result []findings.Finding

	// 重建文件的线性视图（context + added），跟踪最近一次函数签名
	type ln struct {
		content string
		no      int
		added   bool
	}
	var view []ln
	for _, h := range fd.Hunks {
		for _, l := range h.Lines {
			if l.Type == diff.LineDeleted {
				continue
			}
			view = append(view, ln{l.Content, l.NewLine, l.Type == diff.LineAdded})
		}
	}

	inCtxFunc := false
	for _, l := range view {
		t := strings.TrimSpace(l.content)
		if strings.HasPrefix(t, "func ") || strings.HasPrefix(t, "func(") {
			inCtxFunc = funcHasContextParam(t)
			continue
		}
		if !l.added || !inCtxFunc || isCommentLine(l.content) {
			continue
		}
		if ctxRootCallRe.MatchString(l.content) {
			f := findings.NewFinding(
				r.Severity(), r.Category(), r.ID(),
				"Token 感知：context 根被替换（函数已接收 ctx 却用 Background/TODO）",
				fd.NewPath, l.no,
				l.content,
				"使用函数传入的 ctx 参数，保持取消传播链路；确需脱离调用方生命周期时加注释说明",
				0.70,
				"token:context_root",
			)
			f.EvidenceChain = findings.BuildEvidenceChain(fd.NewPath, l.no, r.ID(),
				"context.Background/TODO inside func with context.Context param", 0.70)
			result = append(result, *f)
		}
	}
	return result, nil
}

// ---------- CON-AST-001: mutex Lock 后无 Unlock ----------

type TokenMutexRule struct{}

// NewTokenMutexRule 创建 mutex 配对检测规则实例。
func NewTokenMutexRule() *TokenMutexRule { return &TokenMutexRule{} }

func (r *TokenMutexRule) ID() string                  { return "CON-AST-001" }
func (r *TokenMutexRule) Name() string                { return "Token 感知的 mutex 配对检测" }
func (r *TokenMutexRule) Severity() findings.Severity { return findings.SeverityMedium }
func (r *TokenMutexRule) Category() findings.Category { return findings.CategoryConcurrency }

// mutexLockRe 提取 .Lock() 的接收者（排除 RLock——读锁单独配对）。
var mutexLockRe = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_.\[\]\*]*?)\.Lock\(\)`)

func (r *TokenMutexRule) Check(fd diff.FileDiff) ([]findings.Finding, error) {
	var result []findings.Finding

	// 通用文件门控：本规则检查的是 Go 语义，非 Go 文本（Markdown/模板/配置）不适用。
	if !fd.IsGoFile() {
		return nil, nil
	}
	added := collectAddedLines(fd)

	for _, line := range added {
		content := line.Content
		if isCommentLine(content) {
			continue
		}
		m := mutexLockRe.FindStringSubmatch(content)
		if m == nil {
			continue
		}
		receiver := m[1]
		// 同行已 Unlock（少见但合法）不报
		unlockRe := regexp.MustCompile(regexp.QuoteMeta(receiver) + `\.Unlock\(\)`)
		if unlockRe.MatchString(content) {
			continue
		}
		found := false
		for _, l2 := range added {
			if unlockRe.MatchString(l2.Content) {
				found = true
				break
			}
		}
		if found {
			continue
		}
		f := findings.NewFinding(
			r.Severity(), r.Category(), r.ID(),
			"Token 感知：mutex Lock 后未见 Unlock",
			fd.NewPath, line.NewLine,
			content,
			"确保 "+receiver+".Unlock() 在所有路径上执行（惯例 defer "+receiver+".Unlock()）",
			0.80,
			"token:mutex_pair",
		)
		f.EvidenceChain = findings.BuildEvidenceChain(fd.NewPath, line.NewLine, r.ID(),
			"receiver="+receiver+", Lock() without Unlock() in added lines", 0.80)
		result = append(result, *f)
	}
	return result, nil
}

// ---------- RES-AST-002: defer 在循环内 + timer/ticker 未停止 ----------

type TokenLoopTimerRule struct{}

// NewTokenLoopTimerRule 创建循环 defer / timer 检测规则实例。
func NewTokenLoopTimerRule() *TokenLoopTimerRule { return &TokenLoopTimerRule{} }

func (r *TokenLoopTimerRule) ID() string { return "RES-AST-002" }
func (r *TokenLoopTimerRule) Name() string {
	return "Token 感知的循环 defer 与 timer 泄漏检测"
}
func (r *TokenLoopTimerRule) Severity() findings.Severity { return findings.SeverityMedium }
func (r *TokenLoopTimerRule) Category() findings.Category { return findings.CategoryResource }

func (r *TokenLoopTimerRule) Check(fd diff.FileDiff) ([]findings.Finding, error) {
	var result []findings.Finding

	// 通用文件门控：本规则检查的是 Go 语义，非 Go 文本（Markdown/模板/配置）不适用。
	if !fd.IsGoFile() {
		return nil, nil
	}
	added := collectAddedLines(fd)

	// (a) defer 在 for 循环体内：花括号深度跟踪。
	// goroutine 字面量（go func() { … defer x.Done() … }）内的 defer 随
	// goroutine 退出执行，属正确用法——waybackurls 真实误报产出，跳过。
	depth := 0
	loopDepth := -1
	goroutineDepth := -1
	for _, line := range added {
		content := line.Content
		trimmed := strings.TrimSpace(content)
		if !isCommentLine(content) && loopDepth < 0 && strings.HasPrefix(trimmed, "for") && strings.Contains(content, "{") {
			loopDepth = depth // for 行自身的 { 会在下面 +1
		}
		if !isCommentLine(content) && goroutineDepth < 0 &&
			(strings.HasPrefix(trimmed, "go ") || trimmed == "go") && strings.Contains(content, "{") {
			goroutineDepth = depth
		}
		opens := strings.Count(content, "{")
		closes := strings.Count(content, "}")
		if loopDepth >= 0 && depth > loopDepth && strings.Contains(trimmed, "defer") && !isCommentLine(content) {
			if goroutineDepth >= 0 && depth > goroutineDepth {
				// defer 在 go func 字面量体内：随 goroutine 退出执行，正确用法
				depth += opens - closes
				if closes > 0 && depth <= goroutineDepth {
					goroutineDepth = -1
				}
				if depth < 0 {
					depth = 0
				}
				continue
			}
			f := findings.NewFinding(
				r.Severity(), r.Category(), r.ID(),
				"Token 感知：defer 在 for 循环内（函数退出才执行）",
				fd.NewPath, line.NewLine,
				content,
				"循环内的资源释放应在每次迭代显式执行，或把循环体抽成函数让 defer 随调用返回",
				0.75,
				"token:defer_in_loop",
			)
			f.EvidenceChain = findings.BuildEvidenceChain(fd.NewPath, line.NewLine, r.ID(),
				"defer inside for loop body", 0.75)
			result = append(result, *f)
		}
		depth += opens - closes
		if closes > 0 && depth <= loopDepth {
			loopDepth = -1
		}
		if depth < 0 {
			depth = 0
		}
	}

	// (b) time.Tick：无法 Stop，直接提示
	for _, line := range added {
		if isCommentLine(line.Content) {
			continue
		}
		if strings.Contains(line.Content, "time.Tick(") {
			f := findings.NewFinding(
				findings.SeverityLow, r.Category(), r.ID(),
				"Token 感知：time.Tick 无法停止（底层 Ticker 不可回收）",
				fd.NewPath, line.NewLine,
				line.Content,
				"改用 time.NewTicker 并 defer ticker.Stop()；临时场景用 time.After",
				0.70,
				"token:time_tick",
			)
			f.EvidenceChain = findings.BuildEvidenceChain(fd.NewPath, line.NewLine, r.ID(),
				"time.Tick cannot be stopped", 0.70)
			result = append(result, *f)
		}
	}

	// (c) NewTimer/NewTicker 的变量在新增行中无 .Stop()
	stopReFor := func(v string) *regexp.Regexp {
		return regexp.MustCompile(`\b` + regexp.QuoteMeta(v) + `\.Stop\(\)`)
	}
	timerVarRe := regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)\s*(?::=|=)\s*time\.New(?:Timer|Ticker)\(`)
	for _, line := range added {
		if isCommentLine(line.Content) {
			continue
		}
		m := timerVarRe.FindStringSubmatch(line.Content)
		if m == nil {
			continue
		}
		v := m[1]
		if v == "_" {
			continue
		}
		found := false
		for _, l2 := range added {
			if stopReFor(v).MatchString(l2.Content) {
				found = true
				break
			}
		}
		if found {
			continue
		}
		f := findings.NewFinding(
			r.Severity(), r.Category(), r.ID(),
			"Token 感知：Timer/Ticker 从未 Stop（后台资源累积）",
			fd.NewPath, line.NewLine,
			line.Content,
			"不再使用时调用 "+v+".Stop()（惯例 defer "+v+".Stop()）",
			0.75,
			"token:timer_leak",
		)
		f.EvidenceChain = findings.BuildEvidenceChain(fd.NewPath, line.NewLine, r.ID(),
			"var="+v+", NewTimer/NewTicker without Stop() in added lines", 0.75)
		result = append(result, *f)
	}

	return result, nil
}

// ---------- DB-AST-002: rows 迭代后未检查 rows.Err ----------

type TokenRowsErrRule struct{}

// NewTokenRowsErrRule 创建 rows.Err 检测规则实例。
func NewTokenRowsErrRule() *TokenRowsErrRule { return &TokenRowsErrRule{} }

func (r *TokenRowsErrRule) ID() string                  { return "DB-AST-002" }
func (r *TokenRowsErrRule) Name() string                { return "Token 感知的 rows.Err 检测" }
func (r *TokenRowsErrRule) Severity() findings.Severity { return findings.SeverityMedium }
func (r *TokenRowsErrRule) Category() findings.Category { return findings.CategoryLifecycle }

// rowsQueryRe 匹配 Go 惯例命名的 rows 接收：rows, err := ....Query(...)
var rowsQueryRe = regexp.MustCompile(`\brows\s*,\s*err\s*(?::=|=)\s*.*\.Query(?:Context)?\(`)

func (r *TokenRowsErrRule) Check(fd diff.FileDiff) ([]findings.Finding, error) {
	var result []findings.Finding

	// 通用文件门控：本规则检查的是 Go 语义，非 Go 文本（Markdown/模板/配置）不适用。
	if !fd.IsGoFile() {
		return nil, nil
	}
	added := collectAddedLines(fd)

	for _, line := range added {
		content := line.Content
		if isCommentLine(content) {
			continue
		}
		if !rowsQueryRe.MatchString(content) {
			continue
		}
		found := false
		for _, l2 := range added {
			if strings.Contains(l2.Content, "rows.Err()") {
				found = true
				break
			}
		}
		if found {
			continue
		}
		f := findings.NewFinding(
			r.Severity(), r.Category(), r.ID(),
			"Token 感知：rows 迭代后未检查 rows.Err()",
			fd.NewPath, line.NewLine,
			content,
			"迭代结束后检查 rows.Err()——迭代中途的 IO 错误只能靠它捕获，否则部分数据被静默当完整返回",
			0.75,
			"token:rows_err",
		)
		f.EvidenceChain = findings.BuildEvidenceChain(fd.NewPath, line.NewLine, r.ID(),
			"rows from Query without rows.Err() check in added lines", 0.75)
		result = append(result, *f)
	}
	return result, nil
}
