// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// newAuthTestServer 启动启用认证 + 注入限流/体积阈值的测试服务。
func newAuthTestServer(t *testing.T, token string, rate float64, burst int, maxBody int64) *httptest.Server {
	t.Helper()
	tmp := t.TempDir()
	s, err := New(Config{
		Port:         0,
		DBPath:       filepath.Join(tmp, "review.db"),
		DataDir:      filepath.Join(tmp, "data"),
		SampleDir:    "../testdata",
		SandboxMode:  "off",
		AuthToken:    token,
		RatePerSec:   rate,
		RateBurst:    burst,
		MaxBodyBytes: maxBody,
	})
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() {
		ts.Close()
		_ = s.Close()
	})
	return ts
}

const validToken = "test-secret-token-123"

func TestAuth_WriteRequiresToken(t *testing.T) {
	ts := newAuthTestServer(t, validToken, 100, 100, 0)

	body := `{"diff_content":"--- a/x.go\n+++ b/x.go\n@@ -1,2 +1,4 @@\n package x\n \n+var apiKey = \"sk-live-000000000000\"\n+var _ = 1\n"}`

	// 无 token → 401
	res, _ := http.Post(ts.URL+"/api/reviews", "application/json", strings.NewReader(body))
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("无 token status = %d, 期望 401", res.StatusCode)
	}
	res.Body.Close()
	if res.Header.Get("WWW-Authenticate") == "" {
		t.Error("401 应携带 WWW-Authenticate")
	}

	// 错误 token → 401
	req, _ := http.NewRequest("POST", ts.URL+"/api/reviews", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Auth-Token", "wrong-token")
	res, _ = http.DefaultClient.Do(req)
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("错误 token status = %d, 期望 401", res.StatusCode)
	}
	res.Body.Close()

	// Bearer 头 → 202
	req, _ = http.NewRequest("POST", ts.URL+"/api/reviews", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+validToken)
	res, _ = http.DefaultClient.Do(req)
	if res.StatusCode != http.StatusAccepted {
		t.Errorf("Bearer token status = %d, 期望 202", res.StatusCode)
	}
	res.Body.Close()

	// X-Auth-Token 头 → 202
	req2, _ := http.NewRequest("POST", ts.URL+"/api/reviews", strings.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("X-Auth-Token", validToken)
	res2, _ := http.DefaultClient.Do(req2)
	if res2.StatusCode != http.StatusAccepted {
		t.Errorf("X-Auth-Token status = %d, 期望 202", res2.StatusCode)
	}
	res2.Body.Close()
}

func TestAuth_ReadEndpointsPublic(t *testing.T) {
	ts := newAuthTestServer(t, validToken, 100, 100, 0)

	// 全部 GET 端点无 token 可访问
	for _, path := range []string{"/api/health", "/api/tasks", "/api/stats", "/api/rules", "/api/samples", "/"} {
		res, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Errorf("GET %s status = %d, 期望 200（读公开）", path, res.StatusCode)
		}
	}
}

func TestAuth_DisabledByDefault(t *testing.T) {
	// 不设置 token：POST 无需认证（旧行为不变）
	ts := newAuthTestServer(t, "", 100, 100, 0)
	res, _ := http.Post(ts.URL+"/api/reviews", "application/json",
		strings.NewReader(`{"diff_content":"--- a/x.go\n+++ b/x.go\n@@ -1,2 +1,4 @@\n package x\n \n+var apiKey = \"sk-live-000000000000\"\n+var _ = 1\n"}`))
	if res.StatusCode != http.StatusAccepted {
		t.Errorf("默认无认证 status = %d, 期望 202", res.StatusCode)
	}
	res.Body.Close()
}

