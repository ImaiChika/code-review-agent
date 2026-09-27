// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
package safety

import (
	"context"
	"encoding/json"

	"trpc.group/trpc-go/trpc-agent-go/tool"
)

// AsPermissionPolicy 把 SafetyFilter 适配为框架的 tool.PermissionPolicy（M1-B2）。
//
// 决策语义映射（枚举值本就对齐框架）：
//   - SafetyFilter allow → tool.AllowPermission()
//   - SafetyFilter deny  → tool.DenyPermission(reason)
//   - SafetyFilter ask   → tool.AskPermission(reason)
//
// 沙箱命令进入执行前经此 policy 检查，治理决策走框架语义；
// 调用方仍负责把每次决策落库（cr_permission_decisions）以满足审计要求。
func (f *SafetyFilter) AsPermissionPolicy() tool.PermissionPolicy {
	return tool.PermissionPolicyFunc(func(ctx context.Context, req *tool.PermissionRequest) (tool.PermissionDecision, error) {
		cmd := CommandFromRequest(req)
		decision := f.Check(cmd)
		switch decision.Decision {
		case DecisionDeny:
			return tool.DenyPermission(decision.Reason), nil
		case DecisionAsk:
			return tool.AskPermission(decision.Reason), nil
		default:
			// allow 也透传原因，供调用方落审计记录
			return tool.PermissionDecision{Action: tool.PermissionActionAllow, Reason: decision.Reason}, nil
		}
	})
}

// CommandFromRequest 从框架 PermissionRequest 提取待执行命令。
//
// 约定：Arguments 为 JSON 对象 {"command": "..."}（本项目沙箱命令的封装格式）；
// 只要存在 "command" 键就返回其值（含空串——空命令由过滤器 fail-closed 拒绝）；
// 不是该格式时退化为原始字节串。
func CommandFromRequest(req *tool.PermissionRequest) string {
	if req == nil || len(req.Arguments) == 0 {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal(req.Arguments, &payload); err == nil {
		if cmd, ok := payload["command"].(string); ok {
			return cmd
		}
	}
	return string(req.Arguments)
}
