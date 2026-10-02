// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//
// Package rules 提供基于 Token Facts 的代码审查规则。
//
// 所有规则使用 go/scanner 提取的语法感知 token facts，
// 不依赖正则表达式，减少误报，提高可解释性。
package rules

import (
	"go/scanner"
	"go/token"
	"regexp"
	"strings"

	"code-review-agent/analyzer"
	"code-review-agent/diff"
	"code-review-agent/findings"
)

// ========== SEC-AST-001: Token 感知的硬编码密钥检测 ==========

// TokenSecretRule 使用词法分析检测硬编码密钥。
//
// 检测策略：
//  1. 赋值语句中，左侧是敏感标识符，右侧是字符串字面量
//  2. 函数参数中，敏感标识符伴随可疑字符串
//
// 不会误报的情况：
//   - 注释里的 password
//   - password := os.Getenv("X")
//   - password := "your-password-here"（占位符）
type TokenSecretRule struct {
	analyzer *analyzer.TokenAnalyzer
}

// NewTokenSecretRule 创建 Token 感知的密钥检测规则实例。
func NewTokenSecretRule() *TokenSecretRule {
	return &TokenSecretRule{analyzer: analyzer.NewTokenAnalyzer()}
}

func (r *TokenSecretRule) ID() string                  { return "SEC-AST-001" }
func (r *TokenSecretRule) Name() string                { return "Token 感知的密钥检测" }
func (r *TokenSecretRule) Severity() findings.Severity { return findings.SeverityHigh }
func (r *TokenSecretRule) Category() findings.Category { return findings.CategorySecurity }

func (r *TokenSecretRule) Check(fd diff.FileDiff) ([]findings.Finding, error) {
	var result []findings.Finding

	for _, line := range fd.AddedLines() {
		content := line.Content
		if content == "" {
			continue
		}

		trimmed := strings.TrimSpace(content)
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*") {
			continue
		}

		analysis := r.analyzer.AnalyzeLine(content, line.NewLine)

		// 检查 1：赋值语句中，左侧敏感标识符，右侧字符串字面量
		if ident, value, ok := analysis.GetAssignedValue(); ok {
			// R3③：值来自环境变量/密钥管理服务时不是硬编码（豁免在命中判断前）
			if strings.Contains(content, "os.Getenv(") ||
				strings.Contains(content, "os.LookupEnv(") ||
				strings.Contains(content, "secretmanager") {
				continue
			}
			if isSensitiveIdent(ident) && !isLikelyNotSecret(value) {
				f := findings.NewFinding(
					r.Severity(), r.Category(), r.ID(),
					"Token 感知：疑似硬编码密钥",
					fd.NewPath, line.NewLine,
					sanitizeTokenEvidence(content, value),
					"将密钥移至环境变量或密钥管理服务",
					0.90,
					"token:hardcoded_secret",
				)
				f.EvidenceChain = findings.BuildEvidenceChain(fd.NewPath, line.NewLine, r.ID(),
					"sensitive identifier + assignment + string literal", 0.90)
				result = append(result, *f)
				continue
			}
		}

		// 检查 2：非赋值上下文中的敏感信息传递
		if found, name := analysis.HasSensitiveIdentifier(); found {
			if !analysis.HasAssignment() {
				// M2 规则深化：struct tag 行（反引号字面量）不构成敏感信息传递——
				// `json:"password"` 是字段标签，字段名/tag 键不是密钥。
				if strings.Contains(content, "`") {
					continue
				}
				strs := analysis.FindStringLiterals()
				for _, s := range strs {
					if !isLikelyNotSecret(s) {
						f := findings.NewFinding(
							findings.SeverityMedium, r.Category(), r.ID(),
							"Token 感知：疑似敏感信息传递",
							fd.NewPath, line.NewLine,
							sanitizeTokenEvidence(content, strings.Trim(s, "\"'`")),
							"检查 "+name+" 是否包含敏感信息",
							0.70,
							"token:sensitive_param",
						)
						f.EvidenceChain = findings.BuildEvidenceChain(fd.NewPath, line.NewLine, r.ID(),
							"sensitive identifier in non-assignment context", 0.70)
						result = append(result, *f)
						break
					}
				}
			}
		}
	}

	return result, nil
}

// ========== SEC-AST-002: Token 感知的敏感信息泄漏检测 ==========

// TokenLeakRule 使用词法分析检测代码中泄漏的敏感信息。
//
// 检测策略：扫描新增行中的字符串字面量，检查是否包含：
//   - AWS Access Key 格式
//   - GitHub Token 格式
//   - 私钥头
//   - 数据库连接串（含密码）
//   - JWT Token
//   - 高熵字符串（可能是密钥）
//
// 与正则规则的区别：通过 token 分析确认是字符串字面量，
// 不会误匹配注释或变量名中的模式。
type TokenLeakRule struct {
	analyzer *analyzer.TokenAnalyzer
}

// NewTokenLeakRule 创建 Token 感知的敏感信息泄漏检测规则实例。
func NewTokenLeakRule() *TokenLeakRule {
	return &TokenLeakRule{analyzer: analyzer.NewTokenAnalyzer()}
}

func (r *TokenLeakRule) ID() string                  { return "SEC-AST-002" }
func (r *TokenLeakRule) Name() string                { return "Token 感知的敏感信息泄漏检测" }
func (r *TokenLeakRule) Severity() findings.Severity { return findings.SeverityHigh }
func (r *TokenLeakRule) Category() findings.Category { return findings.CategorySensitiveLeak }

