// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"code-review-agent/storage"
)

// TestSettingsCRUDAndMasking 设置中心核心安全语义：
// 保存生效、GET 永不回明文（只有尾号提示）、"-" 清除。
func TestSettingsCRUDAndMasking(t *testing.T) {
	ts := newTestServer(t)
	const key = "sk-test-abcdef1234567890wxyz"

	// 1) 保存（带 key）
	body := `{"llm_provider":"dashscope","llm_base_url":"https://example.com/v1/","llm_model":"qwen3.8-flash","llm_api_key":"` + key + `","e2b_api_key":"e2b-key-99887766"}`
	resp, err := http.Post(ts.URL+"/api/settings", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST 失败: %v", err)
	}
	var saved struct {
		LLM struct {
			Provider string `json:"provider"`
			BaseURL  string `json:"base_url"`
			Model    string `json:"model"`
			KeySet   bool   `json:"key_set"`
			KeyHint  string `json:"key_hint"`
			Ready    bool   `json:"ready"`
		} `json:"llm"`
		E2B struct {
			KeySet bool   `json:"key_set"`
			Hint   string `json:"key_hint"`
		} `json:"e2b"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&saved)
	resp.Body.Close()
	if saved.LLM.Provider != "dashscope" || saved.LLM.Model != "qwen3.8-flash" {
		t.Fatalf("保存回显错误: %+v", saved.LLM)
	}
	if !strings.HasSuffix(saved.LLM.BaseURL, "/v1") {
		t.Errorf("Base URL 应去掉尾斜杠, got %q", saved.LLM.BaseURL)
	}
	if !saved.LLM.Ready || !saved.E2B.KeySet {
		t.Errorf("保存后应为 ready/key_set: %+v", saved)
	}
	if strings.Contains(saved.LLM.KeyHint, "abcdef123456") {
		t.Errorf("key_hint 泄漏了明文片段: %q", saved.LLM.KeyHint)
	}

	// 2) GET 不回明文
	resp2, _ := http.Get(ts.URL + "/api/settings")
	raw2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if strings.Contains(string(raw2), key) {
		t.Error("GET /api/settings 泄漏了明文 API Key")
	}
	var got settingsView
	if err := json.Unmarshal(raw2, &got); err != nil {
		t.Fatalf("GET 响应不是合法 settingsView: %v", err)
	}
	if !got.LLM.KeySet || !got.E2B.KeySet {
		t.Errorf("GET 应 key_set=true: llm=%v e2b=%v", got.LLM.KeySet, got.E2B.KeySet)
	}

	// 3) "-" 清除 key（其余字段保留）
	resp3, err := http.Post(ts.URL+"/api/settings", "application/json",
		strings.NewReader(`{"llm_api_key":"-"}`))
	if err != nil {
		t.Fatalf("清除请求失败: %v", err)
	}
	resp3.Body.Close()
	if v, ok, _ := testServerStore.GetSetting(storage.SettingLLMAPIKey); ok && v != "" {
		t.Error("'-' 应清除 llm_api_key")
	}
	if _, ok, _ := testServerStore.GetSetting(storage.SettingLLMModel); !ok {
		t.Error("清除 key 不应影响模型名")
	}

	// 4) 非法 base_url 被拒绝
	resp4, _ := http.Post(ts.URL+"/api/settings", "application/json",
		strings.NewReader(`{"llm_base_url":"ftp://bad"}`))
	if resp4.StatusCode != http.StatusBadRequest {
		t.Errorf("非法 base_url 应 400, got %d", resp4.StatusCode)
	}
	resp4.Body.Close()
}

// TestSettingsTestEndpointErrors 连接测试的错误映射（stub 上游返回 401/200）。
func TestSettingsTestEndpointErrors(t *testing.T) {
	ts := newTestServer(t)

	// stub 上游：/chat/completions 按 path 前缀的 token 决定返回 200 或 401
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
		resp, err := http.Post(ts.URL+"/api/settings/test", "application/json", strings.NewReader(payload))
		if err != nil {
			t.Fatalf("test 请求失败: %v", err)
		}
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		return out
	}

	// 成功路径
	ok := callTest(`{"llm_base_url":"` + upstream.URL + `","llm_model":"qwen3.8-flash","llm_api_key":"good-key"}`)
	if ok["ok"] != true || ok["code"] != "ok" {
		t.Errorf("应连接成功, got %v", ok)
	}
	// 401 → unauthorized + 用户可读提示
	bad := callTest(`{"llm_base_url":"` + upstream.URL + `","llm_model":"qwen3.8-flash","llm_api_key":"bad-key"}`)
	if bad["ok"] != false || bad["code"] != "unauthorized" {
		t.Errorf("401 应映射 unauthorized, got %v", bad)
	}
	if s, _ := bad["message"].(string); !strings.Contains(s, "API Key 无效") {
		t.Errorf("401 提示应可读, got %q", s)
	}
	// 不存在的主机 → unreachable/timeout，不出现裸 err 文本
	unreach := callTest(`{"llm_base_url":"http://127.0.0.1:1/v1","llm_model":"m","llm_api_key":"k"}`)
	if unreach["ok"] != false {
		t.Errorf("不可达应失败, got %v", unreach)
	}
	// 缺配置 → incomplete
	inc := callTest(`{"llm_base_url":"","llm_model":""}`)
	if inc["code"] != "incomplete" {
		t.Errorf("缺配置应 incomplete, got %v", inc)
	}
}

// TestSandboxBackendValidation sandbox_backend 白名单与 e2b key 预检。
func TestSandboxBackendValidation(t *testing.T) {
	ts := newTestServer(t)

	// 非法后端 → 400
	resp, _ := http.Post(ts.URL+"/api/reviews", "application/json",
		strings.NewReader(`{"repo_path":"/tmp","sandbox":true,"sandbox_backend":"docker --privileged"}`))
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("非法 sandbox_backend 应 400, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// e2b 但无 key → 400 + 明确提示
	resp2, _ := http.Post(ts.URL+"/api/reviews", "application/json",
		strings.NewReader(`{"repo_path":"/tmp","sandbox":true,"sandbox_backend":"e2b"}`))
	if resp2.StatusCode != http.StatusBadRequest {
		t.Errorf("e2b 无 key 应 400, got %d", resp2.StatusCode)
	}
	raw, _ := io2ReadAll(resp2)
	resp2.Body.Close()
	if !strings.Contains(raw, "E2B API Key") {
		t.Errorf("提示应引导去设置中心, got %s", raw)
	}
}

// io2ReadAll 小工具避免引入 io 别名混乱。
func io2ReadAll(resp *http.Response) (string, error) {
	buf := make([]byte, 2048)
	n, _ := resp.Body.Read(buf)
	return string(buf[:n]), nil
}
