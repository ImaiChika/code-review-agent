// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
package rules

import (
	"testing"

	"code-review-agent/findings"
)

// runR2 复用 runR1Rule 的真实 diff 解析路径。

func TestInsecureTLS(t *testing.T) {
	fs := runR1Rule(t, NewTokenInsecureTLSRule(), `--- a/c.go
+++ b/c.go
@@ -1,2 +1,6 @@
 package c

+func dial() *http.Client {
+	tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
+	return &http.Client{Transport: tr}
+}`)
	if len(fs) != 1 || fs[0].Line != 4 {
		t.Fatalf("应命中 c.go:4, got %+v", fs)
	}
}

func TestInsecureTLS_CommentNotReported(t *testing.T) {
	fs := runR1Rule(t, NewTokenInsecureTLSRule(), `--- a/c.go
+++ b/c.go
@@ -1,2 +1,4 @@
 package c

+// InsecureSkipVerify: true 会跳过校验
+func dial() *http.Client { return nil }
+`)
	if len(fs) != 0 {
		t.Errorf("注释不应上报: %+v", fs)
	}
}

func TestContextRoot_InCtxFunc(t *testing.T) {
	fs := runR1Rule(t, NewTokenContextRootRule(), `--- a/h.go
+++ b/h.go
@@ -1,2 +1,7 @@
 package h

+func handle(ctx context.Context, req *Request) error {
+	if req == nil {
+		return use(context.TODO())
+	}
+	return use(ctx)
+}`)
	if len(fs) != 1 || fs[0].Line != 5 {
		t.Fatalf("应命中 h.go:5, got %+v", fs)
	}
	if fs[0].Confidence > 0.75 {
		t.Errorf("本规则误报风险最高，confidence 应压 0.70, got %v", fs[0].Confidence)
	}
}

func TestContextRoot_ReturnValueSignatureNotReported(t *testing.T) {
	// 返回值位置 context.Context ≠ 接收调用方 ctx
	fs := runR1Rule(t, NewTokenContextRootRule(), `--- a/m.go
+++ b/m.go
@@ -1,2 +1,7 @@
 package m

+func setup() context.Context {
+	ctx := context.Background()
+	return context.WithValue(ctx, key, val)
+}`)
	if len(fs) != 0 {
		t.Errorf("返回值位置签名不应上报: %+v", fs)
	}
}

func TestMutex_NoUnlock(t *testing.T) {
	fs := runR1Rule(t, NewTokenMutexRule(), `--- a/s.go
+++ b/s.go
@@ -1,2 +1,7 @@
 package s

+func (s *store) set(k, v string) {
+	s.mu.Lock()
+	s.data[k] = v
+	use(s.data)
+}`)
	if len(fs) != 1 || fs[0].Line != 4 {
		t.Fatalf("应命中 s.go:4, got %+v", fs)
	}
}

func TestMutex_DeferUnlockNotReported(t *testing.T) {
	fs := runR1Rule(t, NewTokenMutexRule(), `--- a/s.go
+++ b/s.go
@@ -1,2 +1,7 @@
 package s

+func (s *store) set(k, v string) {
+	s.mu.Lock()
+	defer s.mu.Unlock()
+	use(s.data)
+}`)
	if len(fs) != 0 {
		t.Errorf("defer Unlock 不应上报: %+v", fs)
	}
}

func TestMutex_RLockNotMatched(t *testing.T) {
	fs := runR1Rule(t, NewTokenMutexRule(), `--- a/s.go
+++ b/s.go
@@ -1,2 +1,7 @@
 package s

+func (s *store) get(k string) string {
+	s.mu.RLock()
+	defer s.mu.RUnlock()
+	return s.data[k]
+}`)
	if len(fs) != 0 {
		t.Errorf("读锁不应被 .Lock() 规则匹配: %+v", fs)
	}
}

func TestLoopTimer_DeferInLoop(t *testing.T) {
	fs := runR1Rule(t, NewTokenLoopTimerRule(), `--- a/sc.go
+++ b/sc.go
@@ -1,2 +1,11 @@
 package sc

+func scan(files []string) {
+	for _, name := range files {
+		f, err := os.Open(name)
+		if err != nil {
+			continue
+		}
+		defer f.Close()
+		use(f)
+	}
+}`)
	var hit bool
	for _, f := range fs {
		if f.Line == 9 {
			hit = true
		}
	}
	if !hit {
		t.Fatalf("应命中 defer 行 sc.go:9, got %+v", fs)
	}
}

func TestLoopTimer_TimeTick(t *testing.T) {
	fs := runR1Rule(t, NewTokenLoopTimerRule(), `--- a/p.go
+++ b/p.go
@@ -1,2 +1,6 @@
 package p

+func poll() {
+	c := time.Tick(time.Minute)
+	use(c)
+}`)
	if len(fs) != 1 || fs[0].Line != 4 {
		t.Fatalf("time.Tick 应命中 p.go:4, got %+v", fs)
	}
}

func TestLoopTimer_TickerNoStop(t *testing.T) {
	fs := runR1Rule(t, NewTokenLoopTimerRule(), `--- a/m.go
+++ b/m.go
@@ -1,2 +1,6 @@
 package m

+func watch() {
+	t := time.NewTicker(time.Second)
+	use(t.C)
+}`)
	if len(fs) != 1 || fs[0].Line != 4 {
		t.Fatalf("NewTicker 无 Stop 应命中 m.go:4, got %+v", fs)
	}
}

func TestLoopTimer_TickerStoppedNotReported(t *testing.T) {
	fs := runR1Rule(t, NewTokenLoopTimerRule(), `--- a/m.go
+++ b/m.go
@@ -1,2 +1,7 @@
 package m

+func watch() {
+	t := time.NewTicker(time.Second)
+	defer t.Stop()
+	use(t.C)
+}`)
	if len(fs) != 0 {
		t.Errorf("defer Stop 不应上报: %+v", fs)
	}
}

func TestRowsErr_Missing(t *testing.T) {
	fs := runR1Rule(t, NewTokenRowsErrRule(), `--- a/d.go
+++ b/d.go
@@ -1,2 +1,10 @@
 package d

+func all() ([]Item, error) {
+	rows, err := db.Query("SELECT id FROM items")
+	if err != nil {
+		return nil, err
+	}
+	defer rows.Close()
+	return collect(rows)
+}`)
	if len(fs) != 1 || fs[0].Line != 4 {
		t.Fatalf("应命中 d.go:4, got %+v", fs)
	}
	if fs[0].Category != findings.CategoryLifecycle {
		t.Errorf("category 应为 lifecycle, got %v", fs[0].Category)
	}
}

func TestRowsErr_CheckedNotReported(t *testing.T) {
	fs := runR1Rule(t, NewTokenRowsErrRule(), `--- a/d.go
+++ b/d.go
@@ -1,2 +1,12 @@
 package d

+func all() ([]Item, error) {
+	rows, err := db.Query("SELECT id FROM items")
+	if err != nil {
+		return nil, err
+	}
+	defer rows.Close()
+	if err := collect(rows); err != nil {
+		return nil, err
+	}
+	return nil, rows.Err()
+}`)
	if len(fs) != 0 {
		t.Errorf("rows.Err() 已检查不应上报: %+v", fs)
	}
}