func (r *TokenLeakRule) Check(fd diff.FileDiff) ([]findings.Finding, error) {
	var result []findings.Finding

	for _, line := range fd.AddedLines() {
		content := line.Content
		if content == "" {
			continue
		}

		trimmed := strings.TrimSpace(content)
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*") {
			continue
		}

		analysis := r.analyzer.AnalyzeLine(content, line.NewLine)
		strs := analysis.FindStringLiterals()

		for _, s := range strs {
			clean := strings.Trim(s, "\"'`")
			if len(clean) < 10 {
				continue
			}

			if match, title, confidence := detectLeakPattern(clean); match {
				f := findings.NewFinding(
					r.Severity(), r.Category(), r.ID(),
					title,
					fd.NewPath, line.NewLine,
					sanitizeTokenEvidence(content, clean),
					"立即轮换密钥，从代码中移除，使用密钥管理服务",
					confidence,
					"token:sensitive_leak",
				)
				f.EvidenceChain = findings.BuildEvidenceChain(fd.NewPath, line.NewLine, r.ID(),
					"string literal matches known leak pattern", 0.85)
				result = append(result, *f)
				break // 一行只报一次
			}
		}
	}

	return result, nil
}

// detectLeakPattern 检测字符串是否匹配已知的泄漏模式。
func detectLeakPattern(s string) (bool, string, float64) {
	// AWS Access Key
	if len(s) >= 20 && strings.HasPrefix(s, "AKIA") && isUpperAlphanumeric(s[4:]) {
		return true, "Token 感知：AWS Access Key 泄漏", 0.95
	}
	// GitHub Token
	if strings.HasPrefix(s, "ghp_") && len(s) >= 40 {
		return true, "Token 感知：GitHub Token 泄漏", 0.95
	}
	if strings.HasPrefix(s, "gho_") && len(s) >= 40 {
		return true, "Token 感知：GitHub OAuth Token 泄漏", 0.95
	}
	// Stripe Key
	if (strings.HasPrefix(s, "sk_live_") || strings.HasPrefix(s, "pk_live_")) && len(s) >= 20 {
		return true, "Token 感知：Stripe 密钥泄漏", 0.95
	}
	// Slack Token
	if strings.HasPrefix(s, "xox") && len(s) >= 15 {
		return true, "Token 感知：Slack Token 泄漏", 0.90
	}
	// Private Key
	if strings.Contains(s, "BEGIN") && strings.Contains(s, "PRIVATE KEY") {
		return true, "Token 感知：私钥泄漏", 0.99
	}
	// JWT Token
	if strings.HasPrefix(s, "eyJ") && len(s) >= 20 {
		parts := strings.Split(s, ".")
		if len(parts) == 3 {
			return true, "Token 感知：JWT Token 泄漏", 0.85
		}
	}
	// Database connection string with password
	lower := strings.ToLower(s)
	dbPrefixes := []string{"mysql://", "postgres://", "postgresql://", "mongodb://", "mongodb+srv://", "redis://"}
	for _, prefix := range dbPrefixes {
		if strings.HasPrefix(lower, prefix) && strings.Contains(s, "@") && strings.Contains(s, ":") {
			return true, "Token 感知：数据库连接串泄漏（含密码）", 0.95
		}
	}
	// R1：GitHub 其余前缀（gho_ 已在上方覆盖）
	for _, p := range []string{"ghu_", "ghs_", "ghr_"} {
		if strings.HasPrefix(s, p) && len(s) >= 35 {
			return true, "Token 感知：GitHub Token 泄漏", 0.95
		}
	}
	// R1：GitLab Personal Access Token
	if strings.HasPrefix(s, "glpat-") && len(s) >= 25 {
		return true, "Token 感知：GitLab Access Token 泄漏", 0.95
	}
	// R1：Google API Key
	if strings.HasPrefix(s, "AIza") && len(s) >= 35 {
		return true, "Token 感知：Google API Key 泄漏", 0.95
	}
	// R1：npm 访问令牌
	if strings.HasPrefix(s, "npm_") && len(s) >= 35 {
		return true, "Token 感知：npm 访问令牌泄漏", 0.95
	}
	// R1：SendGrid API Key（SG. xxx . yyy 双段）
	if strings.HasPrefix(s, "SG.") && strings.Count(s, ".") >= 2 && len(s) >= 40 {
		return true, "Token 感知：SendGrid API Key 泄漏", 0.95
	}
	// R1：Bearer 头携带长凭据（排除占位符形态）
	if bearerToken := bearerLeakToken(s); bearerToken != "" {
		return true, "Token 感知：Bearer 凭据硬编码", 0.90
	}
	// URL with credentials
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		atIdx := strings.Index(s, "@")
		colonIdx := strings.Index(s, ":")
		if atIdx > 0 && colonIdx > 0 && colonIdx < atIdx {
			// http://user:pass@host
			return true, "Token 感知：URL 中嵌入了凭据", 0.90
		}
	}

	return false, "", 0
}

