// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

const f3Diff = `--- a/leak.go
+++ b/leak.go
@@ -1,2 +1,4 @@
 package leak

+var apiKey = "sk-live-f3f3f3f3f3f3f3f3"
+var _ = 1
`

// waitCompleted202 轮询 202 任务到终态并返回详情（input_test 专用简版）。
func waitCompleted202(t *testing.T, ts *httptest.Server, body string) (map[string]any, string) {
	t.Helper()
	res, err := http.Post(ts.URL+"/api/reviews", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var sub map[string]any
	json.NewDecoder(res.Body).Decode(&sub)
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, 期望 202, body=%v", res.StatusCode, sub)
	}
	id := sub["task_id"].(string)
	d := waitTaskDone(t, ts, id)
	return d, id
}

// ========== 粘贴整文件（files_content） ==========

func TestInput_FilesContent(t *testing.T) {
	ts := newTestServer(t)
	body, _ := json.Marshal(map[string]string{
		"leak.go": "package leak\n\nvar apiKey = \"sk-live-f3f3f3f3f3f3f3f3\"\n",
	})
	req, _ := http.NewRequest("POST", ts.URL+"/api/reviews",
		strings.NewReader(`{"files_content":{"leak.go":"package leak\n\nvar apiKey = \"sk-live-f3f3f3f3f3f3f3f3\"\n"}}`))
	req.Header.Set("Content-Type", "application/json")
	res, _ := http.DefaultClient.Do(req)
	res.Body.Close()
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("files_content 提交 status = %d, 期望 202", res.StatusCode)
	}

	// 走 waitCompleted202 校验完整链路
	d := waitTaskDone(t, ts, func() string {
		r, _ := http.Post(ts.URL+"/api/reviews", "application/json",
			strings.NewReader(`{"files_content":{"leak.go":"package leak\n\nvar apiKey = \"sk-live-f3f3f3f3f3f3f3f3\"\n"}}`))
		var s map[string]any
		json.NewDecoder(r.Body).Decode(&s)
		r.Body.Close()
		return s["task_id"].(string)
	}())
	task, _ := d["task"].(map[string]any)
	if task["input_type"] != "file_contents" {
		t.Errorf("input_type = %v, 期望 file_contents", task["input_type"])
	}
	raw, _ := json.Marshal(d["report"])
	if !strings.Contains(string(raw), "SEC-AST-001") {
		t.Error("粘贴代码中的密钥应被检出")
	}
	if strings.Contains(string(raw), "sk-live-f3f3f3f3f3f3f3f3") {
		t.Error("报告不应包含明文密钥")
	}
	_ = body
}

