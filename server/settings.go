// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//
// M8-设置中心 v2：多方案 LLM 配置（最多 10 个，出厂预置 6 家）+ E2B 云沙箱配置。
//
// 方案模型：
//   - 每个方案 = 自定义名称 + 服务商标签 + Base URL + 模型名 + API Key；
//     方案不以服务商区分——同一服务商可以有多个不同命名的方案；
//   - 「当前方案」持久化在服务端（cr_settings.llm_current），重新打开页面/重新
//     登录后设置页优先展示它，审查管线也按它注入；
//   - 首次读取时懒迁移：旧单配置（llm_provider/llm_base_url/llm_model/llm_api_key）
//     合并进对应出厂方案并设为当前，随后删除旧键，避免双真相源。
//
// 安全纪律（与 v1 相同）：
//   - API Key 只落本机 SQLite（cr_settings），任何接口不回传明文，只回
//     key_set + 尾号提示（key_hint）；
//   - GET 公开（脱敏视图），写操作受全局 writeAuth 保护；
//   - /api/settings/test 真实外呼 LLM（max_tokens=1），额外包 IP 限流；
//   - 服务端日志不打印任何 key。

package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
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

// maxLLMProfiles 方案数量上限（用户约定：最多 10 个）。
const maxLLMProfiles = 10

// llmProviders 服务商标签 → 默认 Base URL（OpenAI 兼容协议）。
// 厂商端点核对时间：2026-09-30（MiMo 为小米官方开放平台 mimo.mi.com）。
var llmProviders = map[string]string{
	"deepseek":  "https://api.deepseek.com/v1",
	"dashscope": "https://dashscope.aliyuncs.com/compatible-mode/v1",
	"glm":       "https://open.bigmodel.cn/api/paas/v4",
	"kimi":      "https://api.moonshot.cn/v1",
	"openai":    "https://api.openai.com/v1",
	"mimo":      "https://api.xiaomimimo.com/v1",
	"ollama":    "http://localhost:11434/v1",
}

// LLMProfile 一个可切换的模型配置方案。
type LLMProfile struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Provider  string `json:"provider"`
	BaseURL   string `json:"base_url"`
	Model     string `json:"model"`
	APIKey    string `json:"-"` // 敏感：HTTP 视图永不序列化外发
	CreatedAt string `json:"created_at"`
}

// storedLLMProfile 方案的存储形态（cr_settings.llm_profiles JSON 数组的元素）。
// 与 LLMProfile 字段一致但 APIKey 带存储标签——json:"-" 会导致整包序列化时
// 密钥被静默丢弃（单测 TestProfilesUpdateCurrentDelete 抓到的真实缺陷）。
type storedLLMProfile struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Provider  string `json:"provider"`
	BaseURL   string `json:"base_url"`
	Model     string `json:"model"`
	APIKey    string `json:"api_key"`
	CreatedAt string `json:"created_at"`
}

func toStored(p []LLMProfile) []storedLLMProfile {
	out := make([]storedLLMProfile, len(p))
	for i, v := range p {
		out[i] = storedLLMProfile{v.ID, v.Name, v.Provider, v.BaseURL, v.Model, v.APIKey, v.CreatedAt}
	}
	return out
}

func fromStored(p []storedLLMProfile) []LLMProfile {
	out := make([]LLMProfile, len(p))
	for i, v := range p {
		out[i] = LLMProfile{v.ID, v.Name, v.Provider, v.BaseURL, v.Model, v.APIKey, v.CreatedAt}
	}
	return out
}

