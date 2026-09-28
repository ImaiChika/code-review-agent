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
	"time"

	"code-review-agent/report"
	"code-review-agent/review"
	"code-review-agent/storage"
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
// submitReview 提交审查（M7-F1 起为异步：202 + task_id）。
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
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, 期望 202, body = %v", res.StatusCode, out)
	}
	if out["task_id"] == nil || out["task_id"] == "" {
		t.Fatalf("202 响应缺少 task_id: %v", out)
	}
	return out
}

// getTaskDetail 拉取任务详情，返回 {task, report, ...}；report 可能为 null（进行中/失败）。
func getTaskDetail(t *testing.T, ts *httptest.Server, taskID string) (map[string]any, int) {
	t.Helper()
	res, err := http.Get(ts.URL + "/api/tasks/" + taskID)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("解析任务详情失败: %v", err)
	}
	return out, res.StatusCode
}

// waitTaskDone 轮询直到任务进入终态（completed/failed），返回详情。
// 注：真实管线先落库后置 completed，因此 completed 时报告必已可查；
// 注入 fake runner 的用例没有报告，completed + report null 同样是合法终态。
func waitTaskDone(t *testing.T, ts *httptest.Server, taskID string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		d, code := getTaskDetail(t, ts, taskID)
		if code == http.StatusOK {
			task, _ := d["task"].(map[string]any)
			if task != nil {
				switch task["status"] {
				case "completed", "failed":
					return d
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("任务 %s 30s 内未完成: %v", taskID, d)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestCreateReview_FromDiffContent(t *testing.T) {
	ts := newTestServer(t)

	reqBody, _ := json.Marshal(map[string]any{"diff_content": sampleSecretDiff})
	sub := submitReview(t, ts, string(reqBody))
	taskID := sub["task_id"].(string)

	// 轮询到终态后取报告
	d := waitTaskDone(t, ts, taskID)
	task, _ := d["task"].(map[string]any)
	if task["input_type"] != "diff_content" {
		t.Errorf("input_type = %v", task["input_type"])
	}
	if task["status"] != "completed" {
		t.Fatalf("status = %v, 期望 completed（errMsg=%v）", task["status"], task["error_msg"])
	}

	rep, _ := d["report"].(map[string]any)
	if rep == nil {
		t.Fatal("completed 任务应有报告")
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

	// 纯上下文 diff（可解析但没有新增行）→ 422（M7-F8，P2-10）
	res, _ = http.Post(ts.URL+"/api/reviews", "application/json",
		strings.NewReader(`{"diff_content":"--- a/x.go\n+++ b/x.go\n@@ -1,3 +1,3 @@\n package x\n \n func keep() {}\n"}`))
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("纯上下文 diff status = %d, 期望 422", res.StatusCode)
	}
	res.Body.Close()

	// 不存在的仓库路径 → 400（M7-F8，P3-12；此前是 500）
	res, _ = http.Post(ts.URL+"/api/reviews", "application/json",
		strings.NewReader(`{"repo_path":"/nonexistent/repo/xyz"}`))
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("不存在仓库路径 status = %d, 期望 400", res.StatusCode)
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

	// M7-F1：入队后任务立即出现在详情里（进行中态，无报告）
	d0, code := getTaskDetail(t, ts, taskID)
	if code != http.StatusOK {
		t.Fatalf("刚入队任务详情 status = %d, 期望 200", code)
	}
	task0, _ := d0["task"].(map[string]any)
	if st := task0["status"]; st != "queued" && st != "running" && st != "completed" {
		t.Errorf("刚入队任务 status = %v（queued/running/completed 均可，取决于调度速度）", st)
	}

	// 等待完成后走完生命周期断言
	waitTaskDone(t, ts, taskID)

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
	d, _ := getTaskDetail(t, ts, taskID)
	if d["task"] == nil || d["report"] == nil {
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
	r1 := submitReview(t, ts, string(reqBody))
	r2 := submitReview(t, ts, string(reqBody)) // 跑两次，验证聚合
	waitTaskDone(t, ts, r1["task_id"].(string))
	waitTaskDone(t, ts, r2["task_id"].(string))

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

	// M7-F1：并发提交 3 个审查，全部应立即 202（不被彼此阻塞），且最终全部成功落库
	type subResult struct {
		id  string
		err error
	}
	subCh := make(chan subResult, 3)
	for i := 0; i < 3; i++ {
		go func(i int) {
			body := fmt.Sprintf(`{"diff_content":%q}`,
				strings.ReplaceAll(sampleSecretDiff, "creds.go", fmt.Sprintf("f%d.go", i)))
			res, err := http.Post(ts.URL+"/api/reviews", "application/json",
				strings.NewReader(body))
			if err != nil {
				subCh <- subResult{err: err}
				return
			}
			var out map[string]any
			json.NewDecoder(res.Body).Decode(&out)
			res.Body.Close()
			if res.StatusCode != http.StatusAccepted {
				subCh <- subResult{err: fmt.Errorf("并发审查 #%d status = %d", i, res.StatusCode)}
				return
			}
			subCh <- subResult{id: out["task_id"].(string)}
		}(i)
	}
	ids := make([]string, 0, 3)
	for i := 0; i < 3; i++ {
		r := <-subCh
		if r.err != nil {
			t.Error(r.err)
			continue
		}
		ids = append(ids, r.id)
	}
	// 三个任务全部 completed 且可取到报告
	for _, id := range ids {
		d := waitTaskDone(t, ts, id)
		task, _ := d["task"].(map[string]any)
		if task["status"] != "completed" {
			t.Errorf("任务 %s status = %v, 期望 completed", id, task["status"])
		}
		if d["report"] == nil {
			t.Errorf("任务 %s 完成后应有报告", id)
		}
	}
}

// ========== M7-F1：异步队列状态机 ==========

// newInjectedServer 启动一个注入 fake runFn 的测试服务（不跑真实审查管线）。
func newInjectedServer(t *testing.T, workers int, timeout time.Duration,
	runFn func(review.Options) (*report.ReviewReport, error)) (*Server, *httptest.Server) {
	t.Helper()
	tmp := t.TempDir()
	s, err := New(Config{
		Port:        0,
		DBPath:      filepath.Join(tmp, "review.db"),
		DataDir:     filepath.Join(tmp, "data"),
		SampleDir:   "../testdata",
		SandboxMode: "off",
		Workers:     workers,
		TaskTimeout: timeout,
	})
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	s.queue.runFn = runFn
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() {
		ts.Close()
		_ = s.Close()
	})
	return s, ts
}

func fakeOK(opts review.Options) (*report.ReviewReport, error) {
	return &report.ReviewReport{TaskID: opts.TaskID}, nil
}

func TestAsyncQueue_RunningStateVisible(t *testing.T) {
	// 慢任务执行期间，详情应 200 返回 running 态且 report 为 null
	release := make(chan struct{})
	_, ts := newInjectedServer(t, 1, time.Minute, func(opts review.Options) (*report.ReviewReport, error) {
		<-release
		return fakeOK(opts)
	})

	sub := submitReview(t, ts, `{"diff_content":"--- a/x.go\n+++ b/x.go\n@@ -1,2 +1,4 @@\n package x\n \n+var apiKey = \"sk-live-000000000000\"\n+var _ = 1\n"}`)
	id := sub["task_id"].(string)

	d, code := getTaskDetail(t, ts, id)
	if code != http.StatusOK {
		t.Fatalf("执行中任务详情 status = %d, 期望 200", code)
	}
	task, _ := d["task"].(map[string]any)
	if task["status"] != "running" {
		t.Errorf("执行中 status = %v, 期望 running", task["status"])
	}
	if d["report"] != nil {
		t.Error("执行中任务 report 应为 null")
	}

	close(release)
	deadline := time.Now().Add(10 * time.Second)
	for {
		d, _ = getTaskDetail(t, ts, id)
		task, _ = d["task"].(map[string]any)
		if task["status"] == "completed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("释放后任务未完成: %v", task)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestAsyncQueue_FailurePersisted(t *testing.T) {
	// runFn 出错 → 任务 failed + 原因可见 + DB 补记失败行
	s, ts := newInjectedServer(t, 1, time.Minute, func(review.Options) (*report.ReviewReport, error) {
		return nil, fmt.Errorf("boom: 沙箱初始化失败")
	})

	sub := submitReview(t, ts, `{"diff_content":"--- a/x.go\n+++ b/x.go\n@@ -1,2 +1,4 @@\n package x\n \n+var apiKey = \"sk-live-000000000000\"\n+var _ = 1\n"}`)
	id := sub["task_id"].(string)

	d := waitTaskDone(t, ts, id)
	task, _ := d["task"].(map[string]any)
	if task["status"] != "failed" {
		t.Fatalf("status = %v, 期望 failed", task["status"])
	}
	if msg, _ := task["error_msg"].(string); msg == "" {
		t.Error("失败原因 error_msg 不应为空")
	} else if !strings.Contains(msg, "boom") {
		t.Errorf("error_msg = %q, 应包含失败原因", msg)
	}
	if d["report"] != nil {
		t.Error("失败任务 report 应为 null")
	}

	// DB 补记的失败行可查（历史可见）
	dbTask, err := s.store.GetTask(id)
	if err != nil {
		t.Fatalf("失败任务应补记进 DB: %v", err)
	}
	if dbTask.Status != "failed" || dbTask.ErrorMsg == "" {
		t.Errorf("DB 行 = %v (err=%q), 期望 failed 带原因", dbTask.Status, dbTask.ErrorMsg)
	}
}

func TestAsyncQueue_TimeoutWatchdog(t *testing.T) {
	// runFn 超过看门狗上限 → 任务标 failed 且提示超时
	_, ts := newInjectedServer(t, 1, 150*time.Millisecond, func(opts review.Options) (*report.ReviewReport, error) {
		time.Sleep(3 * time.Second)
		return fakeOK(opts)
	})

	sub := submitReview(t, ts, `{"diff_content":"--- a/x.go\n+++ b/x.go\n@@ -1,2 +1,4 @@\n package x\n \n+var apiKey = \"sk-live-000000000000\"\n+var _ = 1\n"}`)
	id := sub["task_id"].(string)

	d := waitTaskDone(t, ts, id)
	task, _ := d["task"].(map[string]any)
	if task["status"] != "failed" {
		t.Fatalf("status = %v, 期望 failed（超时）", task["status"])
	}
	if msg, _ := task["error_msg"].(string); !strings.Contains(msg, "超时") {
		t.Errorf("error_msg = %q, 应包含超时提示", msg)
	}
}

func TestAsyncQueue_SubmitNotBlockedByRunningJob(t *testing.T) {
	// 单 worker 下，第一个任务慢执行时，第二个提交必须立即 202（不再互相阻塞）
	release := make(chan struct{})
	_, ts := newInjectedServer(t, 1, time.Minute, func(opts review.Options) (*report.ReviewReport, error) {
		<-release
		return fakeOK(opts)
	})

	sub1 := submitReview(t, ts, `{"diff_content":"--- a/a.go\n+++ b/a.go\n@@ -1,2 +1,4 @@\n package a\n \n+var apiKey = \"sk-live-000000000000\"\n+var _ = 1\n"}`)

	// 第二个提交在第一个占用 worker 期间到达：必须快速拿到 202
	start := time.Now()
	sub2 := submitReview(t, ts, `{"diff_content":"--- a/b.go\n+++ b/b.go\n@@ -1,2 +1,4 @@\n package b\n \n+var apiKey = \"sk-live-000000000001\"\n+var _ = 1\n"}`)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("第二个提交被阻塞 %v（期望毫秒级 202）", elapsed)
	}

	// 第二个任务此时应处于 queued（worker 被第一个占着）
	d, _ := getTaskDetail(t, ts, sub2["task_id"].(string))
	task, _ := d["task"].(map[string]any)
	if task["status"] != "queued" {
		t.Logf("第二个任务 status = %v（单 worker 下预期 queued）", task["status"])
	}

	close(release)
	waitTaskDone(t, ts, sub1["task_id"].(string))
	waitTaskDone(t, ts, sub2["task_id"].(string))
}

// TestStats_TotalAccurate_Beyond200 M7-F5（修 P2-11）：
// 超过 200 个任务后 total_tasks 仍准确（SQL COUNT 全量），且 stats 响应含每日趋势。
func TestStats_TotalAccurate_Beyond200(t *testing.T) {
	tmp := t.TempDir()
	s, err := New(Config{
		Port: 0, DBPath: filepath.Join(tmp, "review.db"), DataDir: filepath.Join(tmp, "data"),
		SampleDir: "../testdata", SandboxMode: "off",
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() { ts.Close(); _ = s.Close() })

	// 直接造 250 条任务行（有报告的任务 1 条，其余仅任务行——聚合不依赖报告 JSON）
	seedTask, _ := json.Marshal(map[string]any{"diff_content": sampleSecretDiff})
	sub := submitReview(t, ts, string(seedTask))
	waitTaskDone(t, ts, sub["task_id"].(string))
	for i := 0; i < 249; i++ {
		if err := s.store.CreateTask(&storage.ReviewTask{
			TaskID:    fmt.Sprintf("task-seed-%04d", i),
			Status:    "completed",
			InputType: "diff_content",
			InputPath: "seed",
			StartedAt: time.Now(),
			RiskScore: float64(i % 100),
		}); err != nil {
			t.Fatal(err)
		}
	}

	res, _ := http.Get(ts.URL + "/api/stats")
	var stats map[string]any
	json.NewDecoder(res.Body).Decode(&stats)
	res.Body.Close()

	if got := stats["total_tasks"].(float64); got != 250 {
		t.Errorf("total_tasks = %.0f, 期望 250（全量 COUNT，非 200 截断）", got)
	}
	daily, _ := stats["trend_daily"].([]any)
	if len(daily) == 0 {
		t.Error("trend_daily 不应为空（今天有任务）")
	}
	day, _ := daily[len(daily)-1].(map[string]any)
	if day["tasks"].(float64) < 250 {
		t.Errorf("当日任务数 = %v, 期望 ≥ 250", day["tasks"])
	}
}
