// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//
// M8-设置中心：LLM（复核/建议）与 E2B 云沙箱的运行时配置。
//
// 安全纪律：
//   - API Key 只落本机 SQLite（cr_settings），任何接口不回传明文，只回
//     key_set + 尾号提示（key_hint）；
//   - GET 公开（与任务详情同级的只读脱敏视图），POST 受全局 writeAuth 保护；
//   - /api/settings/test 会真实外呼 LLM（成本极低：max_tokens=1），额外包
//     IP 限流，防止被刷成出网代理；
//   - 服务端日志不打印任何 key。

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"code-review-agent/review"
	"code-review-agent/storage"
)

// llmProviders 前端服务商选项 → 默认 Base URL（openai 兼容协议）。
var llmProviders = map[string]string{
	"dashscope": "https://dashscope.aliyuncs.com/compatible-mode/v1",
	"openai":    "https://api.openai.com/v1",
	"ollama":    "http://localhost:11434/v1",
}

// llmSettingsView 一次审查生效所需的全部 LLM 配置（内部传递用）。
type llmSettings struct {
	Provider string
	BaseURL  string
	Model    string
	APIKey   string
	Source   string // "db" / "env" / ""（未配置）
}

// resolveLLMSettings 读取生效的 LLM 配置：DB 设置优先，key 回退 OPENAI_API_KEY 环境变量。
func (s *Server) resolveLLMSettings() (*llmSettings, error) {
	out := &llmSettings{}
	get := func(key string) (string, bool) {
		v, ok, err := s.store.GetSetting(key)
		if err != nil || !ok {
			return "", false
		}
		return v, true
	}
	if v, ok := get(storage.SettingLLMProvider); ok {
		out.Provider = v
	}
	if v, ok := get(storage.SettingLLMBaseURL); ok {
		out.BaseURL = v
	}
	if v, ok := get(storage.SettingLLMModel); ok {
		out.Model = v
	}
	if v, ok := get(storage.SettingLLMAPIKey); ok && v != "" {
		out.APIKey = v
		out.Source = "db"
	} else if osGetenv("OPENAI_API_KEY") != "" {
		out.APIKey = osGetenv("OPENAI_API_KEY")
		out.Source = "env"
	}
	return out, nil
}

// resolveE2BKey 生效的 E2B Key：DB 优先，回退环境变量。返回 (key, source)。
func (s *Server) resolveE2BKey() (string, string) {
	if v, ok, err := s.store.GetSetting(storage.SettingE2BAPIKey); err == nil && ok && v != "" {
		return v, "db"
	}
	if k := osGetenv("E2B_API_KEY"); k != "" {
		return k, "env"
	}
	return "", ""
}

// osGetenv 间接引用 os.Getenv，测试可注入（避免全局状态串扰）。
var osGetenv = os.Getenv

// injectSettings 把设置中心的生效配置注入审查选项：
// LLM key/base/model（key 来源 db > 环境变量）与 E2B key。
// 是否启用 LLM 复核（LLMMode）由调用方按请求参数决定。
func (s *Server) injectSettings(opts *review.Options) error {
	llm, err := s.resolveLLMSettings()
	if err != nil {
		return err
	}
	opts.LLMModelName = llm.Model
	opts.LLMBaseURL = llm.BaseURL
	opts.LLMAPIKey = llm.APIKey
	opts.E2BAPIKey, _ = s.resolveE2BKey()
	return nil
}

// settingsView 是 GET/POST /api/settings 的响应体（密钥只含脱敏提示）。
type settingsView struct {
	LLM struct {
		Provider  string `json:"provider"`
		BaseURL   string `json:"base_url"`
		Model     string `json:"model"`
		KeySet    bool   `json:"key_set"`
		KeyHint   string `json:"key_hint,omitempty"`
		KeySource string `json:"key_source"` // db / env / ""
		Ready     bool   `json:"ready"`      // 对新审查生效（有 key 且有 base_url/model）
	} `json:"llm"`
	E2B struct {
		KeySet    bool   `json:"key_set"`
		KeyHint   string `json:"key_hint,omitempty"`
		KeySource string `json:"key_source"`
	} `json:"e2b"`
	SandboxDefault string `json:"sandbox_default"` // 服务端默认沙箱后端（repo 审查不指定时用）
	AuthEnabled    bool   `json:"auth_enabled"`
}

