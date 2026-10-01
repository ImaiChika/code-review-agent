// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
package rules

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"code-review-agent/diff"
	"code-review-agent/findings"
)

// ========== W1：全语言兜底检测（非 Go 文本文件不静默跳过） ==========
//
// 实测探底（dataset/cases/positive/w1_* 标注先行）：
//   - .env / YAML 无引号 KEY=value 凭据：现有 Go 词法规则完全漏检
//     （go/scanner 提取不到无引号字面量）
//   - 单文件大段删除：静默无提示
//   - JS/Python 带引号密钥：现有 SEC-AST-001/002 已容错覆盖，无需重复
//
// 两条规则都只作用于非 Go 文本文件（Go 由既有 15 条规则负责），避免双报。

// DetectLanguage 按扩展名探测语言（W1：非 Go 文件不静默跳过的落地呈现）。
func DetectLanguage(path string) string {
	base := strings.ToLower(filepath.Base(path))
	ext := strings.ToLower(filepath.Ext(path))
	if strings.HasPrefix(base, ".env") {
		return "dotenv"
	}
	switch ext {
	case ".go":
		return "go"
	case ".js", ".mjs", ".cjs", ".jsx":
		return "javascript"
	case ".ts", ".tsx":
		return "typescript"
	case ".py":
		return "python"
	case ".rb":
		return "ruby"
	case ".php":
		return "php"
	case ".java":
		return "java"
	case ".kt":
		return "kotlin"
	case ".yaml", ".yml":
		return "yaml"
	case ".json":
		return "json"
	case ".toml":
		return "toml"
	case ".ini", ".cfg", ".conf", ".properties":
		return "ini"
	case ".sql":
		return "sql"
	case ".md", ".markdown":
		return "markdown"
	case ".sh", ".bash", ".zsh":
		return "shell"
	case ".xml", ".html":
		return "xml"
	case ".css":
		return "css"
	case "":
		return "text"
	default:
		return strings.TrimPrefix(ext, ".")
	}
}

// ---------- SEC-GEN-001: 无引号敏感凭据（.env / YAML / properties 等） ----------

type GeneralSecretRule struct{}

// NewGeneralSecretRule 创建无引号敏感凭据检测规则实例（W1 全语言兜底）。
func NewGeneralSecretRule() *GeneralSecretRule { return &GeneralSecretRule{} }

func (r *GeneralSecretRule) ID() string                  { return "SEC-GEN-001" }
func (r *GeneralSecretRule) Name() string                { return "通用规则：无引号敏感凭据检测" }
func (r *GeneralSecretRule) Severity() findings.Severity { return findings.SeverityHigh }
func (r *GeneralSecretRule) Category() findings.Category { return findings.CategorySensitiveLeak }

// unquotedCredRe 无引号凭据赋值：key 含敏感词（以敏感词为核心，_name/_id 后缀豁免在值侧处理），
// 值为非空非引号内容。冒号（YAML）与等号（.env/properties）两种形态。
var unquotedCredRe = regexp.MustCompile(
	`(?i)^\s*([A-Za-z0-9_.-]*(?:password|passwd|secret|token|api[_-]?key|access[_-]?key|credential)[A-Za-z0-9_.-]*)\s*[:=]\s*(\S.*)$`)

// genUrlCredRe 值侧 URL 内嵌凭据：scheme://user:pass@（密码段非空）。
var genUrlCredRe = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.~-]*://[^/\s:@]+:[^/\s@]+@`)

// genPlaceholderRe 占位/引用形态豁免（值侧）。
var genPlaceholderRe = regexp.MustCompile(`^\$\{|^<|^%%|^\{\{`)

