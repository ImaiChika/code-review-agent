// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
package rules

import (
	"testing"

	"code-review-agent/diff"
	"code-review-agent/findings"
	"code-review-agent/safety"
)

// ========== R1 规则单测三件套：正例命中、陷阱反例放行、evidence_chain 填充 ==========

// runR1Rule 用指定 diff 内容跑单条规则（走真实 diff 解析）。
func runR1Rule(t *testing.T, rule Rule, diffContent string) []findings.Finding {
	t.Helper()
	files, err := diff.ReadFromContent(diffContent)
	if err != nil {
		t.Fatalf("解析 diff 失败: %v", err)
	}
	var all []findings.Finding
	for _, f := range files {
		out, err := rule.Check(f)
		if err != nil {
			t.Fatalf("Check 失败: %v", err)
		}
		all = append(all, out...)
	}
	return all
}

// ───── CTX-AST-001 ─────

func TestContextCancel_NamedUncalled(t *testing.T) {
	fs := runR1Rule(t, NewTokenContextCancelRule(), `--- a/fetch.go
+++ b/fetch.go
@@ -1,2 +1,6 @@
 package fetch

+func load() error {
+	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
+	return doFetch(ctx)
+}`)
	if len(fs) != 1 || fs[0].Line != 4 {
		t.Fatalf("应命中 fetch.go:4, got %+v", fs)
	}
	if fs[0].Severity != findings.SeverityMedium || fs[0].Confidence > 0.8 {
		t.Errorf("命名变体应为 medium/0.75: %v %v", fs[0].Severity, fs[0].Confidence)
	}
	if len(fs[0].EvidenceChain) == 0 {
		t.Error("evidence_chain 应非空")
	}
}

func TestContextCancel_DiscardHighSeverity(t *testing.T) {
	// `_` 丢弃变体：确定性泄漏，high/0.85；该形态暂不入数据集（ERR-AST-001 噪音共报），单测锁死
	fs := runR1Rule(t, NewTokenContextCancelRule(), `--- a/w.go
+++ b/w.go
@@ -1,2 +1,5 @@
 package w

+func run() {
+	ctx, _ := context.WithCancel(context.Background())
+}
+`)
	if len(fs) != 1 || fs[0].Line != 4 {
		t.Fatalf("应命中 w.go:4, got %+v", fs)
	}
	if fs[0].Severity != findings.SeverityHigh {
		t.Errorf("丢弃变体应为 high, got %v", fs[0].Severity)
	}
}

func TestContextCancel_DeferredNotReported(t *testing.T) {
	fs := runR1Rule(t, NewTokenContextCancelRule(), `--- a/fetch.go
+++ b/fetch.go
@@ -1,2 +1,7 @@
 package fetch

+func load() error {
+	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
+	defer cancel()
+	return doFetch(ctx)
+}`)
	if len(fs) != 0 {
		t.Errorf("defer cancel() 是规范写法，不应上报: %+v", fs)
	}
}

func TestContextCancel_CalledLaterLineNotReported(t *testing.T) {
	fs := runR1Rule(t, NewTokenContextCancelRule(), `--- a/fetch.go
+++ b/fetch.go
@@ -1,2 +1,8 @@
 package fetch

+func load() error {
+	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
+	defer func() {
+		cancel()
+	}()
+	return doFetch(ctx)
+}`)
	if len(fs) != 0 {
		t.Errorf("闭包内 cancel() 应视为已处理: %+v", fs)
	}
}

// ───── SEC-AST-003 ─────

func TestSQLInjection_Concat(t *testing.T) {
	fs := runR1Rule(t, NewTokenSQLInjectionRule(), `--- a/repo.go
+++ b/repo.go
@@ -1,2 +1,6 @@
 package repo

+func find(name string) (*userRow, error) {
+	query := "SELECT id, name FROM users WHERE name = '" + name + "'"
+	return queryOne(query)
+}`)
	if len(fs) != 1 || fs[0].Line != 4 {
		t.Fatalf("应命中 repo.go:4, got %+v", fs)
	}
	if fs[0].Category != findings.CategorySecurity {
		t.Errorf("category 应为 security, got %v", fs[0].Category)
	}
	if len(fs[0].EvidenceChain) == 0 {
		t.Error("evidence_chain 应非空")
	}
}

func TestSQLInjection_Sprintf(t *testing.T) {
	fs := runR1Rule(t, NewTokenSQLInjectionRule(), `--- a/repo.go
+++ b/repo.go
@@ -1,2 +1,6 @@
 package repo

+func rename(id int, name string) error {
+	q := fmt.Sprintf("UPDATE users SET name = '%s' WHERE id = %d", name, id)
+	return execSQL(q)
+}`)
	if len(fs) != 1 || fs[0].Line != 4 {
		t.Fatalf("应命中 repo.go:4, got %+v", fs)
	}
}

func TestSQLInjection_ParameterizedNotReported(t *testing.T) {
	fs := runR1Rule(t, NewTokenSQLInjectionRule(), `--- a/repo.go
+++ b/repo.go
@@ -1,2 +1,6 @@
 package repo

+func find(id int) (*userRow, error) {
+	row := db.QueryRow("SELECT * FROM users WHERE id = ?", id)
+	return scan(row)
+}`)
	if len(fs) != 0 {
		t.Errorf("参数化查询不应上报: %+v", fs)
	}
}