func maskKey(k string) string {
	if k == "" {
		return ""
	}
	if len(k) <= 8 {
		return "••••"
	}
	return k[:4] + "••••" + k[len(k)-4:]
}

func (s *Server) buildSettingsView() (*settingsView, error) {
	llm, err := s.resolveLLMSettings()
	if err != nil {
		return nil, err
	}
	v := &settingsView{SandboxDefault: s.sandboxDefaultName(), AuthEnabled: s.cfg.AuthToken != ""}
	v.LLM.Provider = llm.Provider
	v.LLM.BaseURL = llm.BaseURL
	v.LLM.Model = llm.Model
	v.LLM.KeySet = llm.APIKey != ""
	v.LLM.KeyHint = maskKey(llm.APIKey)
	v.LLM.KeySource = llm.Source
	v.LLM.Ready = llm.APIKey != "" && llm.BaseURL != "" && llm.Model != ""
	e2bKey, e2bSrc := s.resolveE2BKey()
	v.E2B.KeySet = e2bKey != ""
	v.E2B.KeyHint = maskKey(e2bKey)
	v.E2B.KeySource = e2bSrc
	return v, nil
}

// sandboxDefaultName 服务默认沙箱后端（off 显示为 local 语义前的原名）。
func (s *Server) sandboxDefaultName() string {
	if s.cfg.SandboxMode == "" {
		return "off"
	}
	return s.cfg.SandboxMode
}

// handleGetSettings GET /api/settings（公开读；密钥只回脱敏提示）。
func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	v, err := s.buildSettingsView()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// saveSettingsRequest POST /api/settings 请求体。全部字段可选：
// 普通字段省略 = 保留现值，"-" = 清除；密钥字段省略/空 = 保留现值，"-" = 清除。
type saveSettingsRequest struct {
	Provider  *string `json:"llm_provider"`
	BaseURL   *string `json:"llm_base_url"`
	Model     *string `json:"llm_model"`
	LLMAPIKey *string `json:"llm_api_key"`
	E2BAPIKey *string `json:"e2b_api_key"`
}

// handleSaveSettings POST /api/settings（写操作，writeAuth 全局保护）。
func (s *Server) handleSaveSettings(w http.ResponseWriter, r *http.Request) {
	var req saveSettingsRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON: "+err.Error())
		return
	}

	// 校验 base_url（显式给了才校验）
	if req.BaseURL != nil && *req.BaseURL != "" && *req.BaseURL != "-" {
		u, err := url.Parse(*req.BaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			writeErr(w, http.StatusBadRequest, "Base URL 不合法：需要 http(s):// 开头的完整地址")
			return
		}
	}

	set := func(key, val string) {
		if err := s.store.SetSetting(key, val); err != nil {
			writeErr(w, http.StatusInternalServerError, "保存设置失败: "+err.Error())
			return
		}
	}
	del := func(key string) {
		if err := s.store.DeleteSetting(key); err != nil {
			writeErr(w, http.StatusInternalServerError, "清除设置失败: "+err.Error())
			return
		}
	}

	if req.Provider != nil {
		p := strings.TrimSpace(*req.Provider)
		if p == "-" {
			del(storage.SettingLLMProvider)
		} else if p != "" {
			if _, known := llmProviders[p]; !known && p != "custom" {
				writeErr(w, http.StatusBadRequest, "未知服务商: "+p+"（可选 dashscope/openai/ollama/custom）")
				return
			}
			set(storage.SettingLLMProvider, p)
		}
	}
	if req.BaseURL != nil {
		b := strings.TrimSpace(*req.BaseURL)
		if b == "-" || b == "" {
			del(storage.SettingLLMBaseURL)
		} else {
			set(storage.SettingLLMBaseURL, strings.TrimRight(b, "/"))
		}
	}
	if req.Model != nil {
		m := strings.TrimSpace(*req.Model)
		if m == "-" || m == "" {
			del(storage.SettingLLMModel)
		} else {
			set(storage.SettingLLMModel, m)
		}
	}
	if req.LLMAPIKey != nil {
		k := strings.TrimSpace(*req.LLMAPIKey)
		if k == "-" {
			del(storage.SettingLLMAPIKey)
		} else if k != "" {
			set(storage.SettingLLMAPIKey, k)
		}
	}
	if req.E2BAPIKey != nil {
		k := strings.TrimSpace(*req.E2BAPIKey)
		if k == "-" {
			del(storage.SettingE2BAPIKey)
		} else if k != "" {
			set(storage.SettingE2BAPIKey, k)
		}
	}

	v, err := s.buildSettingsView()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// testSettingsRequest POST /api/settings/test 请求体（全部可选，缺省用已保存值）。
