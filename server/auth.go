// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//
// Package server — M7-F2 认证与请求边界。
//
// 三层防线（全部可关/宽松默认，不启用时行为与旧版完全一致）：
//  1. secureHeaders：安全响应头（nosniff / DENY iframe）。
//  2. writeAuth：--auth-token 启用后，写操作（非 GET/HEAD）必须携带
//     `Authorization: Bearer <token>` 或 `X-Auth-Token`；读操作公开——
//     前端看板无需凭证即可浏览（"浏览公开只读"模型）。token 比较用
//     constant-time 防时序侧信道。
//  3. ipRateLimiter：按客户端 IP 的令牌桶，只限审查提交（POST /api/reviews），
//     超限 429 + Retry-After。默认 2 req/s、burst 10，Config 可调（测试注入小阈值）。
//
// 请求体上限（MaxBytesReader → 413）在 handleCreateReview 内实施，见 server.go。
package server

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ========== 1. 安全响应头 ==========

// secureHeaders 附加最小安全响应头。
func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

// ========== 2. 写操作认证 ==========

// errUnauthorized 认证失败（不含 token 值，避免日志泄漏）。
var errUnauthorized = fmt.Errorf("需要认证：请在请求头携带 Authorization: Bearer <token> 或 X-Auth-Token")

// writeAuth 保护写操作：配置了 token 时，非 GET/HEAD 请求必须携带匹配的 token。
// GET/HEAD（看板浏览、任务查询）始终放行。
func writeAuth(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token == "" || r.Method == http.MethodGet || r.Method == http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		if extractToken(r) == "" ||
			subtle.ConstantTimeCompare([]byte(extractToken(r)), []byte(token)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="code-review-agent"`)
			writeErr(w, http.StatusUnauthorized, errUnauthorized.Error())
			return
		}
		next.ServeHTTP(w, r)
	})
}

// extractToken 依次尝试 Authorization: Bearer 与 X-Auth-Token。
func extractToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	return strings.TrimSpace(r.Header.Get("X-Auth-Token"))
}

// ========== 3. IP 令牌桶限流 ==========

// tokenBucket 单 IP 的令牌桶。
type tokenBucket struct {
	tokens float64
	last   time.Time
}

// ipRateLimiter 按客户端 IP 限流的令牌桶集合。
// 只包住审查提交端点——看板/查询是轻操作，不设限。
type ipRateLimiter struct {
	mu      sync.Mutex
	rate    float64 // 每秒补充令牌数
	burst   float64 // 桶容量
	buckets map[string]*tokenBucket
	lastGC  time.Time
}

// newIPRateLimiter rate<=0 或 burst<=0 时使用默认值（2/s，burst 10）。
func newIPRateLimiter(rate float64, burst int) *ipRateLimiter {
	if rate <= 0 {
		rate = 2
	}
	if burst <= 0 {
		burst = 10
	}
	return &ipRateLimiter{
		rate:    rate,
		burst:   float64(burst),
		buckets: make(map[string]*tokenBucket),
		lastGC:  time.Now(),
	}
}

// allow 消耗一个令牌；false = 超限（并返回建议的重试等待时长）。
func (l *ipRateLimiter) allow(ip string) (bool, time.Duration) {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	l.gcLocked(now)

	b, ok := l.buckets[ip]
	if !ok {
		b = &tokenBucket{tokens: l.burst, last: now}
		l.buckets[ip] = b
	}
	// 补充令牌
	b.tokens += now.Sub(b.last).Seconds() * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = now

	if b.tokens >= 1 {
		b.tokens -= 1
		return true, 0
	}
	wait := time.Duration((1 - b.tokens) / l.rate * float64(time.Second))
	return false, wait
}

// gcLocked 惰性清理：每 10 分钟删掉 30 分钟不活跃的桶，防内存无限增长。
func (l *ipRateLimiter) gcLocked(now time.Time) {
	if now.Sub(l.lastGC) < 10*time.Minute {
		return
	}
	l.lastGC = now
	for ip, b := range l.buckets {
		if now.Sub(b.last) > 30*time.Minute {
			delete(l.buckets, ip)
		}
	}
}

// clientIP 提取客户端 IP（本服务无反代假设，直接用 RemoteAddr；
// 部署在反代后时由反代层限流兜底，见部署文档）。
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// limitSubmit 包住审查提交端点做 IP 限流。
func (l *ipRateLimiter) limitSubmit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ok, wait := l.allow(clientIP(r))
		if !ok {
			w.Header().Set("Retry-After", fmt.Sprintf("%d", int(wait.Seconds())+1))
			writeErr(w, http.StatusTooManyRequests,
				fmt.Sprintf("提交过于频繁，请 %d 秒后重试", int(wait.Seconds())+1))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// bodyTooLarge 判断 readJSON 的错误是否为请求体超限（MaxBytesReader）。
// readJSON 会用 %w 包装原始错误，必须用 errors.As 解包。
func bodyTooLarge(err error) bool {
	var maxErr *http.MaxBytesError
	return errors.As(err, &maxErr)
}