// isUpperAlphanumeric 检查字符串是否全是大写字母和数字。
func isUpperAlphanumeric(s string) bool {
	for _, c := range s {
		if !((c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			return false
		}
	}
	return true
}

// sanitizeTokenEvidence 脱敏证据中的敏感信息。
func sanitizeTokenEvidence(content, secret string) string {
	if len(secret) <= 8 {
		return content
	}
	masked := secret[:4] + "***REDACTED***"
	return strings.Replace(content, secret, masked, 1)
}

// ========== GOR-AST-001: Token 感知的 goroutine 泄漏检测 ==========

// TokenGoroutineRule 使用词法分析检测 goroutine 泄漏。
type TokenGoroutineRule struct {
	analyzer *analyzer.TokenAnalyzer
}

// NewTokenGoroutineRule 创建 Token 感知的 goroutine 泄漏检测规则实例。
func NewTokenGoroutineRule() *TokenGoroutineRule {
	return &TokenGoroutineRule{analyzer: analyzer.NewTokenAnalyzer()}
}

func (r *TokenGoroutineRule) ID() string                  { return "GOR-AST-001" }
func (r *TokenGoroutineRule) Name() string                { return "Token 感知的 goroutine 泄漏检测" }
func (r *TokenGoroutineRule) Severity() findings.Severity { return findings.SeverityHigh }
func (r *TokenGoroutineRule) Category() findings.Category { return findings.CategoryResource }

func (r *TokenGoroutineRule) Check(fd diff.FileDiff) ([]findings.Finding, error) {
	// W1：go.mod/非 Go 文件不适用（"go 1.21" 会被误判为 goroutine）
	if !fd.IsGoFile() {
		return nil, nil
	}
	var result []findings.Finding

	for _, hunk := range fd.Hunks {
		var allLines []string
		for _, line := range hunk.Lines {
			allLines = append(allLines, line.Content)
		}
		lineAnalyses := r.analyzer.AnalyzeLines(allLines)

		for i, line := range hunk.Lines {
			if line.Type != diff.LineAdded {
				continue
			}
			if i >= len(lineAnalyses) {
				continue
			}

			analysis := lineAnalyses[i]
			if !analysis.HasGoStatement() {
				continue
			}

			if hasExitMechanismToken(lineAnalyses) {
				continue
			}

			if isOneShotGoroutineToken(lineAnalyses, i, line.Content) {
				continue
			}

			f := findings.NewFinding(
				r.Severity(), r.Category(), r.ID(),
				"Token 感知：goroutine 可能泄漏",
				fd.NewPath, line.NewLine,
				line.Content,
				"为 goroutine 添加退出机制：context.WithCancel + select",
				0.85,
				"token:goroutine_leak",
			)
			f.EvidenceChain = findings.BuildEvidenceChain(fd.NewPath, line.NewLine, r.ID(),
				"go statement without exit markers (select/ctx/done)", 0.85)
			result = append(result, *f)
		}
	}

	return result, nil
}

func hasExitMechanismToken(analyses []analyzer.TokenAnalysis) bool {
	exitIdentifiers := map[string]bool{
		"ctx": true, "cancel": true, "done": true,
		"stop": true, "quit": true, "errgroup": true,
	}

	for _, a := range analyses {
		for _, f := range a.Facts {
			if f.Kind == analyzer.FactSelect {
				return true
			}
		}
		for _, id := range a.FindIdentifiers() {
			if exitIdentifiers[strings.ToLower(id)] {
				return true
			}
		}
		if a.HasDefer() {
			return true
		}
	}
	return false
}

// isOneShotGoroutineToken 判断是否为"一次性"goroutine（M2 深化）。
//
// M2 深化：一次性排除只适用于闭包立即执行（go func() { ... }()）——
// 函数体就在 hunk 内，token 层看得到有没有 for 常驻循环；
// 具名函数调用（go worker(ch)）的函数体不在本次变更中，
// token 层无法判定是否常驻，保守上报（不排除）。
func isOneShotGoroutineToken(analyses []analyzer.TokenAnalysis, goIndex int, goLine string) bool {
	if !isClosureGoroutine(goLine) {
		return false
	}
	for i := goIndex + 1; i < len(analyses); i++ {
		for _, f := range analyses[i].Facts {
			if f.Kind == analyzer.FactFor {
				return false
			}
		}
	}
	return true
}

// isClosureGoroutine 判断 go 语句是否为闭包立即执行（go 后紧跟 func 关键字）。
func isClosureGoroutine(goLine string) bool {
	trimmed := strings.TrimSpace(goLine)
	return strings.HasPrefix(strings.TrimPrefix(trimmed, "go "), "func")
}

// ========== RES-AST-001: Token 感知的资源泄漏检测 ==========

// TokenResourceRule 使用词法分析检测资源泄漏。
type TokenResourceRule struct {
	repoTypes *analyzer.RepoTypes // D3：repo 模式类型信息（nil = 非 repo 模式）
	analyzer  *analyzer.TokenAnalyzer
}

// NewTokenResourceRule 创建 Token 感知的资源泄漏检测规则实例。
func NewTokenResourceRule() *TokenResourceRule {
	return &TokenResourceRule{analyzer: analyzer.NewTokenAnalyzer()}
}

func (r *TokenResourceRule) ID() string                  { return "RES-AST-001" }
func (r *TokenResourceRule) Name() string                { return "Token 感知的资源泄漏检测" }
func (r *TokenResourceRule) Severity() findings.Severity { return findings.SeverityMedium }
func (r *TokenResourceRule) Category() findings.Category { return findings.CategoryResource }

var resourceOpenCalls = map[string]string{
	"os.Open(":       ".Close()",
	"os.Create(":     ".Close()",
	"os.OpenFile(":   ".Close()",
	"os.CreateTemp(": ".Close()",
	"http.Get(":      ".Body.Close()",
	"http.Post(":     ".Body.Close()",
	"http.Do(":       ".Body.Close()",
	"sql.Open(":      ".Close()",
	".Conn(":         ".Close()",
	".Query(":        ".Close()",
	".Prepare(":      ".Close()",
	"net.Dial(":      ".Close()",
	"net.Listen(":    ".Close()",
}

// SetRepoTypes 注入 repo 模式类型信息（D3 TypeAware）。
func (r *TokenResourceRule) SetRepoTypes(rt *analyzer.RepoTypes) { r.repoTypes = rt }

func (r *TokenResourceRule) Check(fd diff.FileDiff) ([]findings.Finding, error) {
	var result []findings.Finding

	// M2 规则深化：Close 检查范围从"单个 hunk"扩大到"文件全部 hunk 的行"。
	// 修复跨 hunk 生命周期误报：open 在 hunk1、defer Close 在 hunk2 是合法代码，
	// 以前按 hunk 局部检查会把这类正常代码误报为资源泄漏。
	var fileLines []string
	for _, hunk := range fd.Hunks {
		for _, line := range hunk.Lines {
			fileLines = append(fileLines, line.Content)
		}
	}

	for _, hunk := range fd.Hunks {
		var addedLines []string
		var addedLineNums []int
		for _, line := range hunk.Lines {
			if line.Type == diff.LineAdded && line.Content != "" {
				addedLines = append(addedLines, line.Content)
				addedLineNums = append(addedLineNums, line.NewLine)
			}
		}

		if len(addedLines) == 0 {
			continue
		}

		for i, content := range addedLines {
			for call, closeMethod := range resourceOpenCalls {
				if strings.Contains(content, call) {
					// 构造器语义：句柄被 return 交给调用方时关闭责任已转移，不算本函数泄漏
					handleVar := strings.TrimSuffix(extractVarNameToken(content), ".")
					// R3①：变量名级匹配——`f, err := os.Open(a)` 提取 f，精确查 f.Close()；
					// 替代旧"文件任意行有 Close 就放过"（抓不到同文件多句柄只关一个的泄漏）。
					// 解析不出变量名时退回行文本级（保守降级）。
					closed := false
					if handleVar != "" {
						closed = hasCloseInLines(fileLines, handleVar+closeMethod)
					} else {
						closed = hasCloseInLines(fileLines, closeMethod)
					}
					// D3：类型信息已知时按 io.Closer 判定——首返回值不实现
					// io.Closer 的调用（如 sql.Row）不再依赖手工清单
					if !closed && r.repoTypes != nil && handleVar != "" {
						if impl, known := r.repoTypes.FirstLHSImplementsCloser(fd.NewPath, addedLineNums[i]); known && !impl {
							break
						}
					}
					if !closed &&
						!ownershipTransferredByReturn(fileLines, handleVar) {
						f := findings.NewFinding(
							r.Severity(), r.Category(), r.ID(),
							"Token 感知：资源可能未关闭",
							fd.NewPath, addedLineNums[i],
							content,
							"添加 defer "+extractVarNameToken(content)+closeMethod,
							0.80,
							"token:resource_leak",
						)
						f.EvidenceChain = findings.BuildEvidenceChain(fd.NewPath, addedLineNums[i], r.ID(),
							"open call without matching close in file", 0.80)
						result = append(result, *f)
					}
					break
				}
			}
		}
	}

	return result, nil
}

func hasCloseInLines(lines []string, closeMethod string) bool {
	for _, line := range lines {
		if strings.Contains(line, "defer") && strings.Contains(line, closeMethod) {
			return true
		}
		if strings.Contains(line, closeMethod) && strings.Contains(line, "Close") {
			return true
		}
	}
	return false
}

// ownershipTransferredByReturn 判断资源句柄变量是否被 return 交给调用方。
// 构造器模式（打开资源后直接 return 句柄，如 func open() (*os.File, error)）
// 的关闭责任在调用方，不应按本函数泄漏上报。
// varName 作为方法接收者出现（如 return db.Ping() 里的 db.）不算返回句柄本身，
// 匹配前先剔除 "varName." 前缀的出现，再看剩余部分是否仍有裸 varName。
func ownershipTransferredByReturn(lines []string, varName string) bool {
	if varName == "" {
		return false
	}
	re, err := regexp.Compile(`\breturn\b[^\n]*\b` + regexp.QuoteMeta(varName) + `\b`)
	if err != nil {
		return false
	}
	for _, line := range lines {
		if !strings.Contains(line, "return") {
			continue
		}
		stripped := strings.ReplaceAll(line, varName+".", "")
		if re.MatchString(stripped) {
			return true
		}
	}
	return false
}

func extractVarNameToken(line string) string {
	ta := analyzer.NewTokenAnalyzer()
	analysis := ta.AnalyzeLine(line, 1)
	ids := analysis.FindIdentifiers()
	if len(ids) > 0 {
		return ids[0] + "."
	}
	return ""
}

// ========== ERR-AST-001: Token 感知的错误处理检测 ==========

// TokenErrorRule 使用词法分析检测错误处理问题。
//
// 检测策略（基于 token facts）：
//  1. 用 _ 忽略错误：找到 DEFINE/ASSIGN + IDENT "_"
//  2. panic 使用：找到关键字 panic
//  3. log.Fatal 使用：找到标识符 log + 点 + Fatal
//  4. 错误被吞没：if err != nil 块中只有 return nil
type TokenErrorRule struct {
	repoTypes *analyzer.RepoTypes // D3：repo 模式类型信息（nil = 非 repo 模式）
	analyzer  *analyzer.TokenAnalyzer
}

// NewTokenErrorRule 创建 Token 感知的错误处理检测规则实例。
func NewTokenErrorRule() *TokenErrorRule {
	return &TokenErrorRule{analyzer: analyzer.NewTokenAnalyzer()}
}

func (r *TokenErrorRule) ID() string                  { return "ERR-AST-001" }
func (r *TokenErrorRule) Name() string                { return "Token 感知的错误处理检测" }
func (r *TokenErrorRule) Severity() findings.Severity { return findings.SeverityMedium }
func (r *TokenErrorRule) Category() findings.Category { return findings.CategoryErrorHandling }

// SetRepoTypes 注入 repo 模式类型信息（D3 TypeAware）。
func (r *TokenErrorRule) SetRepoTypes(rt *analyzer.RepoTypes) { r.repoTypes = rt }

func (r *TokenErrorRule) Check(fd diff.FileDiff) ([]findings.Finding, error) {
	var result []findings.Finding

	for _, hunk := range fd.Hunks {
		// 跨行检查：错误被吞没
		if finding := checkSwallowedErrorToken(fd.NewPath, hunk); finding != nil {
			result = append(result, *finding)
		}

		for _, line := range hunk.Lines {
			if line.Type != diff.LineAdded {
				continue
			}
			content := line.Content
			if content == "" {
				continue
			}

			trimmed := strings.TrimSpace(content)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}

			analysis := r.analyzer.AnalyzeLine(content, line.NewLine)

			// 检查 1：用 _ 忽略错误
			// W1 模拟测试延伸（真实仓库误报猎捕）：测试文件的 `_` 丢弃是
			// 惯用法（断言框架处理错误），整文件跳过（对齐 #2348）
			if strings.HasSuffix(fd.NewPath, "_test.go") {
				continue
			}
			// D3：repo 模式下用类型信息判定——每个 _ 位置的返回值类型都已知
			// 且都不是 error 时，本次丢弃与错误无关（三返回值首丢弃等误报根治）
			if r.repoTypes != nil {
				lhs := content
				if i := strings.Index(content, ":="); i >= 0 {
					lhs = content[:i]
				} else if i := strings.Index(content, "="); i >= 0 {
					lhs = content[:i]
				}
				parts := strings.Split(strings.TrimSpace(lhs), ",")
				if hasUnderscoreParts(parts) {
					knownNonError, anyError := 0, 0
					for idx, p := range parts {
						if strings.TrimSpace(p) != "_" {
							continue
						}
						t := r.repoTypes.LHSPositionType(fd.NewPath, line.NewLine, idx)
						switch {
						case t == nil:
							// 未知 → 不下结论，保持词法行为
							knownNonError, anyError = 0, 1
						case analyzer.IsErrorType(t):
							anyError = 1
						default:
							knownNonError++
						}
					}
					if anyError == 0 && knownNonError > 0 {
						continue // 所有 _ 位置类型已知且非 error → 与错误无关
					}
				}
			}
			if isIgnoredErrorToken(analysis, content) {
				f := findings.NewFinding(
					r.Severity(), r.Category(), r.ID(),
					"Token 感知：错误可能被忽略（使用 _ 丢弃）",
					fd.NewPath, line.NewLine,
					content,
					"检查错误返回值并处理：if err != nil { return fmt.Errorf(\"context: %w\", err) }",
					0.80,
					"token:error_ignored",
				)
				f.EvidenceChain = findings.BuildEvidenceChain(fd.NewPath, line.NewLine, r.ID(),
					"error return value discarded via blank identifier", 0.80)
				result = append(result, *f)
				continue
			}

			// 检查 2：panic 使用
			if isPanicUsage(analysis) && !isTestFile(fd.NewPath) {
				// W1 模拟测试延伸：库代码 panic 是设计决策（如 ksuid 对
				// crypto/rand 失败的防御）——降级 warnings 通道减少误报噪音
				f := findings.NewFinding(
					findings.SeverityLow, r.Category(), r.ID(),
					"Token 感知：使用 panic 代替返回 error",
					fd.NewPath, line.NewLine,
					content,
					"库代码应返回 error 而不是 panic",
					0.65,
					"token:panic_usage",
				)
				f.EvidenceChain = findings.BuildEvidenceChain(fd.NewPath, line.NewLine, r.ID(),
					"panic in non-test library code", 0.65)
				result = append(result, *f)
				continue
			}

			// 检查 3：log.Fatal 使用
			if isLogFatalUsage(analysis, content) && !isMainOrTestFile(fd.NewPath) {
				f := findings.NewFinding(
					r.Severity(), r.Category(), r.ID(),
					"Token 感知：库代码使用 log.Fatal（会直接退出进程）",
					fd.NewPath, line.NewLine,
					content,
					"库代码应返回 error 而不是调用 log.Fatal",
					0.80,
					"token:log_fatal",
				)
				f.EvidenceChain = findings.BuildEvidenceChain(fd.NewPath, line.NewLine, r.ID(),
					"log.Fatal in non-main non-test code", 0.80)
				result = append(result, *f)
				continue
			}
		}
	}

	return result, nil
}