type testSettingsRequest struct {
	BaseURL   string `json:"llm_base_url"`
	Model     string `json:"llm_model"`
	LLMAPIKey string `json:"llm_api_key"` // 测试专用 key（不落库）
}

// handleTestSettings POST /api/settings/test：发一次 max_tokens=1 的最小请求验证连通性。
// 成本约 1~2 个 token；响应把常见错误映射成用户可读提示。
func (s *Server) handleTestSettings(w http.ResponseWriter, r *http.Request) {
	var req testSettingsRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON: "+err.Error())
		return
	}
	llm, err := s.resolveLLMSettings()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	baseURL := firstNonEmpty(req.BaseURL, llm.BaseURL)
	model := firstNonEmpty(req.Model, llm.Model)
	apiKey := firstNonEmpty(req.LLMAPIKey, llm.APIKey)

	// 前置校验：不发请求就能给出的明确提示
	if baseURL == "" || model == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false, "code": "incomplete",
			"message": "配置不完整：至少需要 Base URL 和模型名（API Key 除本地 ollama 外必填）",
		})
		return
	}
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false, "code": "bad_url",
			"message": "Base URL 不合法：需要 http(s):// 开头的完整地址",
		})
		return
	}
	if apiKey == "" && !strings.Contains(u.Host, "localhost") && !strings.Contains(u.Host, "127.0.0.1") {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false, "code": "no_key",
			"message": "尚未配置 API Key：请在下方填入并保存后再测试",
		})
		return
	}

	start := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	ok, code, message := probeLLM(ctx, baseURL, model, apiKey, "")
	latency := time.Since(start).Milliseconds()

	resp := map[string]any{
		"ok": ok, "code": code, "message": message,
		"latency_ms": latency, "model": model,
	}
	writeJSON(w, http.StatusOK, resp)
}

// probeLLM 发一次最小化 chat 请求（max_tokens=1）。返回 (成功?, 错误码, 用户可读消息)。
// errHint 供测试注入自定义底层错误（可空）。
func probeLLM(ctx context.Context, baseURL, model, apiKey, errHint string) (bool, string, string) {
	if errHint != "" {
		return false, "injected", errHint
	}
	endpoint := strings.TrimRight(baseURL, "/") + "/chat/completions"
	payload := map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "user", "content": "ping"},
		},
		"max_tokens": 1, // 成本纪律：验证连通即可，1 个 token
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return false, "bad_request", "构造请求失败: " + err.Error()
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		// 超时 vs 连接失败分开提示
		if errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "Timeout") {
			return false, "timeout", "连接超时：检查 Base URL 是否可达、本机网络/代理是否正常"
		}
		return false, "unreachable", "无法连接到服务地址：检查 Base URL 拼写与网络连通性（" + shortErr(err) + "）"
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))

	switch resp.StatusCode {
	case http.StatusOK:
		return true, "ok", "连接成功，模型可用"
	case http.StatusUnauthorized:
		return false, "unauthorized", "API Key 无效或已过期（401）：核对 key 是否完整、是否属于该平台"
	case http.StatusForbidden:
		return false, "forbidden", "无权访问（403）：确认账号已开通该模型服务、key 未被封禁"
	case http.StatusNotFound:
		return false, "not_found", "接口或模型不存在（404）：确认 Base URL 以 /v1 结尾、模型名拼写正确"
	case http.StatusTooManyRequests:
		return false, "rate_limited", "触发限流（429）：key 欠费或请求过快，稍后再试"
	default:
		if resp.StatusCode == http.StatusBadRequest && bytes.Contains(raw, []byte("model")) {
			return false, "bad_model", "模型名可能不正确（400）：「" + model + "」被服务拒绝"
		}
		return false, "http_" + fmt.Sprint(resp.StatusCode),
			"服务返回异常（HTTP " + fmt.Sprint(resp.StatusCode) + "）： " + summarizeBody(raw)
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func shortErr(err error) string {
	s := err.Error()
	if len(s) > 80 {
		s = s[:80] + "…"
	}
	return s
}

func summarizeBody(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}
