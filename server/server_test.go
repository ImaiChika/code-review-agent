// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// newTestServer 启动一个完整的测试服务（真实 SQLite + 内嵌前端 + 真实审查管线）。
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	tmp := t.TempDir()
	s, err := New(Config{
		Port:        0,
		DBPath:      filepath.Join(tmp, "review.db"),
		DataDir:     filepath.Join(tmp, "data"),
		SampleDir:   "../testdata",
		SandboxMode: "off",
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

const sampleSecretDiff = `--- a/creds.go
+++ b/creds.go
@@ -1,2 +1,4 @@
 package creds

+var apiKey = "sk-live-q1w2e3r4t5y6u7i8o9p0"
+var dbURL = "mysql://root:Hunter2Secret@prod-db:3306/app"
`

func TestHealth(t *testing.T) {
	ts := newTestServer(t)
	res, err := http.Get(ts.URL + "/api/health")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status = %d", res.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "ok" {
		t.Errorf("status = %v, 期望 ok", body["status"])
	}
	if body["version"] == "" {
		t.Error("version 不应为空")
	}
}

func TestFrontendAssets(t *testing.T) {
	ts := newTestServer(t)

	res, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body := new(bytes.Buffer)
	body.ReadFrom(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("GET / status = %d", res.StatusCode)
	}
	if !strings.Contains(body.String(), "CR//AGENT") {
		t.Error("首页应包含品牌标识")
	}
	if !strings.Contains(body.String(), "代码审查控制台") {
		t.Error("首页应包含中文标题")
	}

	for _, path := range []string{"/static/app.js", "/static/style.css"} {
		res, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 200 {
			t.Errorf("GET %s status = %d", path, res.StatusCode)
		}
	}
}

// submitReview 提交一次审查并返回报告 JSON。
func submitReview(t *testing.T, ts *httptest.Server, body string) map[string]any {
	t.Helper()
	res, err := http.Post(ts.URL+"/api/reviews", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("status = %d, body = %v", res.StatusCode, out)
	}
	return out
}

func TestCreateReview_FromDiffContent(t *testing.T) {
	ts := newTestServer(t)

	reqBody, _ := json.Marshal(map[string]any{"diff_content": sampleSecretDiff})
	rep := submitReview(t, ts, string(reqBody))

	if rep["task_id"] == nil || rep["task_id"] == "" {
		t.Error("task_id 不应为空")
	}
	if rep["input_type"] != "diff_content" {
		t.Errorf("input_type = %v", rep["input_type"])
	}

	// findings 应包含 SEC-AST-001，且 evidence 无明文密钥
	raw, _ := json.Marshal(rep["findings"])
	findingsJSON := string(raw)
	if !strings.Contains(findingsJSON, "SEC-AST-001") {
		t.Error("应检出 SEC-AST-001 硬编码密钥")
	}
	if strings.Contains(findingsJSON, "sk-live-q1w2e3r4t5y6u7i8o9p0") ||
		strings.Contains(findingsJSON, "Hunter2Secret") {
		t.Error("findings 中泄漏了明文密钥")
	}

	// monitor.tool_call_count 应为 0（无沙箱）
	monitor, _ := rep["monitor"].(map[string]any)
	if monitor["tool_call_count"].(float64) != 0 {
		t.Errorf("tool_call_count = %v, 期望 0", monitor["tool_call_count"])
	}
}

func TestCreateReview_Validation(t *testing.T) {
	ts := newTestServer(t)

	// 空输入 → 400
	res, _ := http.Post(ts.URL+"/api/reviews", "application/json",
		strings.NewReader(`{}`))
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("空输入 status = %d, 期望 400", res.StatusCode)
	}
	res.Body.Close()

	// 非法 JSON → 400
	res, _ = http.Post(ts.URL+"/api/reviews", "application/json",
		strings.NewReader(`{bad json`))
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("非法 JSON status = %d, 期望 400", res.StatusCode)
	}
	res.Body.Close()

	// 非 diff 文本（无可审查变更）→ 422
	res, _ = http.Post(ts.URL+"/api/reviews", "application/json",
		strings.NewReader(`{"diff_content":"plain text, not a diff"}`))
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("空变更 status = %d, 期望 422", res.StatusCode)
	}
	res.Body.Close()

	// GET 方法被拒 → 405
	res, _ = http.Get(ts.URL + "/api/reviews")
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET reviews status = %d, 期望 405", res.StatusCode)
	}
	res.Body.Close()
}