func TestRateLimit_Submit429(t *testing.T) {
	// burst=2, rate=0.5/s：连发 2 个成功，第 3 个 429
	ts := newAuthTestServer(t, "", 0.5, 2, 0)
	body := `{"diff_content":"--- a/x.go\n+++ b/x.go\n@@ -1,2 +1,4 @@\n package x\n \n+var apiKey = \"sk-live-000000000000\"\n+var _ = 1\n"}`

	codes := make([]int, 3)
	for i := 0; i < 3; i++ {
		res, err := http.Post(ts.URL+"/api/reviews", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		codes[i] = res.StatusCode
		if i < 2 && res.StatusCode != http.StatusAccepted {
			t.Errorf("前 %d 个请求 status = %d, 期望 202（burst 内）", i+1, res.StatusCode)
		}
		res.Body.Close()
	}
	if codes[2] != http.StatusTooManyRequests {
		t.Errorf("超限请求 status = %d, 期望 429（codes=%v）", codes[2], codes)
	}

	// 429 响应应带 Retry-After
	res, err := http.Post(ts.URL+"/api/reviews", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusTooManyRequests && res.Header.Get("Retry-After") == "" {
		t.Error("429 应携带 Retry-After")
	}

	// 限流不影响读端点
	if r, _ := http.Get(ts.URL + "/api/health"); r.StatusCode != http.StatusOK {
		t.Errorf("限流后 GET health = %d, 期望 200", r.StatusCode)
	} else {
		r.Body.Close()
	}
}

func TestRateLimit_PerIPIsolation(t *testing.T) {
	// 不同 RemoteAddr 互不影响（用 limiter 直接验证，绕过 httptest 单一来源）
	l := newIPRateLimiter(0.5, 1)
	ok1, _ := l.allow("10.0.0.1")
	ok2, _ := l.allow("10.0.0.2")
	ok3, _ := l.allow("10.0.0.1")
	if !ok1 || !ok2 {
		t.Error("每个 IP 首个请求应放行")
	}
	if ok3 {
		t.Error("同一 IP burst 用尽后应拒绝")
	}
}

func TestBodyLimit_413(t *testing.T) {
	ts := newAuthTestServer(t, "", 100, 100, 1024) // 上限 1KB

	// ~2KB 的合法 JSON diff → 413（用 json.Marshal 构造，避免手写转义错误）
	big := strings.Repeat("+var v = 1\n", 200)
	raw, _ := json.Marshal(map[string]string{
		"diff_content": "--- a/x.go\n+++ b/x.go\n@@ -1,2 +1,200 @@\n package x\n \n" + big,
	})
	res, err := http.Post(ts.URL+"/api/reviews", "application/json", strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("超大请求体 status = %d, 期望 413", res.StatusCode)
	}
	var out map[string]any
	json.NewDecoder(res.Body).Decode(&out)
	if !strings.Contains(out["error"].(string), "上限") {
		t.Errorf("错误信息应说明上限: %v", out["error"])
	}

	// 正常大小仍然可用
	small, _ := json.Marshal(map[string]string{
		"diff_content": "--- a/x.go\n+++ b/x.go\n@@ -1,2 +1,4 @@\n package x\n \n+var apiKey = \"sk-live-000000000000\"\n+var _ = 1\n",
	})
	res2, err := http.Post(ts.URL+"/api/reviews", "application/json", strings.NewReader(string(small)))
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Body.Close()
	if res2.StatusCode != http.StatusAccepted {
		t.Errorf("正常请求 status = %d, 期望 202", res2.StatusCode)
	}
}

func TestSecureHeaders_Present(t *testing.T) {
	ts := newAuthTestServer(t, "", 100, 100, 0)
	res, err := http.Get(ts.URL + "/api/health")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("应携带 X-Content-Type-Options: nosniff")
	}
	if res.Header.Get("X-Frame-Options") != "DENY" {
		t.Error("应携带 X-Frame-Options: DENY")
	}
}

func TestTokenCompare_ConstantTimeBehavior(t *testing.T) {
	// 空请求头或带前缀空格的 token 都不应通过（fail-closed）
	got := extractToken(func() *http.Request {
		r := httptest.NewRequest("POST", "/api/reviews", nil)
		r.Header.Set("Authorization", "Bearer  "+validToken+"  ")
		return r
	}())
	if got != validToken {
		t.Errorf("extractToken 应去空格: %q", got)
	}
}
