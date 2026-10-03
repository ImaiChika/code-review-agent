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

// TestDetectCodeRole 代码角色判定（多语言）。
func TestDetectCodeRole(t *testing.T) {
	cases := map[string]CodeRole{
		"main.go":                         RoleProduction,
		"pkg/server/serve.go":             RoleProduction,
		"src/requests/auth.py":            RoleProduction,
		"server_test.go":                  RoleTest,
		"pkg/handler/handler_test.go":     RoleTest,
		"internal/fx/testdata/fixture.go": RoleTest,
		"tests/test_utils.py":             RoleTest, // tests/ 目录（Python 惯例）
		"test_requests.py":                RoleTest, // test_*.py
		"pkg/foo_test.py":                 RoleTest, // *_test.py
		"src/app.test.js":                 RoleTest, // JS 测试
		"HISTORY.md":                      RoleExample,
		"docs/user/advanced.rst":          RoleExample,
		"README.md":                       RoleExample,
		"examples/colorprofile/main.go":   RoleExample,
		"example_test.go":                 RoleTest, // _test.go 优先于 example 字样
	}
	for path, want := range cases {
		if got := DetectCodeRole(path); got != want {
			t.Errorf("DetectCodeRole(%q) = %q, want %q", path, got, want)
		}
	}
}

// TestEngineRoleDampening 引擎层角色降噪：测试文件里的 InsecureSkipVerify
// 被压置信度进 warnings 通道（cap 0.65），生产文件保持原置信度。
func TestEngineRoleDampening(t *testing.T) {
	mkDiff := func(path string) diff.FileDiff {
		return diff.FileDiff{
			NewPath: path,
			Hunks: []diff.Hunk{
				{
					Lines: []diff.Line{
						{Type: diff.LineAdded, NewLine: 3, Content: `InsecureSkipVerify: true,`},
					},
				},
			},
		}
	}
	engine := NewEngine()
	engine.Register(NewTokenInsecureTLSRule())

	// 生产文件：保持 0.85 → findings
	out, err := engine.Run([]diff.FileDiff{mkDiff("client.go")})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Confidence != 0.85 {
		t.Fatalf("生产文件应保持原置信度: %+v", out)
	}

	// 测试文件：cap 0.65 → warnings（由 Deduplicate 分层，这里断言置信度被压）
	out, err = engine.Run([]diff.FileDiff{mkDiff("client_test.go")})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("测试文件应保留命中（降级不删除）: %+v", out)
	}
	if out[0].Confidence != 0.65 {
		t.Errorf("测试文件置信度应 cap 0.65, got %.2f", out[0].Confidence)
	}
	if !strings.Contains(out[0].Title, "降级") {
		t.Errorf("降级命中应带角色说明: %q", out[0].Title)
	}
}

// TestEngineNolintDirective //nolint 指令行命中被剔除（lint 生态契约）。
func TestEngineNolintDirective(t *testing.T) {
	fd := diff.FileDiff{
		NewPath: "editor.go",
		Hunks: []diff.Hunk{
			{
				Lines: []diff.Line{
					{Type: diff.LineAdded, NewLine: 3, Content: `c := exec.Command(editor) //nolint:gosec`},
					{Type: diff.LineAdded, NewLine: 4, Content: `d := exec.Command(other)`},
				},
			},
		},
	}
	engine := NewEngine()
	engine.Register(NewTokenCommandInjectionRule())
	out, err := engine.Run([]diff.FileDiff{fd})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("nolint 行剔除后应只剩 1 条命中: %+v", out)
	}
	if out[0].Line != 4 {
		t.Errorf("剩余命中应在未标记行: %d", out[0].Line)
	}
}

// TestURLAuthorityHasCredentials URL 凭据判定必须限定在 authority 段内。
func TestURLAuthorityHasCredentials(t *testing.T) {
	yes := []string{
		"https://user:pass@example.com/path",
		"postgres://svc:secret@10.0.0.8:5432/app",
		"postgres://svc:Pr0d-P@ss@10.0.0.8:5432/app", // 密码含 @（LastIndex 语义）
		"mysql://root:p@localhost/db",
	}
	no := []string{
		"https://proxy.golang.org/github.com/example/pkg/@v/list", // path 里的 @
		"https://example.com/path?user=adm&pass=x",                // query 里的冒号
		"https://example.com/a@b",                                 // path 里单独 @
		"https://user@example.com/",                               // @ 有但无 user:pass 冒号
		"https://:pass@example.com/",                              // user 为空
		"https://user:@example.com/",                              // pass 为空
		"not-a-url",
	}
	for _, s := range yes {
		if !urlAuthorityHasCredentials(s) {
			t.Errorf("应检出凭据: %s", s)
		}
	}
	for _, s := range no {
		if urlAuthorityHasCredentials(s) {
			t.Errorf("不应误报凭据: %s", s)
		}
	}
}