// factoryLLMProfiles 出厂预置方案（用户 2026-09-30 指定 6 家）。
// 出厂 ID 稳定：重置后保持同一 ID，"当前方案"指向出厂方案时可以延续。
var factoryLLMProfiles = []LLMProfile{
	{ID: "factory-deepseek", Name: "DeepSeek", Provider: "deepseek", BaseURL: "https://api.deepseek.com/v1", Model: "deepseek-chat"},
	{ID: "factory-qwen", Name: "通义千问", Provider: "dashscope", BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1", Model: "qwen3.8-flash"},
	{ID: "factory-glm", Name: "智谱 GLM", Provider: "glm", BaseURL: "https://open.bigmodel.cn/api/paas/v4", Model: "glm-4.5-flash"},
	{ID: "factory-kimi", Name: "Kimi（月之暗面）", Provider: "kimi", BaseURL: "https://api.moonshot.cn/v1", Model: "moonshot-v1-8k"},
	{ID: "factory-openai", Name: "OpenAI", Provider: "openai", BaseURL: "https://api.openai.com/v1", Model: "gpt-4o-mini"},
	{ID: "factory-mimo", Name: "MiMo（小米）", Provider: "mimo", BaseURL: "https://api.xiaomimimo.com/v1", Model: "mimo-7b"},
}

// ========== 方案存取（cr_settings 内两个键：llm_profiles + llm_current） ==========

// loadLLMProfiles 读取全部方案与当前方案 ID；首次读取时执行出厂预置与旧配置迁移。
func (s *Server) loadLLMProfiles() ([]LLMProfile, string, error) {
	raw, ok, err := s.store.GetSetting(storage.SettingLLMProfiles)
	if err != nil {
		return nil, "", err
	}
	if ok && raw != "" {
		var stored []storedLLMProfile
		if err := json.Unmarshal([]byte(raw), &stored); err != nil {
			// 方案数据损坏时不静默清空：报错让人修，而不是把 key 抹掉
			return nil, "", fmt.Errorf("方案数据损坏（llm_profiles 不是合法 JSON）: %w", err)
		}
		current, _, _ := s.store.GetSetting(storage.SettingLLMCurrent)
		return fromStored(stored), current, nil
	}

	// 首次：出厂预置 + 旧单配置迁移
	profiles := make([]LLMProfile, len(factoryLLMProfiles))
	copy(profiles, factoryLLMProfiles)
	now := time.Now().Format(time.RFC3339)
	for i := range profiles {
		profiles[i].CreatedAt = now
	}

	current := ""
	legacy := map[string]string{}
	for _, k := range []string{storage.SettingLLMProvider, storage.SettingLLMBaseURL, storage.SettingLLMModel, storage.SettingLLMAPIKey} {
		if v, ok, err := s.store.GetSetting(k); err == nil && ok && v != "" {
			legacy[k] = v
		}
	}
	if len(legacy) > 0 {
		target := -1
		if p, ok := legacy[storage.SettingLLMProvider]; ok {
			for i := range profiles {
				if profiles[i].Provider == p {
					target = i
					break
				}
			}
		}
		if target < 0 {
			// 旧配置不属于任何出厂服务商 → 追加为独立方案
			profiles = append(profiles, LLMProfile{ID: "prof-migrated", Name: "旧配置（迁移）", CreatedAt: now})
			target = len(profiles) - 1
		}
		if v, ok := legacy[storage.SettingLLMBaseURL]; ok {
			profiles[target].BaseURL = v
		}
		if v, ok := legacy[storage.SettingLLMModel]; ok {
			profiles[target].Model = v
		}
		if v, ok := legacy[storage.SettingLLMAPIKey]; ok {
			profiles[target].APIKey = v
		}
		current = profiles[target].ID
	}

	if err := s.saveLLMProfiles(profiles, current); err != nil {
		return nil, "", err
	}
	// 迁移完成，删除旧键（e2b_api_key 不属于 LLM 方案，保留）
	for _, k := range []string{storage.SettingLLMProvider, storage.SettingLLMBaseURL, storage.SettingLLMModel, storage.SettingLLMAPIKey} {
		_ = s.store.DeleteSetting(k)
	}
	return profiles, current, nil
}

// saveLLMProfiles 写回方案列表与当前方案。
func (s *Server) saveLLMProfiles(profiles []LLMProfile, currentID string) error {
	raw, err := json.Marshal(toStored(profiles))
	if err != nil {
		return err
	}
	if err := s.store.SetSetting(storage.SettingLLMProfiles, string(raw)); err != nil {
		return err
	}
	return s.store.SetSetting(storage.SettingLLMCurrent, currentID)
}

// findProfile 按 ID 查方案。
func findProfile(profiles []LLMProfile, id string) *LLMProfile {
	for i := range profiles {
		if profiles[i].ID == id {
			return &profiles[i]
		}
	}
	return nil
}

// injectSettings 把设置中心的生效配置注入审查选项：
// 当前方案的 LLM key/base/model（key 来源 db > 环境变量）与 E2B key。
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

// llmSettingsView 一次审查生效所需的全部 LLM 配置（内部传递用）。
type llmSettings struct {
	Provider string
	BaseURL  string
	Model    string
	APIKey   string
	Source   string // "db"（当前方案）/ "env"（环境变量）/ ""（未配置）
}

// resolveLLMSettings 读取生效的 LLM 配置：当前方案优先，key 回退 OPENAI_API_KEY 环境变量。
func (s *Server) resolveLLMSettings() (*llmSettings, error) {
	out := &llmSettings{}
	profiles, currentID, err := s.loadLLMProfiles()
	if err == nil {
		if p := findProfile(profiles, currentID); p != nil {
			out.Provider = p.Provider
			out.BaseURL = p.BaseURL
			if out.BaseURL == "" {
				out.BaseURL = llmProviders[p.Provider] // 方案缺省端点时用服务商默认
			}
			out.Model = p.Model
			if p.APIKey != "" {
				out.APIKey = p.APIKey
				out.Source = "db"
				return out, nil
			}
		}
	}
	if k := osGetenv("OPENAI_API_KEY"); k != "" {
		out.APIKey = k
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

// ========== HTTP 视图与处理器 ==========

// profileView 方案的对外脱敏视图。
type profileView struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Provider  string `json:"provider"`
	BaseURL   string `json:"base_url"`
	Model     string `json:"model"`
	KeySet    bool   `json:"key_set"`
	KeyHint   string `json:"key_hint,omitempty"`
	IsCurrent bool   `json:"is_current"`
	BuiltIn   bool   `json:"built_in"` // 出厂预置（仍可编辑/删除，仅用于展示标识）
}

// settingsView 是 GET /api/settings 的响应体（密钥只含脱敏提示）。
type settingsView struct {
	Profiles  []profileView `json:"profiles"`
	CurrentID string        `json:"current_id"`
	LLM       struct {
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
	MaxProfiles    int    `json:"max_profiles"`
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
	profiles, currentID, err := s.loadLLMProfiles()
	if err != nil {
		return nil, err
	}
	v := &settingsView{
		Profiles:       make([]profileView, 0, len(profiles)),
		CurrentID:      currentID,
		SandboxDefault: s.sandboxDefaultName(),
		AuthEnabled:    s.cfg.AuthToken != "",
		MaxProfiles:    maxLLMProfiles,
	}
	for _, p := range profiles {
		v.Profiles = append(v.Profiles, profileView{
			ID: p.ID, Name: p.Name, Provider: p.Provider, BaseURL: p.BaseURL, Model: p.Model,
			KeySet: p.APIKey != "", KeyHint: maskKey(p.APIKey),
			IsCurrent: p.ID == currentID,
			BuiltIn:   strings.HasPrefix(p.ID, "factory-"),
		})
	}
	llm, err := s.resolveLLMSettings()
	if err != nil {
		return nil, err
	}
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

// saveSettingsRequest POST /api/settings 请求体（v2 仅剩 E2B key；LLM 配置走 profiles）。
type saveSettingsRequest struct {
	E2BAPIKey *string `json:"e2b_api_key"`
}

// handleSaveSettings POST /api/settings（写操作，writeAuth 全局保护）。
func (s *Server) handleSaveSettings(w http.ResponseWriter, r *http.Request) {
	var req saveSettingsRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON: "+err.Error())
		return
	}
	if req.E2BAPIKey != nil {
		k := strings.TrimSpace(*req.E2BAPIKey)
		if k == "-" {
			if err := s.store.DeleteSetting(storage.SettingE2BAPIKey); err != nil {
				writeErr(w, http.StatusInternalServerError, "清除设置失败: "+err.Error())
				return
			}
		} else if k != "" {
			if err := s.store.SetSetting(storage.SettingE2BAPIKey, k); err != nil {
				writeErr(w, http.StatusInternalServerError, "保存设置失败: "+err.Error())
				return
			}
		}
	}
	v, err := s.buildSettingsView()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// ========== 方案 CRUD ==========

// profileWriteRequest 方案创建/更新请求体。api_key：空 = 保留现值，"-" = 清除。
type profileWriteRequest struct {
	Name      *string `json:"name"`
	Provider  *string `json:"provider"`
	BaseURL   *string `json:"base_url"`
	Model     *string `json:"model"`
	LLMAPIKey *string `json:"llm_api_key"`
}

// validateProfileInput 创建/更新共用的字段校验，返回清洗后的值。
func validateProfileInput(name, provider, baseURL, model string) (string, string, string, string, error) {
	name = strings.TrimSpace(name)
	provider = strings.TrimSpace(provider)
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	model = strings.TrimSpace(model)
	if name == "" {
		return "", "", "", "", errors.New("方案名称不能为空")
	}
	if len(name) > 40 {
		return "", "", "", "", errors.New("方案名称过长（最多 40 字）")
	}
	if _, known := llmProviders[provider]; !known && provider != "custom" {
		return "", "", "", "", fmt.Errorf("未知服务商: %s（可选 deepseek/dashscope/glm/kimi/openai/mimo/ollama/custom）", provider)
	}
	if baseURL != "" {
		u, err := url.Parse(baseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return "", "", "", "", errors.New("Base URL 不合法：需要 http(s):// 开头的完整地址")
		}
	}
	if model == "" {
		return "", "", "", "", errors.New("模型名不能为空")
	}
	return name, provider, baseURL, model, nil
}

// handleCreateProfile POST /api/settings/profiles——新建方案。
// 已满 maxLLMProfiles 时 400，message 前端弹窗展示。
func (s *Server) handleCreateProfile(w http.ResponseWriter, r *http.Request) {
	var req profileWriteRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON: "+err.Error())
		return
	}
	profiles, currentID, err := s.loadLLMProfiles()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(profiles) >= maxLLMProfiles {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("最多 %d 个配置方案（当前已满）：请先删除不用的方案再添加", maxLLMProfiles))
		return
	}
	var name, provider, baseURL, model string
	if req.Name != nil {
		name = *req.Name
	}
	if req.Provider != nil {
		provider = *req.Provider
	}
	if req.BaseURL != nil {
		baseURL = *req.BaseURL
	}
	if req.Model != nil {
		model = *req.Model
	}
	name, provider, baseURL, model, err = validateProfileInput(name, provider, baseURL, model)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	p := LLMProfile{
		ID:        "prof-" + randHex4(),
		Name:      name,
		Provider:  provider,
		BaseURL:   baseURL,
		Model:     model,
		CreatedAt: time.Now().Format(time.RFC3339),
	}
	if req.LLMAPIKey != nil {
		if k := strings.TrimSpace(*req.LLMAPIKey); k != "" && k != "-" {
			p.APIKey = k
		}
	}
	profiles = append(profiles, p)
	if currentID == "" {
		currentID = p.ID // 第一个方案自动成为当前方案
	}
	if err := s.saveLLMProfiles(profiles, currentID); err != nil {
		writeErr(w, http.StatusInternalServerError, "保存方案失败: "+err.Error())
		return
	}
	v, err := s.buildSettingsView()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// handleProfileRoutes /api/settings/profiles/ 子树：{id}（POST 更新 / DELETE 删除）、
// {id}/current（POST 设当前）、reset（POST 重置出厂）。
func (s *Server) handleProfileRoutes(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/settings/profiles/")
	rest = strings.Trim(rest, "/")
	switch {
	case rest == "reset" && r.Method == http.MethodPost:
		s.handleResetProfiles(w, r)
	case strings.HasSuffix(rest, "/current") && r.Method == http.MethodPost:
		s.handleSetCurrentProfile(w, r, strings.TrimSuffix(rest, "/current"))
	case rest != "" && r.Method == http.MethodPost:
		s.handleUpdateProfile(w, r, rest)
	case rest != "" && r.Method == http.MethodDelete:
		s.handleDeleteProfile(w, r, rest)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "不支持的请求")
	}
}

// handleUpdateProfile POST /api/settings/profiles/{id}——更新方案字段。
func (s *Server) handleUpdateProfile(w http.ResponseWriter, r *http.Request, id string) {
	var req profileWriteRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON: "+err.Error())
		return
	}
	profiles, currentID, err := s.loadLLMProfiles()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	p := findProfile(profiles, id)
	if p == nil {
		writeErr(w, http.StatusNotFound, "方案不存在（可能已被删除）")
		return
	}
	if req.Name != nil || req.Provider != nil || req.BaseURL != nil || req.Model != nil {
		// 只覆盖显式给出的字段
		name, provider, baseURL, model := p.Name, p.Provider, p.BaseURL, p.Model
		if req.Name != nil {
			name = *req.Name
		}
		if req.Provider != nil {
			provider = *req.Provider
		}
		if req.BaseURL != nil {
			baseURL = *req.BaseURL
		}
		if req.Model != nil {
			model = *req.Model
		}
		name, provider, baseURL, model, err = validateProfileInput(name, provider, baseURL, model)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		p.Name, p.Provider, p.BaseURL, p.Model = name, provider, baseURL, model
	}
	if req.LLMAPIKey != nil {
		k := strings.TrimSpace(*req.LLMAPIKey)
		switch {
		case k == "-":
			p.APIKey = ""
		case k != "":
			p.APIKey = k
		}
	}
	if err := s.saveLLMProfiles(profiles, currentID); err != nil {
		writeErr(w, http.StatusInternalServerError, "保存方案失败: "+err.Error())
		return
	}
	v, err := s.buildSettingsView()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// handleSetCurrentProfile POST /api/settings/profiles/{id}/current——设为当前方案。
func (s *Server) handleSetCurrentProfile(w http.ResponseWriter, r *http.Request, id string) {
	profiles, _, err := s.loadLLMProfiles()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if findProfile(profiles, id) == nil {
		writeErr(w, http.StatusNotFound, "方案不存在（可能已被删除）")
		return
	}
	if err := s.saveLLMProfiles(profiles, id); err != nil {
		writeErr(w, http.StatusInternalServerError, "保存失败: "+err.Error())
		return
	}
	v, err := s.buildSettingsView()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// handleDeleteProfile DELETE /api/settings/profiles/{id}——删除方案（密钥一并清除）。
// 删除当前方案后"当前"置空，需要重新选择（明确语义优于自动顺延）。
func (s *Server) handleDeleteProfile(w http.ResponseWriter, r *http.Request, id string) {
	profiles, currentID, err := s.loadLLMProfiles()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	idx := -1
	for i := range profiles {
		if profiles[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		writeErr(w, http.StatusNotFound, "方案不存在（可能已被删除）")
		return
	}
	profiles = append(profiles[:idx], profiles[idx+1:]...)
	if currentID == id {
		currentID = ""
	}
	if err := s.saveLLMProfiles(profiles, currentID); err != nil {
		writeErr(w, http.StatusInternalServerError, "删除失败: "+err.Error())
		return
	}
	v, err := s.buildSettingsView()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// handleResetProfiles POST /api/settings/profiles/reset——重置为出厂方案。
// 同服务商已填的密钥会保留（迁移到对应出厂方案）；自定义方案删除（密钥随之清除）。
func (s *Server) handleResetProfiles(w http.ResponseWriter, r *http.Request) {
	old, currentID, err := s.loadLLMProfiles()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	keyByProvider := map[string]string{}
	for _, p := range old {
		if p.APIKey != "" {
			keyByProvider[p.Provider] = p.APIKey // 同服务商多个方案时取最后一个
		}
	}
	profiles := make([]LLMProfile, len(factoryLLMProfiles))
	copy(profiles, factoryLLMProfiles)
	now := time.Now().Format(time.RFC3339)
	for i := range profiles {
		profiles[i].CreatedAt = now
		if k, ok := keyByProvider[profiles[i].Provider]; ok {
			profiles[i].APIKey = k
		}
	}
	// 旧"当前"若指向出厂方案（ID 稳定）则延续，否则置空待重选
	if findProfile(profiles, currentID) == nil {
		currentID = ""
	}
	if err := s.saveLLMProfiles(profiles, currentID); err != nil {
		writeErr(w, http.StatusInternalServerError, "重置失败: "+err.Error())
		return
	}
	v, err := s.buildSettingsView()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// testSettingsRequest POST /api/settings/test 请求体（全部可选）。
// profile_id：测试某个已保存方案（缺省测当前方案）；显式字段优先于方案值。
type testSettingsRequest struct {
	ProfileID string `json:"profile_id"`
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
	var fb llmSettings
	if req.ProfileID != "" {
		profiles, _, err := s.loadLLMProfiles()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if p := findProfile(profiles, req.ProfileID); p != nil {
			fb = llmSettings{Provider: p.Provider, BaseURL: p.BaseURL, Model: p.Model, APIKey: p.APIKey, Source: "db"}
			if fb.BaseURL == "" {
				fb.BaseURL = llmProviders[p.Provider]
			}
		} else {
			writeErr(w, http.StatusNotFound, "要测试的方案不存在（可能已被删除）")
			return
		}
	} else {
		llm, err := s.resolveLLMSettings()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		fb = *llm
	}
	baseURL := firstNonEmpty(req.BaseURL, fb.BaseURL)
	model := firstNonEmpty(req.Model, fb.Model)
	apiKey := firstNonEmpty(req.LLMAPIKey, fb.APIKey)

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
			"message": "该方案还没有 API Key：请在下方填入并保存后再测试",
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

// randHex4 生成 4 字节随机 hex（方案 ID 后缀用）。
func randHex4() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano()%0xffff)
	}
	return hex.EncodeToString(b)
}
