// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
package rules

import (
	"testing"

	"code-review-agent/diff"
)

// R3①：RES-AST-001 变量级匹配——多句柄只关一个要报
func TestResource_VariableLevelCatchesPartialClose(t *testing.T) {
	r := NewTokenResourceRule()
	files, err := diff.ReadFromContent(`--- a/copy.go
+++ b/copy.go
@@ -1,2 +1,13 @@
 package copy

+func copyFile(a, b string) {
+	f1, err := os.Open(a)
+	if err != nil {
+		return
+	}
+	f2, err := os.Open(b)
+	if err != nil {
+		return
+	}
+	defer f2.Close()
+	use(f1, f2)
+}`)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	for _, f := range files {
		out, _ := r.Check(f)
		n += len(out)
	}
	if n != 1 {
		t.Fatalf("f1 泄漏应报 1 条, got %d", n)
	}
}

// R3①：同变量跨 hunk Close 仍放行（M2 既有语义不回退）
func TestResource_CrossHunkSameVarStillOK(t *testing.T) {
	r := NewTokenResourceRule()
	files, err := diff.ReadFromContent(`--- a/x.go
+++ b/x.go
@@ -1,2 +1,5 @@
 package x

+func a() error {
+	f, err := os.Open("x")
+	if err != nil {
@@ -10,2 +13,6 @@
+		return err
+	}
+	defer f.Close()
+	return nil
+}
+`)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	for _, f := range files {
		out, _ := r.Check(f)
		n += len(out)
	}
	if n != 0 {
		t.Errorf("同变量跨 hunk Close 不应上报, got %d", n)
	}
}

// R3②：白名单调用的 _ 丢弃不报
func TestErrWhitelist_SafeIgnores(t *testing.T) {
	r := NewTokenErrorRule()
	files, err := diff.ReadFromContent(`--- a/u.go
+++ b/u.go
@@ -1,2 +1,7 @@
 package u

+func parse(data []byte, tmp string) (Item, error) {
+	_ = os.Remove(tmp)
+	var v Item
+	_ = json.Unmarshal(data, &v)
+	return v, nil
+}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		out, _ := r.Check(f)
		if len(out) != 0 {
			t.Fatalf("白名单调用不应上报: %+v", out)
		}
	}
}

// R3③：os.Getenv 豁免（即便变量名敏感、env 名再长）
func TestSecretEnvExempt(t *testing.T) {
	r := NewTokenSecretRule()
	files, err := diff.ReadFromContent(`--- a/c.go
+++ b/c.go
@@ -1,2 +1,6 @@
 package c

+func load() (*Cfg, error) {
+	apiKey := os.Getenv("PROD_API_KEY_VALUE")
+	return &Cfg{Key: apiKey}, nil
+}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		out, _ := r.Check(f)
		if len(out) != 0 {
			t.Fatalf("os.Getenv 不应上报: %+v", out)
		}
	}
}
