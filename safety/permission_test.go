// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
package safety

import (
	"context"
	"testing"

	"trpc.group/trpc-go/trpc-agent-go/tool"
)

// TestAsPermissionPolicy_Allow 白名单命令 → 框架 allow 决策（M1-B2）。
func TestAsPermissionPolicy_Allow(t *testing.T) {
	filter := NewSafetyFilter(DefaultConfig())
	policy := filter.AsPermissionPolicy()

	dec, err := policy.CheckToolPermission(context.Background(), &tool.PermissionRequest{
		ToolName:  "sandbox",
		Arguments: []byte(`{"command":"go vet ./..."}`),
	})
	if err != nil {
		t.Fatalf("CheckToolPermission 失败: %v", err)
	}
	if dec.Action != tool.PermissionActionAllow {
		t.Errorf("Action = %q, 期望 allow", dec.Action)
	}
	if dec.Reason == "" {
		t.Error("allow 决策应透传原因供审计")
	}
}

// TestAsPermissionPolicy_Deny 黑名单命令 → 框架 deny 决策 + 原因。
func TestAsPermissionPolicy_Deny(t *testing.T) {
	filter := NewSafetyFilter(DefaultConfig())
	policy := filter.AsPermissionPolicy()

	dec, err := policy.CheckToolPermission(context.Background(), &tool.PermissionRequest{
		ToolName:  "sandbox",
		Arguments: []byte(`{"command":"rm -rf /"}`),
	})
	if err != nil {
		t.Fatalf("CheckToolPermission 失败: %v", err)
	}
	if dec.Action != tool.PermissionActionDeny {
		t.Errorf("Action = %q, 期望 deny", dec.Action)
	}
	if dec.Reason == "" {
		t.Error("deny 决策必须带原因")
	}
}

// TestAsPermissionPolicy_Ask 可疑注入模式 → 框架 ask 决策（不执行，等人工）。
func TestAsPermissionPolicy_Ask(t *testing.T) {
	filter := NewSafetyFilter(DefaultConfig())
	policy := filter.AsPermissionPolicy()

	dec, err := policy.CheckToolPermission(context.Background(), &tool.PermissionRequest{
		ToolName:  "sandbox",
		Arguments: []byte(`{"command":"go test ./... && echo $(whoami)"}`),
	})
	if err != nil {
		t.Fatalf("CheckToolPermission 失败: %v", err)
	}
	if dec.Action != tool.PermissionActionAsk {
		t.Errorf("Action = %q, 期望 ask", dec.Action)
	}
}

// TestAsPermissionPolicy_Empty 空命令 → deny（fail-closed）。
func TestAsPermissionPolicy_Empty(t *testing.T) {
	filter := NewSafetyFilter(DefaultConfig())
	policy := filter.AsPermissionPolicy()

	dec, _ := policy.CheckToolPermission(context.Background(), &tool.PermissionRequest{
		ToolName:  "sandbox",
		Arguments: []byte(`{"command":""}`),
	})
	if dec.Action != tool.PermissionActionDeny {
		t.Errorf("空命令 Action = %q, 期望 deny", dec.Action)
	}
}

// TestCommandFromRequest 参数提取的三种形态。
func TestCommandFromRequest(t *testing.T) {
	// JSON 封装格式
	got := CommandFromRequest(&tool.PermissionRequest{Arguments: []byte(`{"command":"go test ./..."}`)})
	if got != "go test ./..." {
		t.Errorf("JSON 封装: got %q", got)
	}
	// 非该格式的原始字节串 → 原样返回
	got = CommandFromRequest(&tool.PermissionRequest{Arguments: []byte("raw command")})
	if got != "raw command" {
		t.Errorf("原始字节串: got %q", got)
	}
	// nil 请求 / 空参数
	if got := CommandFromRequest(nil); got != "" {
		t.Errorf("nil 请求: got %q", got)
	}
	if got := CommandFromRequest(&tool.PermissionRequest{}); got != "" {
		t.Errorf("空参数: got %q", got)
	}
}
