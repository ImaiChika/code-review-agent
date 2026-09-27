// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
package llmreview

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"trpc.group/trpc-go/trpc-agent-go/model"
)

// FakeModel 确定性假模型（M4-C2）。
//
// 语义对齐框架 test 子模块的 QueueModel（Push 预设响应、按序回放、无网络）；
// 该子模块未随框架 v1.10.0 发布，故按其模式自实现，保持 go.mod 可移植
// （不引入指向本地框架目录的 replace）。
//
// 与 QueueModel 的一个差异：QueueModel 每次 GenerateContent 回放全部入队响应，
// 本实现按序弹出一条——与"一次复核请求消费一条预设响应"的批量复核协议匹配。
type FakeModel struct {
	mu       sync.Mutex
	queued   []*model.Response
	consumed int
}

// NewFakeModel 创建假模型。
func NewFakeModel() *FakeModel {
	return &FakeModel{}
}

// Push 入队一条预设响应（按序被 GenerateContent 消费）。
func (m *FakeModel) Push(resp *model.Response) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.queued = append(m.queued, resp)
}

// PushText 入队一段纯文本响应（便捷方法）。
func (m *FakeModel) PushText(content string) {
	m.Push(&model.Response{
		Choices: []model.Choice{{Index: 0, Message: model.Message{Role: model.RoleAssistant, Content: content}}},
	})
}

// PushConfirmAll 入队一段全 CONFIRM 的判定文本（n 条候选）。
func (m *FakeModel) PushConfirmAll(n int) {
	m.PushText(confirmAllText(n))
}

// confirmAllText 生成 `i. CONFIRM: ok` 格式的 n 行判定文本。
func confirmAllText(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "%d. CONFIRM: ok\n", i)
	}
	return b.String()
}

// GenerateContent 按序回放一条预设响应。
//
// 队列为空时的默认行为（--fake-model 自包含语义，M4-C2）：
// 从请求 prompt 中解析候选数量，回放"全部 CONFIRM"的确定性判定——
// 用于验证"LLM 开启后 recall 不降"；剔除机制用显式 Push 的响应测试。
func (m *FakeModel) GenerateContent(ctx context.Context, request *model.Request) (<-chan *model.Response, error) {
	if request == nil {
		return nil, errors.New("fake model: request is nil")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.consumed >= len(m.queued) {
		resp := &model.Response{
			Choices: []model.Choice{{
				Index:   0,
				Message: model.Message{Role: model.RoleAssistant, Content: confirmAllText(countCandidates(request))},
			}},
		}
		ch := make(chan *model.Response, 1)
		ch <- resp
		close(ch)
		return ch, nil
	}
	resp := m.queued[m.consumed]
	m.consumed++

	ch := make(chan *model.Response, 1)
	ch <- resp
	close(ch)
	return ch, nil
}

// Info 返回模型信息。
func (m *FakeModel) Info() model.Info {
	return model.Info{Name: "fake-model"}
}

// candidateLineRe 匹配 prompt 中的候选行（`3. [SEC-AST-001][high] ...`）。
var candidateLineRe = regexp.MustCompile(`(?m)^\d+\. \[`)

// countCandidates 从复核请求的 prompt 中解析候选数量。
func countCandidates(request *model.Request) int {
	if request == nil || len(request.Messages) == 0 {
		return 0
	}
	content := request.Messages[len(request.Messages)-1].Content
	return len(candidateLineRe.FindAllString(content, -1))
}

// Consumed 返回已消费的响应数（测试断言用）。
func (m *FakeModel) Consumed() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.consumed
}