// isIgnoredErrorToken 检查是否用 _ 忽略了错误返回值。
func isIgnoredErrorToken(analysis analyzer.TokenAnalysis, content string) bool {
	// 检查是否有 _ 标识符
	hasUnderscore := false
	hasAssignment := false

	for _, f := range analysis.Facts {
		if f.Kind == analyzer.FactIdentifier && f.Value == "_" {
			hasUnderscore = true
		}
		if f.Kind == analyzer.FactAssignment {
			hasAssignment = true
		}
	}

	if !hasUnderscore || !hasAssignment {
		return false
	}

	// R3②延伸（真实代码误报猎捕，2026-09-30）：三类 `_` 惯用法与错误无关
	// a) `var _ I = ...` 编译期接口断言（必须 var 前缀；裸 `_ =` 是真实丢弃不豁免）
	if strings.HasPrefix(strings.TrimSpace(content), "var ") {
		return false
	}
	rhs := ""
	if i := strings.LastIndex(content, "="); i >= 0 {
		rhs = strings.TrimSpace(content[i+1:])
	}
	// d) `_ = someVar`：纯变量赋值（清理/显式忽略标记），右侧无函数调用
	if !strings.Contains(rhs, "(") {
		return false
	}
	// e2) RHS 必须是调用形态（标识符紧跟左括号）——配置正则/模板串
	// （pylintrc 的 `_+$|(_[a-z]…`）不是错误丢弃（真实仓库误报猎捕产出）
	callShaped := regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.\[\]"']*\s*\(`)
	if !callShaped.MatchString(rhs) {
		return false
	}
	// e) errors.As 惯用法：`_, isX := y.AsXxx(err)`，第二变量是断言结果非错误
	if regexp.MustCompile(`\.[A-Z]?As[A-Z]`).MatchString(rhs) {
		return false
	}
	lhs := content
	if i := strings.Index(content, ":="); i >= 0 {
		lhs = content[:i]
	} else if i := strings.Index(content, "="); i >= 0 {
		lhs = content[:i]
	}
	lhs = strings.TrimSpace(lhs)
	parts := strings.Split(lhs, ",")
	// b) comma-ok 惯用法：`_, x := m[k]` 首位丢弃 + 右侧索引表达式 = 存在性检查
	//   （第二变量名任意：ok/enabled/…），不是错误丢弃
	if len(parts) >= 2 && strings.TrimSpace(parts[0]) == "_" &&
		strings.Contains(content[strings.LastIndex(content, "=")+1:], "[") {
		return false
	}
	// 命名 ok 的类型断言/多返回值形态
	for _, p := range parts {
		if strings.TrimSpace(p) == "ok" {
			return false
		}
	}
	// c) error 变量已被接收（`_, err = ...` / `err, _ := ...`）——丢弃的是非 error 位置
	for _, p := range parts {
		if strings.TrimSpace(p) == "err" {
			return false
		}
	}

	// R3 提前项：range 循环的 `_` 是惯用占位（for _, v := range …），与错误无关
	if strings.Contains(content, "range") && strings.Contains(content, "for") {
		return false
	}

	// 排除安全的忽略
	// R3②：错误返回调用的已知安全忽略面白名单（#2318/#2348）
	safeIgnores := []string{
		"fmt.Print", "fmt.Fprint", "io.Copy", "io.WriteString",
		".Write(", ".Close()", "w.Write", "f.Write",
		".Commit()", ".Rollback()", "os.Remove(", "os.RemoveAll(",
		"json.Unmarshal(", "json.NewDecoder(", "json.Marshal(", ".Scan(", ".Encode(",
		".EmitEvent(",
	}
	for _, safe := range safeIgnores {
		if strings.Contains(content, safe) {
			return false
		}
	}

	return true
}

