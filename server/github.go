// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//
// Package server — M7-F3 GitHub PR 输入。
//
// 用户粘贴 PR 链接（github.com/{owner}/{repo}/pull/{n}），服务端经
// GitHub API 拉取该 PR 的 unified diff（Accept: application/vnd.github.v3.diff），
// 走与"粘贴 diff"完全相同的审查管线。公开仓库无需凭证；
// 设置 GITHUB_TOKEN 环境变量可提升 API 限速并访问私有仓库。
package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// githubAPIBase GitHub API 基地址（Config 可覆盖，测试注入假服务）。
// 环境 GITHUB_API_BASE 优先（自建/GHE 实例），其次 Config，最后官方地址。
func (s *Server) githubAPIBase() string {
	if v := os.Getenv("GITHUB_API_BASE"); v != "" {
		return strings.TrimRight(v, "/")
	}
	if s.cfg.GitHubAPIBase != "" {
		return strings.TrimRight(s.cfg.GitHubAPIBase, "/")
	}
	return "https://api.github.com"
}

// prRef 解析出的 PR 定位。
type prRef struct {
	Owner  string
	Repo   string
	Number string
}

var (
	// prURLPattern 匹配 host 剥离后的 path：{owner}/{repo}/pull/{n}
	//（host 已由 url.Parse 单独校验，见 parsePRURL）
	prURLPattern = regexp.MustCompile(`^([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)/pull/(\d+)/?$`)
	// errBadPRURL 无法识别的 PR 链接
	errBadPRURL = errors.New("无法识别的 GitHub PR 链接（期望形如 https://github.com/{owner}/{repo}/pull/123）")
)

// parsePRURL 从用户输入提取 owner/repo/number；支持裸 "owner/repo#123" 简写
// 与无 scheme 的链接（自动补 https://）。
func parsePRURL(raw string) (prRef, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return prRef{}, errBadPRURL
	}

	// 简写：owner/repo#123
	if m := regexp.MustCompile(`^([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)#(\d+)$`).FindStringSubmatch(raw); m != nil {
		return prRef{Owner: m[1], Repo: m[2], Number: m[3]}, nil
	}

	// 无 scheme 的链接补全，否则 url.Parse 会把 host 吃进 path
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}

	// 完整链接：解析后按 host+path 匹配，防正则被特制 URL 绕过
	u, err := url.Parse(raw)
	if err != nil {
		return prRef{}, errBadPRURL
	}
	host := strings.TrimPrefix(strings.ToLower(u.Host), "www.")
	if host != "github.com" {
		return prRef{}, errBadPRURL
	}
	m := prURLPattern.FindStringSubmatch(strings.Trim(u.Path, "/"))
	if m == nil {
		return prRef{}, errBadPRURL
	}
	return prRef{Owner: m[1], Repo: m[2], Number: m[3]}, nil
}

// fetchPRDiff 拉取 PR 的 unified diff 文本。
// 公开仓库无需 token；GITHUB_TOKEN 存在时附带（提升限速 / 私有仓库）。
func (s *Server) fetchPRDiff(ctx context.Context, ref prRef) (string, error) {
	api := s.githubAPIBase() + "/repos/" + ref.Owner + "/" + ref.Repo + "/pulls/" + ref.Number
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api, nil)
	if err != nil {
		return "", fmt.Errorf("构造 PR 请求失败: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github.v3.diff")
	req.Header.Set("User-Agent", "code-review-agent")
	if t := os.Getenv("GITHUB_TOKEN"); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("请求 GitHub API 失败: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		// fallthrough 到读取
	case http.StatusNotFound:
		return "", fmt.Errorf("PR 不存在或无权访问（%s/%s#%s）", ref.Owner, ref.Repo, ref.Number)
	case http.StatusForbidden, http.StatusTooManyRequests:
		return "", fmt.Errorf("GitHub API 限速/拒绝（%d）：可设置 GITHUB_TOKEN 提升限额", resp.StatusCode)
	default:
		return "", fmt.Errorf("GitHub API 返回 %d", resp.StatusCode)
	}

	// diff 大小与请求体上限对齐（审查输入不该超过它）
	body, err := io.ReadAll(io.LimitReader(resp.Body, s.maxBody))
	if err != nil {
		return "", fmt.Errorf("读取 PR diff 失败: %w", err)
	}
	if len(body) == 0 {
		return "", fmt.Errorf("PR %s/%s#%s 没有 diff（可能没有变更或只有评论）", ref.Owner, ref.Repo, ref.Number)
	}
	return string(body), nil
}