func TestInput_FilesContentEmpty(t *testing.T) {
	ts := newTestServer(t)
	res, err := http.Post(ts.URL+"/api/reviews", "application/json",
		strings.NewReader(`{"files_content":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("空 files_content status = %d, 期望 400", res.StatusCode)
	}
}

// ========== GitHub PR 输入 ==========

func TestParsePRURL(t *testing.T) {
	cases := []struct {
		in   string
		want string // owner/repo#n
		bad  bool
	}{
		{in: "https://github.com/owner/repo/pull/123", want: "owner/repo#123"},
		{in: "http://github.com/owner/repo/pull/123/", want: "owner/repo#123"},
		{in: "https://www.github.com/owner/repo/pull/9", want: "owner/repo#9"},
		{in: "github.com/owner/repo/pull/1?files=1", want: "owner/repo#1"},
		{in: "owner/repo#42", want: "owner/repo#42"},
		{in: "https://gitlab.com/owner/repo/pull/1", bad: true},
		{in: "https://github.com/owner/repo/issues/5", bad: true},
		{in: "https://github.com/owner/repo/wiki", bad: true},
		{in: "github.com/owner/repo/pull/abc", bad: true},
		{in: "", bad: true},
	}
	for _, c := range cases {
		ref, err := parsePRURL(c.in)
		if c.bad {
			if err == nil {
				t.Errorf("parsePRURL(%q) 应失败, 得到 %v", c.in, ref)
			}
			continue
		}
		if err != nil {
			t.Errorf("parsePRURL(%q) 失败: %v", c.in, err)
			continue
		}
		if got := ref.Owner + "/" + ref.Repo + "#" + ref.Number; got != c.want {
			t.Errorf("parsePRURL(%q) = %q, 期望 %q", c.in, got, c.want)
		}
	}
}

// newGitHubMockServer 假 GitHub API：返回固定 diff / 指定状态码。
func newGitHubMockServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/pulls/") {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Accept") != "application/vnd.github.v3.diff" {
			w.WriteHeader(http.StatusNotAcceptable)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(ts.Close)
	return ts
}

func TestInput_PRURL_Mocked(t *testing.T) {
	mock := newGitHubMockServer(t, http.StatusOK, f3Diff)
	tmp := t.TempDir()
	s, err := New(Config{
		Port: 0, DBPath: filepath.Join(tmp, "review.db"), DataDir: filepath.Join(tmp, "data"),
		SampleDir: "../testdata", SandboxMode: "off", GitHubAPIBase: mock.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() { ts.Close(); _ = s.Close() })

	d, id := waitCompleted202(t, ts, `{"pr_url":"https://github.com/acme/widget/pull/7"}`)
	task, _ := d["task"].(map[string]any)
	// PR diff 走 diff 管线消费；来源标签经 InputLabel 保留 PR 定位
	if task["input_path"] != "acme/widget#7" {
		t.Errorf("input_path = %v, 期望 acme/widget#7", task["input_path"])
	}
	raw, _ := json.Marshal(d["report"])
	if !strings.Contains(string(raw), "SEC-AST-001") {
		t.Error("PR diff 中的密钥应被检出")
	}
	_ = id
}

func TestInput_PRURL_NotFound502(t *testing.T) {
	mock := newGitHubMockServer(t, http.StatusNotFound, "Not Found")
	tmp := t.TempDir()
	s, _ := New(Config{
		Port: 0, DBPath: filepath.Join(tmp, "review.db"), DataDir: filepath.Join(tmp, "data"),
		SampleDir: "../testdata", SandboxMode: "off", GitHubAPIBase: mock.URL,
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() { ts.Close(); _ = s.Close() })

	res, err := http.Post(ts.URL+"/api/reviews", "application/json",
		strings.NewReader(`{"pr_url":"https://github.com/acme/widget/pull/404"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadGateway {
		t.Errorf("PR 不存在 status = %d, 期望 502", res.StatusCode)
	}
	var out map[string]any
	json.NewDecoder(res.Body).Decode(&out)
	if !strings.Contains(out["error"].(string), "不存在") {
		t.Errorf("错误应说明 PR 不存在: %v", out["error"])
	}
}

func TestInput_PRURL_BadURL400(t *testing.T) {
	ts := newTestServer(t)
	res, err := http.Post(ts.URL+"/api/reviews", "application/json",
		strings.NewReader(`{"pr_url":"https://gitlab.com/a/b/pull/1"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("非法 PR 链接 status = %d, 期望 400", res.StatusCode)
	}
}

// ========== 上传输入（multipart / zip） ==========

func multipartBody(t *testing.T, filename, content string) (string, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, _ := w.CreateFormFile("files", filename)
	fw.Write([]byte(content))
	w.Close()
	return w.FormDataContentType(), &buf
}

func TestInput_UploadFiles(t *testing.T) {
	ts := newTestServer(t)
	ct, body := multipartBody(t, "leak.go", "package leak\n\nvar apiKey = \"sk-live-f3f3f3f3f3f3f3f3\"\n")
	res, err := http.Post(ts.URL+"/api/reviews/upload", ct, body)
	if err != nil {
		t.Fatal(err)
	}
	var sub map[string]any
	json.NewDecoder(res.Body).Decode(&sub)
	res.Body.Close()
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("上传 status = %d, 期望 202, body=%v", res.StatusCode, sub)
	}

	d := waitTaskDone(t, ts, sub["task_id"].(string))
	task, _ := d["task"].(map[string]any)
	// input_type 是管线消费形态（上传内容按 file_contents 消费）；来源见 input_path
	if task["input_type"] != "file_contents" {
		t.Errorf("input_type = %v, 期望 file_contents", task["input_type"])
	}
	if task["input_path"] != "leak.go" {
		t.Errorf("input_path = %v, 期望 leak.go（上传清单标签）", task["input_path"])
	}
	raw, _ := json.Marshal(d["report"])
	if !strings.Contains(string(raw), "SEC-AST-001") {
		t.Error("上传文件中的密钥应被检出")
	}
}

func TestInput_UploadZip(t *testing.T) {
	ts := newTestServer(t)

	// 内存构造 zip：1 个 Go 文件 + 1 个二进制（应跳过）+ 目录项
	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	fw, _ := zw.Create("src/leak.go")
	fw.Write([]byte("package leak\n\nvar apiKey = \"sk-live-f3f3f3f3f3f3f3f3\"\n"))
	bw, _ := zw.Create("blob.bin")
	bw.Write([]byte{0, 1, 2, 0, 3})
	zw.Close()

	// 包装成上传文件
	var mp bytes.Buffer
	w := multipart.NewWriter(&mp)
	fh, _ := w.CreateFormFile("files", "src.zip")
	fh.Write(zipBuf.Bytes())
	w.Close()

	res, err := http.Post(ts.URL+"/api/reviews/upload", w.FormDataContentType(), &mp)
	if err != nil {
		t.Fatal(err)
	}
	var sub map[string]any
	json.NewDecoder(res.Body).Decode(&sub)
	res.Body.Close()
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("zip 上传 status = %d, 期望 202, body=%v", res.StatusCode, sub)
	}

	d := waitTaskDone(t, ts, sub["task_id"].(string))
	raw, _ := json.Marshal(d["report"])
	if !strings.Contains(string(raw), "SEC-AST-001") {
		t.Error("zip 内 Go 文件中的密钥应被检出")
	}
	if strings.Contains(string(raw), "blob.bin") {
		t.Error("二进制条目应被跳过，不应出现在报告")
	}
}

func TestInput_UploadZipSlipRejected(t *testing.T) {
	ts := newTestServer(t)

	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	fw, _ := zw.Create("../evil.go")
	fw.Write([]byte("package evil\n"))
	zw.Close()

	var mp bytes.Buffer
	w := multipart.NewWriter(&mp)
	fh, _ := w.CreateFormFile("files", "bad.zip")
	fh.Write(zipBuf.Bytes())
	w.Close()

	res, err := http.Post(ts.URL+"/api/reviews/upload", w.FormDataContentType(), &mp)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("zip-slip status = %d, 期望 400", res.StatusCode)
	}
	var out map[string]any
	json.NewDecoder(res.Body).Decode(&out)
	if !strings.Contains(out["error"].(string), "不安全") {
		t.Errorf("错误应说明不安全路径: %v", out["error"])
	}
}

func TestInput_UploadBinaryOnlyRejected(t *testing.T) {
	ts := newTestServer(t)
	ct, body := multipartBody(t, "blob.bin", string([]byte{0, 1, 2, 0, 3}))
	res, err := http.Post(ts.URL+"/api/reviews/upload", ct, body)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("纯二进制上传 status = %d, 期望 400", res.StatusCode)
	}
}

func TestInput_UploadEmptyRejected(t *testing.T) {
	ts := newTestServer(t)
	res, err := http.Post(ts.URL+"/api/reviews/upload",
		"application/x-www-form-urlencoded", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("空上传 status = %d, 期望 400", res.StatusCode)
	}
}

// 上传端点同样受认证保护（写操作）
func TestInput_UploadRequiresTokenWhenEnabled(t *testing.T) {
	ts := newAuthTestServer(t, validToken, 100, 100, 0)
	ct, body := multipartBody(t, "leak.go", "package leak\n\nvar apiKey = \"sk-live-f3f3f3f3f3f3f3f3\"\n")
	res, err := http.Post(ts.URL+"/api/reviews/upload", ct, body)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("上传无 token status = %d, 期望 401", res.StatusCode)
	}
}