func TestTaskLifecycle(t *testing.T) {
	ts := newTestServer(t)

	reqBody, _ := json.Marshal(map[string]any{"diff_content": sampleSecretDiff})
	rep := submitReview(t, ts, string(reqBody))
	taskID := rep["task_id"].(string)

	// 任务列表包含该任务
	res, _ := http.Get(ts.URL + "/api/tasks?limit=10")
	var list map[string]any
	json.NewDecoder(res.Body).Decode(&list)
	res.Body.Close()
	rawList, _ := json.Marshal(list["tasks"])
	if !strings.Contains(string(rawList), taskID) {
		t.Error("任务列表应包含刚创建的任务")
	}

	// 详情
	res, _ = http.Get(ts.URL + "/api/tasks/" + taskID)
	if res.StatusCode != 200 {
		t.Fatalf("详情 status = %d", res.StatusCode)
	}
	var detail map[string]any
	json.NewDecoder(res.Body).Decode(&detail)
	res.Body.Close()
	if detail["task"] == nil || detail["report"] == nil {
		t.Error("详情应包含 task 与 report")
	}

	// Markdown 报告
	res, _ = http.Get(ts.URL + "/api/tasks/" + taskID + "/report")
	if res.StatusCode != 200 {
		t.Fatalf("报告 status = %d", res.StatusCode)
	}
	ct := res.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/markdown") {
		t.Errorf("报告 Content-Type = %q", ct)
	}
	res.Body.Close()

	// 不存在的任务 → 404
	res, _ = http.Get(ts.URL + "/api/tasks/task-not-exist")
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("不存在任务 status = %d, 期望 404", res.StatusCode)
	}
	res.Body.Close()
}

func TestStats(t *testing.T) {
	ts := newTestServer(t)

	reqBody, _ := json.Marshal(map[string]any{"diff_content": sampleSecretDiff})
	submitReview(t, ts, string(reqBody))
	submitReview(t, ts, string(reqBody)) // 跑两次，验证聚合

	res, _ := http.Get(ts.URL + "/api/stats")
	var stats map[string]any
	json.NewDecoder(res.Body).Decode(&stats)
	res.Body.Close()

	if stats["total_tasks"].(float64) < 2 {
		t.Errorf("total_tasks = %v, 期望 ≥ 2", stats["total_tasks"])
	}
	fsStats, _ := stats["finding_stats"].(map[string]any)
	if fsStats == nil || fsStats["total"].(float64) == 0 {
		t.Error("finding_stats.total 应 > 0")
	}
	bySeverity, _ := fsStats["by_severity"].(map[string]any)
	if bySeverity["high"] == nil || bySeverity["high"].(float64) == 0 {
		t.Error("高危发现统计应 > 0")
	}
	topRules, _ := fsStats["top_rules"].([]any)
	if len(topRules) == 0 {
		t.Error("top_rules 不应为空")
	}
}

func TestRules(t *testing.T) {
	ts := newTestServer(t)
	res, _ := http.Get(ts.URL + "/api/rules")
	var data map[string]any
	json.NewDecoder(res.Body).Decode(&data)
	res.Body.Close()

	rules, _ := data["rules"].([]any)
	if len(rules) < 6 {
		t.Errorf("规则数 = %d, 期望 ≥ 6（内置规则）", len(rules))
	}
	raw, _ := json.Marshal(data["rules"])
	if !strings.Contains(string(raw), "SEC-AST-001") {
		t.Error("应包含 SEC-AST-001")
	}
	dims, _ := data["dimensions"].([]any)
	if len(dims) != 6 {
		t.Errorf("评分维度 = %d, 期望 6", len(dims))
	}
}

func TestSamples(t *testing.T) {
	ts := newTestServer(t)
	res, _ := http.Get(ts.URL + "/api/samples")
	var data map[string]any
	json.NewDecoder(res.Body).Decode(&data)
	res.Body.Close()

	count, _ := data["count"].(float64)
	if count < 8 {
		t.Errorf("示例数 = %v, 期望 ≥ 8（testdata 内置 fixture）", count)
	}
	samples, _ := data["samples"].([]any)
	first, _ := samples[0].(map[string]any)
	if first["name"] == "" || first["content"] == "" {
		t.Error("示例应包含 name 与 content")
	}
}

func TestConcurrency_SerializedReviews(t *testing.T) {
	ts := newTestServer(t)

	// 并发提交 3 个审查，验证互斥锁下全部成功落库
	errCh := make(chan error, 3)
	for i := 0; i < 3; i++ {
		go func(i int) {
			body := fmt.Sprintf(`{"diff_content":%q}`,
				strings.ReplaceAll(sampleSecretDiff, "creds.go", fmt.Sprintf("f%d.go", i)))
			res, err := http.Post(ts.URL+"/api/reviews", "application/json",
				strings.NewReader(body))
			if err != nil {
				errCh <- err
				return
			}
			if res.StatusCode != 200 {
				errCh <- fmt.Errorf("并发审查 #%d status = %d", i, res.StatusCode)
				return
			}
			res.Body.Close()
			errCh <- nil
		}(i)
	}
	for i := 0; i < 3; i++ {
		if err := <-errCh; err != nil {
			t.Error(err)
		}
	}
}
