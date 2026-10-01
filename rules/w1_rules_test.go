// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
package rules

import (
	"strings"
	"testing"

	"code-review-agent/diff"
	"code-review-agent/findings"
)

func runW1Rule(t *testing.T, rule Rule, diffContent string) []findings.Finding {
	t.Helper()
	files, err := diff.ReadFromContent(diffContent)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	var all []findings.Finding
	for _, f := range files {
		out, _ := rule.Check(f)
		all = append(all, out...)
	}
	return all
}

func TestDetectLanguage(t *testing.T) {
	cases := map[string]string{
		"main.go":         "go",
		"app.js":          "javascript",
		"server.ts":       "typescript",
		"main.py":         "python",
		".env":            "dotenv",
		".env.production": "dotenv",
		"config.yaml":     "yaml",
		"app.yml":         "yaml",
		"settings.ini":    "ini",
		"README.md":       "markdown",
		"deploy.sh":       "shell",
		"artifact.bin":    "bin",
	}
	for path, want := range cases {
		if got := DetectLanguage(path); got != want {
			t.Errorf("DetectLanguage(%q) = %q, 期望 %q", path, got, want)
		}
	}
}

func TestGeneralSecret_EnvUnquoted(t *testing.T) {
	fs := runW1Rule(t, NewGeneralSecretRule(), `--- a/.env.production
+++ b/.env.production
@@ -1,2 +1,4 @@
 APP_PORT=8080
+DATABASE_URL=postgres://admin:secretpass@db.internal:5432/prod
+SMTP_PASSWORD=mailpass-2024-prod
+SESSION_SECRET=zoom-session-secret-9900`)
	if len(fs) != 3 {
		t.Fatalf("应命中 3 条, got %+v", fs)
	}
	for _, f := range fs {
		if f.Category != findings.CategorySensitiveLeak {
			t.Errorf("category 应为 sensitive_leak: %v", f.Category)
		}
		if !strings.Contains(f.Title, "dotenv") {
			t.Errorf("标题应标注语言 dotenv: %q", f.Title)
		}
	}
}

func TestGeneralSecret_YamlQuotedNotDoubleReported(t *testing.T) {
	// 带引号的值交由词法路径（SEC-AST-002），GEN 不双报
	fs := runW1Rule(t, NewGeneralSecretRule(), `--- a/c.yaml
+++ b/c.yaml
@@ -1,2 +1,4 @@
 name: demo
+token_plain: sk-live-abcdef1234567890
+token_quoted: "ghp_0123456789abcdefghijklmnopqrstuvwxyz"
+`)
	for _, f := range fs {
		if strings.Contains(f.Evidence, "quoted") {
			t.Errorf("带引号值不应由 GEN 规则报（避免双报）: %+v", f)
		}
	}
	if len(fs) != 1 {
		t.Fatalf("仅无引号形态应报 1 条, got %+v", fs)
	}
}

func TestGeneralSecret_PlaceholdersExempt(t *testing.T) {
	fs := runW1Rule(t, NewGeneralSecretRule(), `--- a/.env.example
+++ b/.env.example
@@ -1,2 +1,6 @@
 APP_PORT=8080
+DATABASE_URL=${DATABASE_URL}
+SMTP_PASSWORD=<your-password-here>
+SESSION_SECRET=changeme
+API_KEY=abc
+FALLBACK_DB=postgres://db.internal:5432/prod
+`)
	if len(fs) != 0 {
		t.Errorf("占位符/引用/过短值/无凭据 URL 不应上报: %+v", fs)
	}
}

func TestGeneralSecret_NameSuffixExempt(t *testing.T) {
	fs := runW1Rule(t, NewGeneralSecretRule(), `--- a/app.yaml
+++ b/app.yaml
@@ -1,2 +1,5 @@
 name: demo
+replica_count: 3
+token_ttl_seconds: 3600
+secret_name: my-arn-reference
+`)
	if len(fs) != 0 {
		t.Errorf("名字/元数据后缀键不应上报: %+v", fs)
	}
}

func TestLargeDelete_OverThreshold(t *testing.T) {
	lines := []string{"--- a/legacy.js", "+++ b/legacy.js", "@@ -1,36 +1,2 @@", " const config = 1"}
	for i := 1; i <= 35; i++ {
		lines = append(lines, "-legacy line to remove")
	}
	lines = append(lines, "+// removed")
	fs := runW1Rule(t, NewGeneralLargeDeleteRule(), strings.Join(lines, "\n")+"\n")
	if len(fs) != 1 {
		t.Fatalf("35 行删除应报 1 条, got %+v", fs)
	}
	if fs[0].Severity != findings.SeverityLow || fs[0].Category != findings.CategoryQuality {
		t.Errorf("大段删除应为 low/quality: %v %v", fs[0].Severity, fs[0].Category)
	}
	// low + 0.6 → warnings 通道
	if fs[0].Confidence >= 0.7 {
		t.Errorf("置信度应低于 0.7（走 warnings）, got %v", fs[0].Confidence)
	}
}

func TestLargeDelete_UnderThresholdNotReported(t *testing.T) {
	lines := []string{"--- a/u.js", "+++ b/u.js", "@@ -1,8 +1,4 @@", " const a = 1"}
	for i := 1; i <= 5; i++ {
		lines = append(lines, "-old line")
	}
	lines = append(lines, " const b = 2")
	fs := runW1Rule(t, NewGeneralLargeDeleteRule(), strings.Join(lines, "\n")+"\n")
	if len(fs) != 0 {
		t.Errorf("小删除不应上报: %+v", fs)
	}
}