// isPanicUsage 检查是否使用了 panic。
func isPanicUsage(analysis analyzer.TokenAnalysis) bool {
	for _, f := range analysis.Facts {
		if f.Kind == analyzer.FactIdentifier && f.Value == "panic" {
			return true
		}
	}
	return false
}

// isLogFatalUsage 检查是否使用了 log.Fatal。
func isLogFatalUsage(analysis analyzer.TokenAnalysis, content string) bool {
	ids := analysis.FindIdentifiers()
	hasLog := false
	hasFatal := false
	for _, id := range ids {
		if id == "log" {
			hasLog = true
		}
		if strings.HasPrefix(id, "Fatal") {
			hasFatal = true
		}
	}
	return hasLog && hasFatal && strings.Contains(content, ".Fatal")
}

// checkSwallowedErrorToken 跨行检查错误被吞没。
func checkSwallowedErrorToken(filePath string, hunk diff.Hunk) *findings.Finding {
	ta := analyzer.NewTokenAnalyzer()

	for i, line := range hunk.Lines {
		if line.Type != diff.LineAdded {
			continue
		}
		if !strings.Contains(line.Content, "if err != nil") {
			continue
		}

		// 检查接下来几行
		for j := i + 1; j < len(hunk.Lines) && j < i+5; j++ {
			nextLine := hunk.Lines[j]
			trimmed := strings.TrimSpace(nextLine.Content)

			if trimmed == "return nil" || trimmed == "return nil," {
				// 确认是吞没：用 token 分析验证
				nextAnalysis := ta.AnalyzeLine(trimmed, j)
				hasReturn := false
				hasNil := false
				for _, f := range nextAnalysis.Facts {
					if f.Kind == analyzer.FactReturn {
						hasReturn = true
					}
					if f.Kind == analyzer.FactIdentifier && f.Value == "nil" {
						hasNil = true
					}
				}
				if hasReturn && hasNil {
					sw := findings.NewFinding(
						findings.SeverityLow, findings.CategoryErrorHandling, "ERR-AST-001",
						"Token 感知：错误被吞没（返回 nil 而非 err）",
						filePath, line.NewLine,
						line.Content,
						"应返回 err 或使用 fmt.Errorf 添加上下文",
						0.75,
						"token:error_swallowed",
					)
					sw.EvidenceChain = findings.BuildEvidenceChain(filePath, line.NewLine, "ERR-AST-001",
						"if err != nil followed by return nil", 0.75)
					return sw
				}
			}

			if strings.HasPrefix(trimmed, "return") {
				break
			}
		}
	}
	return nil
}