func TestSQLInjection_ConstConcatNotReported(t *testing.T) {
	fs := runR1Rule(t, NewTokenSQLInjectionRule(), `--- a/repo.go
+++ b/repo.go
@@ -1,2 +1,7 @@
 package repo

+const baseSQL = "SELECT id, name FROM users"
+
+func list() (*userRow, error) {
+	return queryOne(baseSQL + " ORDER BY id")
+}`)
	if len(fs) != 0 {
		t.Errorf("纯常量拼接不应上报: %+v", fs)
	}
}

// ───── SEC-AST-004 ─────

func TestCommandInjection_ShellDynamicPayload(t *testing.T) {
	fs := runR1Rule(t, NewTokenCommandInjectionRule(), `--- a/tool.go
+++ b/tool.go
@@ -1,2 +1,6 @@
 package tool

+func runCmd(input string) ([]byte, error) {
+	out, err := exec.Command("sh", "-c", input).Output()
+	return out, err
+}`)
	if len(fs) != 1 || fs[0].Line != 4 {
		t.Fatalf("应命中 tool.go:4, got %+v", fs)
	}
	if len(fs[0].EvidenceChain) == 0 {
		t.Error("evidence_chain 应非空")
	}
}

func TestCommandInjection_ContextShellPayload(t *testing.T) {
	fs := runR1Rule(t, NewTokenCommandInjectionRule(), `--- a/tool.go
+++ b/tool.go
@@ -1,2 +1,6 @@
 package tool

+func runCtx(ctx context.Context, script string) ([]byte, error) {
+	out, err := exec.CommandContext(ctx, "bash", "-c", script).Output()
+	return out, err
+}`)
	if len(fs) != 1 || fs[0].Line != 4 {
		t.Fatalf("应命中 tool.go:4, got %+v", fs)
	}
}

func TestCommandInjection_DynamicBinary(t *testing.T) {
	fs := runR1Rule(t, NewTokenCommandInjectionRule(), `--- a/tool.go
+++ b/tool.go
@@ -1,2 +1,6 @@
 package tool

+func runBin(bin string) ([]byte, error) {
+	out, err := exec.Command(bin, "--help").Output()
+	return out, err
+}`)
	if len(fs) != 1 || fs[0].Line != 4 {
		t.Fatalf("可执行文件来自变量应命中 tool.go:4, got %+v", fs)
	}
}

func TestCommandInjection_AllLiteralsNotReported(t *testing.T) {
	fs := runR1Rule(t, NewTokenCommandInjectionRule(), `--- a/tool.go
+++ b/tool.go
@@ -1,2 +1,6 @@
 package tool

+func greet() ([]byte, error) {
+	out, err := exec.Command("sh", "-c", "echo hello world").Output()
+	return out, err
+}`)
	if len(fs) != 0 {
		t.Errorf("全字面量不应上报: %+v", fs)
	}
}

func TestCommandInjection_NonShellBinaryNotReported(t *testing.T) {
	// testdata/sandbox_failure.diff 同款：非 shell 字面量 + 字面量 flag
	fs := runR1Rule(t, NewTokenCommandInjectionRule(), `--- a/t.go
+++ b/t.go
@@ -1,2 +1,5 @@
 package t

+func main() {
+	cmd := exec.Command("nonexistent-tool", "--flag")
+}`)
	if len(fs) != 0 {
		t.Errorf("非 shell 字面量可执行文件不应上报: %+v", fs)
	}
}

// ───── SEC-AST-002 前缀扩充 ─────

func TestLeakPattern_NewPrefixes(t *testing.T) {
	cases := []struct {
		literal string
		want    bool
	}{
		{"glpat-AbCdEfGhIjKlMnOpQrStUv", true},
		{"AIzaSyA1234567890abcdefghijklmnopqrstuvwxy", true},
		{"npm_0123456789012345678901234567890abcd", true},
		{"Authorization: Bearer AbCdEfGhIjKlMnOpQrStUvWxYz", true},
		{"ghs_0123456789abcdefghijklmnopqrstuvwxyz", true},
		{"ghr_0123456789abcdefghijklmnopqrstuvwxyz", true},
		{"SG.abcdefghijklmnopqrstuvwxyZ1234567.abcdefghijklmnopqrstuvwxyz123456", true},
		{"ghp_xxxx", false},                                      // 过短占位
		{"Bearer abc", false},                                    // 过短
		{"Bearer <your-token-here>", false},                      // 占位符
		{"use tokens like ghp_xxxx or Bearer xxx in env", false}, // 文档句
		{"https://example.com/AIzaDocsPage", false},              // AIza 后不足长度
	}
	for _, c := range cases {
		got, _, _ := detectLeakPattern(c.literal)
		if got != c.want {
			t.Errorf("detectLeakPattern(%q) = %v, 期望 %v", c.literal, got, c.want)
		}
	}
}

func TestMask_NewPrefixesRedacted(t *testing.T) {
	// 脱敏表与检测表必须同步（脱敏硬门禁的数据面）
	cases := []string{
		"glpat-AbCdEfGhIjKlMnOpQrStUv",
		"AIzaSyA1234567890abcdefghijklmnopqrstuvwxy",
		"npm_0123456789012345678901234567890abcd",
		"Authorization: Bearer AbCdEfGhIjKlMnOpQrStUvWxYz",
		"ghs_0123456789abcdefghijklmnopqrstuvwxyz",
		"ghr_0123456789abcdefghijklmnopqrstuvwxyz",
	}
	for _, c := range cases {
		if got := safety.MaskForTest(c); got == c {
			t.Errorf("%q 未被脱敏", c)
		}
	}
}
