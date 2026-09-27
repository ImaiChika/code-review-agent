// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
package server

import (
	"encoding/json"
	"strings"
	"testing"
)

// newTestMCP 创建测试用 MCP 服务端（真实 SQLite + 真实审查管线）。
func newTestMCP(t *testing.T) *MCPServer {
	t.Helper()
	tmp := t.TempDir()
	m, err := NewMCPServer(Config{
		DBPath:      tmp + "/review.db",
		DataDir:     tmp + "/data",
		SandboxMode: "off",
	})
	if err != nil {
		t.Fatalf("NewMCPServer 失败: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func mcpRequest(id int, method string, params string) []byte {
	return []byte(`{"jsonrpc":"2.0","id":` + itoa(id) + `,"method":"` + method + `","params":` + params + `}`)
}

func itoa(i int) string {
	return strings.TrimSpace(jsonNumber(i))
}

func jsonNumber(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}

// TestMCP_Initialize M5-C6：握手返回协议版本与服务端信息。
func TestMCP_Initialize(t *testing.T) {
	m := newTestMCP(t)
	out, _, err := m.handleMessage(mcpRequest(1, "initialize", `{"protocolVersion":"2025-06-18"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp := out.(map[string]any)
	result := resp["result"].(map[string]any)
	if result["protocolVersion"] != "2025-06-18" {
		t.Errorf("protocolVersion = %v", result["protocolVersion"])
	}
	info := result["serverInfo"].(map[string]any)
	if info["name"] != "code-review-agent" {
		t.Errorf("serverInfo.name = %v", info["name"])
	}
}

// TestMCP_ToolsList 工具清单包含 code_review 与 list_review_tasks。
func TestMCP_ToolsList(t *testing.T) {
	m := newTestMCP(t)
	out, _, _ := m.handleMessage(mcpRequest(2, "tools/list", `{}`))
	tools := out.(map[string]any)["result"].(map[string]any)["tools"].([]map[string]any)
	names := map[string]bool{}
	for _, tool := range tools {
		names[tool["name"].(string)] = true
	}
	if !names["code_review"] || !names["list_review_tasks"] {
		t.Errorf("工具清单缺 code_review/list_review_tasks: %v", names)
	}
}

// TestMCP_ToolCall_Review 工具调用走真实管线：diff → 结构化报告（含证据链与脱敏）。
func TestMCP_ToolCall_Review(t *testing.T) {
	m := newTestMCP(t)
	args, _ := json.Marshal(map[string]any{
		"diff_content": sampleSecretDiff,
	})
	params, _ := json.Marshal(map[string]any{"name": "code_review", "arguments": json.RawMessage(args)})
	out, _, err := m.handleMessage(mcpRequest(3, "tools/call", string(params)))
	if err != nil {
		t.Fatal(err)
	}
	result := out.(map[string]any)["result"].(map[string]any)
	if result["isError"] == true {
		t.Fatalf("工具调用报错: %v", result["content"])
	}
	text := result["content"].([]map[string]any)[0]["text"].(string)

	var rep struct {
		TaskID  string `json:"task_id"`
		Monitor struct {
			RiskScore float64 `json:"risk_score"`
			LLMMode   string  `json:"llm_mode"`
		} `json:"monitor"`
		Findings []struct {
			RuleID        string   `json:"rule_id"`
			EvidenceChain []string `json:"evidence_chain"`
		} `json:"findings"`
	}
	if err := json.Unmarshal([]byte(text), &rep); err != nil {
		t.Fatalf("工具输出应为完整报告 JSON: %v", err)
	}
	if rep.TaskID == "" || len(rep.Findings) == 0 {
		t.Fatal("报告应含 task_id 与 findings")
	}
	raw, _ := json.Marshal(rep.Findings)
	if strings.Contains(string(raw), "sk-live-q1w2e3r4t5y6u7i8o9p0") {
		t.Error("MCP 输出泄漏明文密钥")
	}
}

// TestMCP_ToolCall_ListTasks list_review_tasks 返回历史任务。
func TestMCP_ToolCall_ListTasks(t *testing.T) {
	m := newTestMCP(t)
	// 先产生一个任务
	args, _ := json.Marshal(map[string]any{"diff_content": sampleSecretDiff})
	params, _ := json.Marshal(map[string]any{"name": "code_review", "arguments": json.RawMessage(args)})
	m.handleMessage(mcpRequest(4, "tools/call", string(params)))

	args2, _ := json.Marshal(map[string]any{"limit": 5})
	params2, _ := json.Marshal(map[string]any{"name": "list_review_tasks", "arguments": json.RawMessage(args2)})
	out, _, _ := m.handleMessage(mcpRequest(5, "tools/call", string(params2)))
	result := out.(map[string]any)["result"].(map[string]any)
	text := result["content"].([]map[string]any)[0]["text"].(string)
	if !strings.Contains(text, "task_id") {
		t.Errorf("任务列表应含 task_id: %s", text)
	}
}

// TestMCP_Errors 未知工具 / 未知方法 / 缺参数。
func TestMCP_Errors(t *testing.T) {
	m := newTestMCP(t)

	args, _ := json.Marshal(map[string]any{"name": "no_such_tool"})
	params, _ := json.Marshal(map[string]any{"name": "no_such_tool", "arguments": json.RawMessage(args)})
	out, _, _ := m.handleMessage(mcpRequest(6, "tools/call", string(params)))
	if out.(map[string]any)["result"].(map[string]any)["isError"] != true {
		t.Error("未知工具应返回 isError")
	}

	out, _, _ = m.handleMessage(mcpRequest(7, "no/such/method", `{}`))
	if out.(map[string]any)["error"] == nil {
		t.Error("未知方法应返回 error")
	}

	params3, _ := json.Marshal(map[string]any{"name": "code_review", "arguments": json.RawMessage(`{}`)})
	out, _, _ = m.handleMessage(mcpRequest(8, "tools/call", string(params3)))
	if out.(map[string]any)["result"].(map[string]any)["isError"] != true {
		t.Error("缺输入参数应返回 isError")
	}
}

// TestMCP_Notification 通知不产生响应。
func TestMCP_Notification(t *testing.T) {
	m := newTestMCP(t)
	out, isNotification, _ := m.handleMessage([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	if !isNotification || out != nil {
		t.Error("通知不应产生响应")
	}
}