// ========== TST-AST-001: Token 感知的测试缺失检测 ==========

// TokenMissingTestRule 使用词法分析检测新增导出函数是否有测试。
//
// 检测策略：
//  1. 用 go/scanner 找到 func 关键字后的大写标识符（导出函数）
//  2. 在同文件和其他测试文件中查找对应的 Test 函数
//  3. 检查测试函数体是否有效（非空、有调用）
type TokenMissingTestRule struct {
	analyzer *analyzer.TokenAnalyzer
}

// NewTokenMissingTestRule 创建 Token 感知的测试缺失检测规则实例。
func NewTokenMissingTestRule() *TokenMissingTestRule {
	return &TokenMissingTestRule{analyzer: analyzer.NewTokenAnalyzer()}
}

func (r *TokenMissingTestRule) ID() string                  { return "TST-AST-001" }
func (r *TokenMissingTestRule) Name() string                { return "Token 感知的测试缺失检测" }
func (r *TokenMissingTestRule) Severity() findings.Severity { return findings.SeverityLow }
func (r *TokenMissingTestRule) Category() findings.Category { return findings.CategoryTesting }

var noTestNeededFuncs = map[string]bool{
	"main": true, "init": true, "String": true, "Error": true,
	"Unwrap": true, "Is": true, "As": true, "ServeHTTP": true,
	"Close": true, "Setup": true, "Teardown": true,
}

