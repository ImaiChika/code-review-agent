// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
package server

// MCP stdio 服务端（M5-C6）：把 code_review 能力暴露为 MCP 工具，
// 供 Claude Code / Cursor 等 MCP 客户端直连。
//
// 协议：JSON-RPC 2.0，newline 分帧（MCP stdio transport 约定）。
// 支持方法：initialize / tools/list / tools/call / ping；
// 框架 v1.10.0 未提供 MCP 服务端（server/ 仅 a2a、openai），故按 MCP 规范自实现最小子集。
//
// 用法：code-review-agent mcp （stdin/stdout 由 MCP 客户端管理）

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"code-review-agent/review"
)

// mcpProtocolVersion 服务端支持的协议版本。
const mcpProtocolVersion = "2024-11-05"

// MCPServer MCP 工具服务端（stdio）。
type MCPServer struct {
	cfg Config
	srv *Server // 复用 HTTP 服务的审查执行与存储层
	mu  sync.Mutex
}

// NewMCPServer 创建 MCP 服务端。
func NewMCPServer(cfg Config) (*MCPServer, error) {
	inner, err := New(cfg)
	if err != nil {
		return nil, err
	}
	return &MCPServer{cfg: cfg, srv: inner}, nil
}

// Serve 运行 stdio 消息循环（阻塞；EOF 退出）。
func (m *MCPServer) Serve(in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		output, isNotification, err := m.handleMessage(line)
		if err != nil {
			// 解析失败：回 JSON-RPC error（若是请求）
			_ = writeMCP(out, map[string]any{
				"jsonrpc": "2.0",
				"id":      nil,
				"error":   map[string]any{"code": -32700, "message": "解析错误: " + err.Error()},
			})
			continue
		}
		if isNotification || output == nil {
			continue
		}
		if err := writeMCP(out, output); err != nil {
			return err
		}
	}
	return scanner.Err()
}

// handleMessage 处理一条 JSON-RPC 消息，返回响应（通知返回 nil）。
func (m *MCPServer) handleMessage(line []byte) (any, bool, error) {
	var msg struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(line, &msg); err != nil {
		return nil, false, err
	}
	isNotification := len(msg.ID) == 0

	switch msg.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		version := p.ProtocolVersion
		if version == "" {
			version = mcpProtocolVersion
		}
		return map[string]any{
			"jsonrpc": "2.0",
			"id":      rawID(msg.ID),
			"result": map[string]any{
				"protocolVersion": version,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "code-review-agent", "version": Version},
			},
		}, false, nil

	case "notifications/initialized":
		return nil, true, nil

	case "ping":
		return map[string]any{"jsonrpc": "2.0", "id": rawID(msg.ID), "result": map[string]any{}}, false, nil

	case "tools/list":
		return map[string]any{
			"jsonrpc": "2.0",
			"id":      rawID(msg.ID),
			"result":  map[string]any{"tools": mcpTools()},
		}, false, nil

	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return rpcError(msg.ID, -32602, "参数解析失败"), false, nil
		}
		result, toolErr := m.callTool(p.Name, p.Arguments)
		payload := map[string]any{"content": []map[string]any{{"type": "text", "text": result}}}
		if toolErr != nil {
			payload["isError"] = true
			payload["content"] = []map[string]any{{"type": "text", "text": toolErr.Error()}}
		}
		return map[string]any{"jsonrpc": "2.0", "id": rawID(msg.ID), "result": payload}, false, nil

	default:
		if isNotification {
			return nil, true, nil
		}
		return rpcError(msg.ID, -32601, "未知方法: "+msg.Method), false, nil
	}
}

// mcpTools 工具清单（code_review + list_review_tasks）。
func mcpTools() []map[string]any {
	return []map[string]any{
		{
			"name":        "code_review",
			"description": "审查代码变更：输入 unified diff 文本或本机 git 仓库路径，返回结构化审查报告（风险评分 + findings 含脱敏证据与证据链）。规则引擎确定性执行，可选沙箱与 LLM 复核。",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"diff_content": map[string]any{"type": "string", "description": "unified diff 文本（与 repo_path 二选一）"},
					"repo_path":    map[string]any{"type": "string", "description": "本机 git 仓库路径（取未提交变更）"},
					"sandbox":      map[string]any{"type": "boolean", "description": "是否执行沙箱（仅 repo_path 有效）"},
					"llm_mode":     map[string]any{"type": "string", "description": "LLM 复核：fake（确定性回放）/ openai（服务端需 OPENAI_API_KEY）"},
				},
			},
		},
		{
			"name":        "list_review_tasks",
			"description": "列出历史审查任务（task_id / 时间 / 输入 / 风险等级）",
			"inputSchema": map[string]any{
				"type":       "object",
				"properties": map[string]any{"limit": map[string]any{"type": "integer", "description": "返回条数（默认 20）"}},
			},
		},
	}
}

// callTool 执行工具调用，返回文本结果。
func (m *MCPServer) callTool(name string, args json.RawMessage) (string, error) {
	switch name {
	case "code_review":
		var req createReviewRequest
		if len(args) > 0 {
			if err := json.Unmarshal(args, &req); err != nil {
				return "", fmt.Errorf("解析参数失败: %w", err)
			}
		}
		if req.DiffContent == "" && req.RepoPath == "" {
			return "", fmt.Errorf("diff_content 与 repo_path 必须提供其一")
		}
		sandboxMode := review.SandboxOff
		if req.Sandbox && req.RepoPath != "" {
			sandboxMode = m.cfg.SandboxMode
			if sandboxMode == "" || sandboxMode == review.SandboxOff {
				sandboxMode = "local"
			}
		}

		m.mu.Lock()
		defer m.mu.Unlock()
		rep, err := review.Run(review.Options{
			DiffContent: req.DiffContent,
			RepoPath:    req.RepoPath,
			RulesDir:    m.cfg.RulesDir,
			DBPath:      m.cfg.DBPath,
			OutputDir:   m.cfg.DataDir,
			SandboxMode: sandboxMode,
			AuditFile:   "tool_safety_audit.jsonl",
			LLMMode:     req.LLMMode,
		})
		if err != nil {
			return "", err
		}
		out, err := json.MarshalIndent(rep, "", "  ")
		if err != nil {
			return "", err
		}
		return string(out), nil

	case "list_review_tasks":
		limit := 20
		if len(args) > 0 {
			var p struct {
				Limit int `json:"limit"`
			}
			if json.Unmarshal(args, &p) == nil && p.Limit > 0 && p.Limit <= 500 {
				limit = p.Limit
			}
		}
		tasks, err := m.srv.store.ListTasks(limit)
		if err != nil {
			return "", err
		}
		out, err := json.MarshalIndent(tasks, "", "  ")
		if err != nil {
			return "", err
		}
		return string(out), nil

	default:
		return "", fmt.Errorf("未知工具: %s", name)
	}
}

// writeMCP 序列化并写入一行 JSON 响应。
func writeMCP(out io.Writer, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = out.Write(append(data, '\n'))
	return err
}

// rpcError 构造 JSON-RPC 错误响应。
func rpcError(id json.RawMessage, code int, msg string) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      rawID(id),
		"error":   map[string]any{"code": code, "message": msg},
	}
}

// rawID 透传请求 ID（保留原始类型：数字或字符串）。
func rawID(id json.RawMessage) any {
	if len(id) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(id, &v); err != nil {
		return string(id)
	}
	return v
}

// Close 关闭底层资源。
func (m *MCPServer) Close() error {
	return m.srv.Close()
}