// TestErrBlankDiscardLayers 显式全丢弃（_ = f / _, _ = f）降级弱提醒：
// safeIgnores 族静默、纯变量清理静默、其余 0.55 warnings 通道。
func TestErrBlankDiscardLayers(t *testing.T) {
	rule := NewTokenErrorRule()
	mk := func(line string, no int) diff.FileDiff {
		return diff.FileDiff{
			NewPath: "a.go",
			Hunks: []diff.Hunk{{Lines: []diff.Line{
				{Type: diff.LineAdded, NewLine: no, Content: line},
			}}},
		}
	}

	// safeIgnores 族显式丢弃：静默
	out, _ := rule.Check(mk(`_ = f.Close()`, 3))
	if len(out) != 0 {
		t.Errorf("_ = f.Close() 应静默: %+v", out)
	}
	out, _ = rule.Check(mk(`_, _ = fmt.Fprintf(w, "x")`, 3))
	if len(out) != 0 {
		t.Errorf("_, _ = fmt.Fprintf 应静默: %+v", out)
	}
	// 纯变量清理：静默
	out, _ = rule.Check(mk(`_ = stale`, 3))
	if len(out) != 0 {
		t.Errorf("_ = 纯变量 应静默: %+v", out)
	}
	// 一般调用显式全丢弃：0.55 弱提醒
	out, _ = rule.Check(mk(`_, _ = w.WriteString(seq)`, 3))
	if len(out) != 1 || out[0].Confidence != 0.55 {
		t.Errorf("_, _ = w.WriteString 应 0.55: %+v", out)
	}
	// 混合形态末位 _: 0.80 上报
	out, _ = rule.Check(mk(`n, _ := writeAll(nil, nil)`, 3))
	if len(out) != 1 || out[0].Confidence != 0.80 {
		t.Errorf("n, _ := writeAll 应 0.80: %+v", out)
	}
}

// TestErrNonFinalUnderscoreExempt 位置约定收紧：非末位 _ 完全豁免。
func TestErrNonFinalUnderscoreExempt(t *testing.T) {
	rule := NewTokenErrorRule()
	mk := func(line string) diff.FileDiff {
		return diff.FileDiff{
			NewPath: "a.go",
			Hunks: []diff.Hunk{{Lines: []diff.Line{
				{Type: diff.LineAdded, NewLine: 3, Content: line},
			}}},
		}
	}
	for _, line := range []string{
		`_, err = db.Exec(q, args...)`,
		`_, isWait := agent.AsWaitNoticeTimeoutError(err)`,
		`for _, v := range nodes {`,
		`for _, i := m.Next(); i < n; i++ {`,
	} {
		out, _ := rule.Check(mk(line))
		if len(out) != 0 {
			t.Errorf("非末位 _ 应完全豁免: %q -> %+v", line, out)
		}
	}
	// 对照：末位 _ 仍是错误位丢弃，保持上报
	out, _ := rule.Check(mk(`for i, _ = m.Next(); i < n; i++ {`))
	if len(out) != 1 || out[0].Confidence != 0.80 {
		t.Errorf("末位 _ 应保持上报: %+v", out)
	}
}

// TestErrTypeAssertionExempt 类型断言 comma-ok 豁免。
func TestErrTypeAssertionExempt(t *testing.T) {
	rule := NewTokenErrorRule()
	fd := diff.FileDiff{
		NewPath: "a.go",
		Hunks: []diff.Hunk{{Lines: []diff.Line{
			{Type: diff.LineAdded, NewLine: 3, Content: `s, _ := val.(string)`},
			{Type: diff.LineAdded, NewLine: 4, Content: `g, _ := ghost.Do()`},
		}}},
	}
	out, _ := rule.Check(fd)
	if len(out) != 1 || out[0].Line != 4 {
		t.Fatalf("断言行豁免、限定调用行保持: %+v", out)
	}
}

// TestDampenFindingByRole 单条降噪不侵入 production 角色。
func TestDampenFindingByRole(t *testing.T) {
	f := &findings.Finding{Confidence: 0.95, Severity: findings.SeverityHigh, Title: "x"}
	dampenFindingByRole(f, RoleProduction)
	if f.Confidence != 0.95 {
		t.Error("production 角色不得干预置信度")
	}
	dampenFindingByRole(f, RoleTest)
	if f.Confidence != nonProductionConfCap || f.Severity != findings.SeverityLow {
		t.Errorf("test 角色应 cap 置信度并降 severity: %+v", f)
	}
}