func (r *TokenMissingTestRule) Check(fd diff.FileDiff) ([]findings.Finding, error) {
	if strings.HasSuffix(fd.NewPath, "_test.go") || !fd.IsGoFile() {
		return nil, nil
	}

	var result []findings.Finding

	// 收集同文件所有行（用于检查测试函数是否存在）
	var allContent []string
	for _, hunk := range fd.Hunks {
		for _, line := range hunk.Lines {
			allContent = append(allContent, line.Content)
		}
	}

	for _, hunk := range fd.Hunks {
		for _, line := range hunk.Lines {
			if line.Type != diff.LineAdded {
				continue
			}

			funcName := extractExportedFuncName(line.Content)
			if funcName == "" {
				continue
			}
			if noTestNeededFuncs[funcName] {
				continue
			}

			// 检查是否有对应的测试
			if hasTestFunction(allContent, funcName) {
				continue
			}

			f := findings.NewFinding(
				r.Severity(), r.Category(), r.ID(),
				"Token 感知：新增导出函数缺少测试",
				fd.NewPath, line.NewLine,
				line.Content,
				"添加测试函数 Test"+funcName+" 覆盖该函数的正常和异常路径",
				0.65,
				"token:missing_test",
			)
			f.EvidenceChain = findings.BuildEvidenceChain(fd.NewPath, line.NewLine, r.ID(),
				"exported function added without matching test", 0.65)
			result = append(result, *f)
		}
	}

	return result, nil
}

// CheckFiles 对多个文件执行测试缺失检测。
func (r *TokenMissingTestRule) CheckFiles(files []diff.FileDiff) ([]findings.Finding, error) {
	// 收集所有测试文件内容
	var testContent []string
	for _, fd := range files {
		if strings.HasSuffix(fd.NewPath, "_test.go") {
			for _, hunk := range fd.Hunks {
				for _, line := range hunk.Lines {
					testContent = append(testContent, line.Content)
				}
			}
		}
	}

	var result []findings.Finding
	for _, fd := range files {
		if strings.HasSuffix(fd.NewPath, "_test.go") || !fd.IsGoFile() {
			continue
		}

		var fileContent []string
		for _, hunk := range fd.Hunks {
			for _, line := range hunk.Lines {
				fileContent = append(fileContent, line.Content)
			}
		}

		for _, hunk := range fd.Hunks {
			for _, line := range hunk.Lines {
				if line.Type != diff.LineAdded {
					continue
				}

				funcName := extractExportedFuncName(line.Content)
				if funcName == "" {
					continue
				}
				if noTestNeededFuncs[funcName] {
					continue
				}

				if hasTestFunction(fileContent, funcName) {
					continue
				}
				if hasTestFunction(testContent, funcName) {
					continue
				}

				f := findings.NewFinding(
					r.Severity(), r.Category(), r.ID(),
					"Token 感知：新增导出函数缺少测试",
					fd.NewPath, line.NewLine,
					line.Content,
					"添加测试函数 Test"+funcName+" 覆盖该函数的正常和异常路径",
					0.65,
					"token:missing_test",
				)
				f.EvidenceChain = findings.BuildEvidenceChain(fd.NewPath, line.NewLine, r.ID(),
					"exported function added without matching test", 0.65)
				result = append(result, *f)
			}
		}
	}

	return result, nil
}