func (r *GeneralSecretRule) Check(fd diff.FileDiff) ([]findings.Finding, error) {
	var result []findings.Finding
	// Go 文件由既有 15 条规则负责（避免双报）
	if fd.IsGoFile() {
		return result, nil
	}
	lang := DetectLanguage(fd.NewPath)

	for _, line := range collectAddedLines(fd) {
		content := line.Content
		if isCommentLine(content) || strings.HasPrefix(strings.TrimSpace(content), "#") {
			continue
		}
		// 分支一：key 含敏感词的无引号赋值
		m := unquotedCredRe.FindStringSubmatch(content)
		if m == nil {
			// 分支二：值侧 URL 内嵌凭据（scheme://user:pass@）——key 名无关
			// （DATABASE_URL=postgres://admin:pass@host 这类 key 不含敏感词的形态）
			if !genUrlCredRe.MatchString(content) {
				continue
			}
			f := findings.NewFinding(
				r.Severity(), r.Category(), r.ID(),
				"通用规则：URL 内嵌凭据（语言: "+lang+"）",
				fd.NewPath, line.NewLine,
				content,
				"连接串凭据移入密钥管理服务或环境变量注入，不要明文提交",
				0.85,
				"token:gen_url_cred",
			)
			f.EvidenceChain = findings.BuildEvidenceChain(fd.NewPath, line.NewLine, r.ID(),
				"URL with embedded credentials (lang="+lang+")", 0.85)
			result = append(result, *f)
			continue
		}
		key, value := strings.TrimSpace(m[1]), strings.TrimSpace(m[2])
		if !genValueLooksReal(key, value) {
			continue
		}
		f := findings.NewFinding(
			r.Severity(), r.Category(), r.ID(),
			"通用规则：无引号敏感凭据（语言: "+lang+"）",
			fd.NewPath, line.NewLine,
			content,
			"配置文件中的凭据移入密钥管理服务或环境变量注入，不要明文提交",
			0.80,
			"token:gen_unquoted_secret",
		)
		f.EvidenceChain = findings.BuildEvidenceChain(fd.NewPath, line.NewLine, r.ID(),
			"key="+key+" with unquoted value (lang="+lang+")", 0.80)
		result = append(result, *f)
	}
	return result, nil
}

// genValueLooksReal 值侧豁免：占位符/引用/过短/名字后缀/注释性内容不是凭据。
func genValueLooksReal(key, value string) bool {
	if len(value) < 6 {
		return false
	}
	if genPlaceholderRe.MatchString(value) {
		return false
	}
	lv := strings.ToLower(value)
	for _, ph := range []string{"your", "changeme", "change_me", "example", "placeholder", "xxx", "****", "todo"} {
		if strings.Contains(lv, ph) {
			return false
		}
	}
	// 引号包裹的值交给 SEC-AST-001/002 词法路径（避免双报）
	if (strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`)) ||
		(strings.HasPrefix(value, "'") && strings.HasSuffix(value, "'")) {
		return false
	}
	// 键名后缀语义：_name/_id/_ttl/_count/_path 等描述性字段不是凭据本身
	lk := strings.ToLower(key)
	for _, suffix := range []string{"_name", "-name", "_id", "-id", "_ttl", "_count", "_path", "_type", "_url", "-url", "_enabled"} {
		if strings.HasSuffix(lk, suffix) {
			return false
		}
	}
	return true
}

// ---------- DEL-GEN-001: 大段删除确认 ----------

// largeDeleteThreshold 单文件删除行数阈值（含该值）。
const largeDeleteThreshold = 30

type GeneralLargeDeleteRule struct{}

// NewGeneralLargeDeleteRule 创建大段删除确认规则实例（W1 全语言兜底）。
func NewGeneralLargeDeleteRule() *GeneralLargeDeleteRule { return &GeneralLargeDeleteRule{} }

func (r *GeneralLargeDeleteRule) ID() string                  { return "DEL-GEN-001" }
func (r *GeneralLargeDeleteRule) Name() string                { return "通用规则：大段删除确认" }
func (r *GeneralLargeDeleteRule) Severity() findings.Severity { return findings.SeverityLow }
func (r *GeneralLargeDeleteRule) Category() findings.Category { return findings.CategoryQuality }

func (r *GeneralLargeDeleteRule) Check(fd diff.FileDiff) ([]findings.Finding, error) {
	var result []findings.Finding
	deleted := 0
	firstLine := 0
	for _, hunk := range fd.Hunks {
		for _, line := range hunk.Lines {
			if line.Type == diff.LineDeleted && strings.TrimSpace(line.Content) != "" {
				if firstLine == 0 {
					firstLine = line.OldLine
				}
				deleted++
			}
		}
	}
	if deleted < largeDeleteThreshold {
		return result, nil
	}
	// 定位到本次变更里第一个新增/上下文行做展示锚点（删除块本身没有新行号）
	anchorLine := firstLine
	for _, hunk := range fd.Hunks {
		for _, line := range hunk.Lines {
			if line.Type == diff.LineAdded {
				anchorLine = line.NewLine
				break
			}
		}
	}
	f := findings.NewFinding(
		r.Severity(), r.Category(), r.ID(),
		"通用规则：大段删除（"+DetectLanguage(fd.NewPath)+"，"+fmt.Sprintf("%d", deleted)+" 行）",
		fd.NewPath, anchorLine,
		"（删除块过大，不展示原文）",
		"大段删除请确认有意为之：是否为重构残留、误删，或需要同步更新文档与调用方",
		0.60,
		"token:large_delete",
	)
	f.EvidenceChain = findings.BuildEvidenceChain(fd.NewPath, anchorLine, r.ID(),
		"deleted lines >= threshold in single file", 0.60)
	result = append(result, *f)
	return result, nil
}
