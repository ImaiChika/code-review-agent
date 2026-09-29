// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"code-review-agent/storage"
)

// postJSON 小工具：POST JSON 并解析响应。
func postJSON(t *testing.T, url, body string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s 失败: %v", url, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

// getJSON 小工具：GET 并解析响应。
func getJSON(t *testing.T, url string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s 失败: %v", url, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

// profileNames 从 settings 视图取方案名列表。
func profileNames(v map[string]any) []string {
	ps, _ := v["profiles"].([]any)
	names := make([]string, 0, len(ps))
	for _, p := range ps {
		m, _ := p.(map[string]any)
		if s, ok := m["name"].(string); ok {
			names = append(names, s)
		}
	}
	return names
}

// TestProfilesSeedingAndMigration 首次读取预置 6 家出厂方案；
// 旧单配置（llm_* 键）迁移进对应出厂方案并设为当前；旧键删除。
func TestProfilesSeedingAndMigration(t *testing.T) {
	ts := newTestServer(t)
	const legacyKey = "sk-test-abcdef1234567890wxyz"

	// 模拟 v1 单配置（迁移前状态）
	for k, v := range map[string]string{
		storage.SettingLLMProvider: "dashscope",
		storage.SettingLLMBaseURL:  "https://dashscope.aliyuncs.com/compatible-mode/v1",
		storage.SettingLLMModel:    "qwen3.8-flash",
		storage.SettingLLMAPIKey:   legacyKey,
	} {
		if err := testServerStore.SetSetting(k, v); err != nil {
			t.Fatalf("种入旧配置失败: %v", err)
		}
	}

	code, v := getJSON(t, ts.URL+"/api/settings")
	if code != 200 {
		t.Fatalf("GET settings = %d", code)
	}
	ps, _ := v["profiles"].([]any)
	if len(ps) != 6 {
		t.Fatalf("出厂方案应 6 个, got %d: %v", len(ps), profileNames(v))
	}
	names := strings.Join(profileNames(v), ",")
	for _, want := range []string{"DeepSeek", "通义千问", "智谱 GLM", "Kimi（月之暗面）", "OpenAI", "MiMo（小米）"} {
		if !strings.Contains(names, want) {
			t.Errorf("缺少出厂方案 %q，实际: %s", want, names)
		}
	}
	if v["current_id"] != "factory-qwen" {
		t.Errorf("旧 dashscope 配置应迁移为 factory-qwen 并设为当前, got %v", v["current_id"])
	}
	// 千问方案带上迁移的 key（只回尾号）
	for _, p := range ps {
		m, _ := p.(map[string]any)
		if m["id"] == "factory-qwen" {
			if m["key_set"] != true {
				t.Errorf("factory-qwen 应带上迁移的 key: %v", m)
			}
			if hint, _ := m["key_hint"].(string); !strings.HasSuffix(hint, "wxyz") {
				t.Errorf("key_hint 应为尾号提示, got %q", hint)
			}
		}
	}
	// 旧键删除，e2b 保留
	if _, ok, _ := testServerStore.GetSetting(storage.SettingLLMAPIKey); ok {
		t.Error("迁移后旧 llm_api_key 应删除")
	}
	// 全响应不含明文 key
	rawAll := fmt.Sprintf("%v", v)
	if strings.Contains(rawAll, legacyKey) {
		t.Error("settings 响应泄漏明文 API Key")
	}
}

// TestProfilesCRUDLimit 方案上限 10 个：第 11 个创建被拒；删除后可再建。
func TestProfilesCRUDLimit(t *testing.T) {
	ts := newTestServer(t)
	create := func(name string) (int, map[string]any) {
		return postJSON(t, ts.URL+"/api/settings/profiles",
			`{"name":"`+name+`","provider":"custom","base_url":"https://example.com/v1","model":"m-1","llm_api_key":"sk-`+name+`-key-9999"}`)
	}

	// 出厂 6 + 新建 4 = 10
	for i := 1; i <= 4; i++ {
		code, _ := create(fmt.Sprintf("custom-%d", i))
		if code != 200 {
			t.Fatalf("第 %d 个自定义方案创建失败（应成功）", i)
		}
	}
	code, v := create("custom-11")
	if code != 400 {
		t.Fatalf("第 11 个方案应被拒绝 400, got %d", code)
	}
	if msg, _ := v["error"].(string); !strings.Contains(msg, "最多 10 个") {
		t.Errorf("拒绝提示应说明上限, got %q", msg)
	}
	if _, v2 := getJSON(t, ts.URL+"/api/settings"); len(profileNames(v2)) != 10 {
		t.Fatalf("被拒后方案数应保持 10, got %d", len(profileNames(v2)))
	}

	// 删除一个自定义方案 → 可再建
	_, v3 := getJSON(t, ts.URL+"/api/settings")
	ps, _ := v3["profiles"].([]any)
	var delID string
	for _, p := range ps {
		m, _ := p.(map[string]any)
		if m["name"] == "custom-4" {
			delID, _ = m["id"].(string)
		}
	}
	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/api/settings/profiles/"+delID, nil)
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if code, _ := create("custom-11-again"); code != 200 {
		t.Error("删除后应可再创建")
	}
}

// TestProfilesUpdateCurrentDelete 更新/设当前/删除的语义。
func TestProfilesUpdateCurrentDelete(t *testing.T) {
	ts := newTestServer(t)

	// 建一个自定义方案并带 key
	code, v := postJSON(t, ts.URL+"/api/settings/profiles",
		`{"name":"我的 DeepSeek","provider":"deepseek","base_url":"https://api.deepseek.com/v1","model":"deepseek-chat","llm_api_key":"sk-mykey-1234567890abcd"}`)
	if code != 200 {
		t.Fatalf("创建失败: %v", v)
	}
	var newID string
	ps, _ := v["profiles"].([]any)
	for _, p := range ps {
		m, _ := p.(map[string]any)
		if m["name"] == "我的 DeepSeek" {
			newID, _ = m["id"].(string)
		}
	}

	// 设为当前 → current_id 变化 + is_current 标记
	code, v = postJSON(t, ts.URL+"/api/settings/profiles/"+newID+"/current", `{}`)
	if code != 200 || v["current_id"] != newID {
		t.Fatalf("设当前失败: code=%d current=%v", code, v["current_id"])
	}
	// GET 不回明文
	if raw := fmt.Sprintf("%v", v); strings.Contains(raw, "sk-mykey-1234567890abcd") {
		t.Error("响应泄漏明文 key")
	}

	// 更新：改名 + "-" 清 key
	code, _ = postJSON(t, ts.URL+"/api/settings/profiles/"+newID,
		`{"name":"DeepSeek 备用","llm_api_key":"-"}`)
	if code != 200 {
		t.Fatalf("更新失败: %d", code)
	}
	_, v = getJSON(t, ts.URL+"/api/settings")
	for _, p := range v["profiles"].([]any) {
		m, _ := p.(map[string]any)
		if m["id"] == newID {
			if m["name"] != "DeepSeek 备用" {
				t.Errorf("改名未生效: %v", m["name"])
			}
			if m["key_set"] != false {
				t.Error("'-' 应清除 key")
			}
		}
	}

	// 删除当前方案 → current 置空
	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/api/settings/profiles/"+newID, nil)
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	_, v = getJSON(t, ts.URL+"/api/settings")
	if v["current_id"] != "" {
		t.Errorf("删除当前方案后 current 应置空, got %v", v["current_id"])
	}
}

// TestProfilesReset 重置出厂：自定义方案删除、同服务商 key 保留、当前延续。
func TestProfilesReset(t *testing.T) {
	ts := newTestServer(t)

	// 给出厂千问方案填 key 并设为当前；再加一个自定义方案
	_, v := getJSON(t, ts.URL+"/api/settings")
	postJSON(t, ts.URL+"/api/settings/profiles/factory-qwen",
		`{"llm_api_key":"sk-qwen-real-9876543210zz"}`)
	postJSON(t, ts.URL+"/api/settings/profiles/factory-qwen/current", `{}`)
	postJSON(t, ts.URL+"/api/settings/profiles",
		`{"name":"我的中转站","provider":"custom","base_url":"https://relay.example.com/v1","model":"any"}`)

	// 重置
	code, v := postJSON(t, ts.URL+"/api/settings/profiles/reset", `{}`)
	if code != 200 {
		t.Fatalf("重置失败: %d", code)
	}
	if got := len(profileNames(v)); got != 6 {
		t.Fatalf("重置后应 6 个出厂方案, got %d", got)
	}
	if v["current_id"] != "factory-qwen" {
		t.Errorf("出厂方案应延续当前身份, got %v", v["current_id"])
	}
	for _, p := range v["profiles"].([]any) {
		m, _ := p.(map[string]any)
		if m["id"] == "factory-qwen" && m["key_set"] != true {
			t.Error("重置应保留同服务商已填的 key")
		}
	}
	// 全量回归检查无明文
	if raw := fmt.Sprintf("%v", v); strings.Contains(raw, "sk-qwen-real") {
		t.Error("重置响应泄漏明文 key")
	}
}

// TestSettingsTestEndpointErrors 连接测试错误映射 + profile_id 回退（stub 上游）。
func TestSettingsTestEndpointErrors(t *testing.T) {
	ts := newTestServer(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if strings.HasSuffix(auth, "bad-key") {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"invalid api key"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"o"}}],"usage":{"total_tokens":1}}`))
	}))
	defer upstream.Close()

	callTest := func(payload string) map[string]any {
		code, out := postJSON(t, ts.URL+"/api/settings/test", payload)
		if code != 200 {
			t.Fatalf("test 应 200（业务错误在 body），got %d: %v", code, out)
		}
		return out
	}

	// 成功路径（显式值）
	ok := callTest(`{"llm_base_url":"` + upstream.URL + `","llm_model":"qwen3.8-flash","llm_api_key":"good-key"}`)
	if ok["ok"] != true || ok["code"] != "ok" {
		t.Errorf("应连接成功, got %v", ok)
	}
	// 401 映射
	bad := callTest(`{"llm_base_url":"` + upstream.URL + `","llm_model":"qwen3.8-flash","llm_api_key":"bad-key"}`)
	if bad["ok"] != false || bad["code"] != "unauthorized" {
		t.Errorf("401 应映射 unauthorized, got %v", bad)
	}
	// 缺配置
	inc := callTest(`{}`)
	if inc["code"] != "incomplete" {
		t.Errorf("缺配置应 incomplete, got %v", inc)
	}
	// profile_id 回退：保存方案后不带显式值测试
	postJSON(t, ts.URL+"/api/settings/profiles",
		`{"name":"stub 方案","provider":"custom","base_url":"`+upstream.URL+`","model":"qwen3.8-flash","llm_api_key":"good-key"}`)
	_, v := getJSON(t, ts.URL+"/api/settings")
	var pid string
	for _, p := range v["profiles"].([]any) {
		m, _ := p.(map[string]any)
		if m["name"] == "stub 方案" {
			pid, _ = m["id"].(string)
		}
	}
	viaProfile := callTest(`{"profile_id":"` + pid + `"}`)
	if viaProfile["ok"] != true {
		t.Errorf("按方案测试应成功, got %v", viaProfile)
	}
}

// TestTaskNamePlumbing 任务名称透传：JSON 入参 → 任务行；超长截断到 80 字。
func TestTaskNamePlumbing(t *testing.T) {
	ts := newTestServer(t)
	long := strings.Repeat("名", 100)
	body := `{"diff_content":"--- a/a.go\n+++ b/a.go\n@@ -1 +1,2 @@\n package a\n+var x = 1\n","task_name":"` + long + `"}`
	resp, _ := http.Post(ts.URL+"/api/reviews", "application/json", strings.NewReader(body))
	var out struct {
		TaskID string `json:"task_id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if out.TaskID == "" {
		t.Fatal("应入队成功")
	}
	// 等队列跑完
	var got string
	for i := 0; i < 40; i++ {
		time.Sleep(100 * time.Millisecond)
		_, v := getJSON(t, ts.URL+"/api/tasks/"+out.TaskID)
		task, _ := v["task"].(map[string]any)
		if task == nil {
			continue
		}
		if task["status"] == "completed" || task["status"] == "failed" {
			got, _ = task["task_name"].(string)
			break
		}
	}
	if runes := []rune(got); len(runes) != 80 {
		t.Errorf("task_name 应截断到 80 字, got %d", len(runes))
	}
}