// extractExportedFuncName 用 go/scanner 从行中提取导出函数名。
func extractExportedFuncName(line string) string {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "func ") {
		return ""
	}

	// 用 scanner 分析
	src := "package _\n" + line
	var s scanner.Scanner
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))
	s.Init(file, []byte(src), nil, 0)

	var prevTok token.Token
	var prevLit string
	inTargetLine := false

	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		position := fset.Position(pos)
		if position.Line >= 2 {
			inTargetLine = true
		}
		if !inTargetLine {
			prevTok = tok
			prevLit = lit
			continue
		}

		// func 后面的标识符就是函数名
		if prevTok == token.FUNC && tok == token.IDENT {
			// 检查是否是导出函数（首字母大写）
			if len(lit) > 0 && lit[0] >= 'A' && lit[0] <= 'Z' {
				return lit
			}
		}

		prevTok = tok
		prevLit = lit
	}
	_ = prevLit
	return ""
}

// hasTestFunction 检查代码中是否有对指定函数的测试。
func hasTestFunction(content []string, funcName string) bool {
	testFuncName := "Test" + funcName

	for _, line := range content {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") {
			continue
		}

		// 精确匹配：func TestGreet(
		if strings.Contains(line, "func "+testFuncName+"(") {
			// 检查函数体是否有效（非空、有调用）
			if hasEffectiveTestBody(content, funcName) {
				return true
			}
		}
		// 前缀匹配：func TestGreet_
		if strings.Contains(line, "func "+testFuncName+"_") {
			return true
		}
		// 调用匹配：Greet( 在测试函数体内
		if containsFunctionCallToken(line, funcName) {
			return true
		}
	}
	return false
}

// hasEffectiveTestBody 检查测试函数体是否有效。
func hasEffectiveTestBody(content []string, targetFuncName string) bool {
	hasExecutableCode := false
	allSkip := true

	for _, line := range content {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*") {
			continue
		}
		if trimmed == "}" || trimmed == "{" || trimmed == "} {" || trimmed == "}{" {
			continue
		}

		hasExecutableCode = true
		if !strings.Contains(trimmed, "t.Skip") {
			allSkip = false
		}
	}

	if !hasExecutableCode || allSkip {
		return false
	}

	// 检查是否调用了目标函数
	for _, line := range content {
		if containsFunctionCallToken(line, targetFuncName) {
			return true
		}
	}

	return false
}

// containsFunctionCallToken 检查一行是否包含函数调用。
func containsFunctionCallToken(line, funcName string) bool {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "func ") || strings.HasPrefix(trimmed, "//") {
		return false
	}

	idx := 0
	for {
		pos := strings.Index(line[idx:], funcName)
		if pos < 0 {
			return false
		}
		afterName := idx + pos + len(funcName)
		if afterName < len(line) && line[afterName] == '(' {
			return true
		}
		idx = afterName
	}
}

// ========== 辅助函数 ==========

func isSensitiveIdent(ident string) bool {
	lower := strings.ToLower(ident)
	sensitive := []string{
		"password", "passwd", "pwd",
		"secret", "secretkey", "secret_key",
		"apikey", "api_key", "api-key",
		"token", "accesstoken", "access_token",
		"private_key", "privatekey",
		"credential", "dsn",
		"signing_key", "encryption_key", "master_key",
	}
	for _, s := range sensitive {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}

func isLikelyNotSecret(value string) bool {
	value = strings.Trim(value, "\"'`")
	// R3②延伸：含格式动词的是格式串模板（"password=%s"），不是密钥本身
	if strings.Contains(value, "%s") || strings.Contains(value, "%d") ||
		strings.Contains(value, "%v") || strings.Contains(value, "%q") {
		return true
	}
	if len(value) < 8 {
		return true
	}
	if strings.TrimSpace(value) == "" {
		return true
	}
	lower := strings.ToLower(value)
	placeholders := []string{
		"your-", "your_", "replace", "changeme", "example",
		"placeholder", "todo", "fixme", "xxx", "dummy",
		"test", "mock", "fake", "sample", "default",
	}
	for _, p := range placeholders {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

func isTestFile(path string) bool {
	return strings.HasSuffix(path, "_test.go")
}

func isMainOrTestFile(path string) bool {
	return isTestFile(path) || path == "main.go" || strings.HasSuffix(path, "/main.go") || strings.Contains(path, "cmd/")
}

// bearerLeakToken 提取字面量中 "Bearer <长凭据>" 的凭据部分；
// 占位符形态（<your-…>、含 your/xxx/example/changeme）返回空。
func bearerLeakToken(s string) string {
	idx := strings.Index(s, "Bearer")
	if idx < 0 {
		return ""
	}
	rest := strings.TrimSpace(s[idx+len("Bearer"):])
	rest = strings.Trim(rest, "\"'") // 可能紧贴引号
	runes := []rune(rest)
	n := 0
	for _, c := range runes {
		if c == '.' || c == '_' || c == '-' || c == '/' || c == '+' ||
			(c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			n++
		} else {
			break
		}
	}
	if n < 25 {
		return ""
	}
	token := rest[:n]
	lower := strings.ToLower(token)
	for _, ph := range []string{"your", "xxx", "example", "changeme", "placeholder"} {
		if strings.Contains(lower, ph) {
			return ""
		}
	}
	return token
}

// hasUnderscoreParts 判断赋值左侧是否存在 _ 占位。
func hasUnderscoreParts(parts []string) bool {
	for _, p := range parts {
		if strings.TrimSpace(p) == "_" {
			return true
		}
	}
	return false
}
