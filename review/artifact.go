// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
package review

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"code-review-agent/storage"
)

// artifact 写入限制（M1-B5，对应官方验收能力 8 的 artifact 限制要求）。
const (
	maxArtifactsPerTask = 20      // 单任务产物数量上限
	maxArtifactSize     = 1 << 20 // 单产物大小上限 1MB
)

// artifactExtAllowed 产物扩展名白名单。
var artifactExtAllowed = map[string]bool{
	".json": true, ".md": true, ".log": true, ".txt": true,
}

// collectArtifacts 收集一次审查要入库的产物：报告 JSON/MD（内存内容）+ 沙箱输出。
// 审计日志本身已在磁盘上独立落盘（tool_safety_audit.jsonl），不入产物表。
func collectArtifacts(taskID string, jsonContent, mdContent []byte, runs []storage.SandboxRun) []storage.Artifact {
	now := time.Now()
	arts := make([]storage.Artifact, 0, 3)
	add := func(aType, name string, content []byte) {
		arts = append(arts, storage.Artifact{
			TaskID:       taskID,
			ArtifactType: aType,
			FilePath:     name,
			Content:      string(content),
			Size:         len(content),
			CreatedAt:    now,
		})
	}

	if len(jsonContent) > 0 {
		add("report", "review_report.json", jsonContent)
	}
	if len(mdContent) > 0 {
		add("report", "review_report.md", mdContent)
	}
	if len(runs) > 0 {
		var buf bytes.Buffer
		for _, r := range runs {
			fmt.Fprintf(&buf, "$ %s [%s exit=%d %s]\n%s\n", r.Command, r.Backend, r.ExitCode, r.Duration, r.Output)
		}
		add("sandbox_output", taskID+"-sandbox.log", buf.Bytes())
	}
	return arts
}

// saveArtifacts 把产物写入 cr_artifacts，强制数量 / 大小 / 扩展名三重限制。
// 被拒的产物记录原因返回（进日志与报告计数），不阻塞审查。
func saveArtifacts(store storage.Store, arts []storage.Artifact) (saved int, rejected []string) {
	for i, a := range arts {
		switch {
		case i >= maxArtifactsPerTask:
			rejected = append(rejected, fmt.Sprintf("%s: 超出单任务产物数量上限 %d", a.FilePath, maxArtifactsPerTask))
			continue
		case !artifactExtAllowed[strings.ToLower(filepath.Ext(a.FilePath))]:
			rejected = append(rejected, fmt.Sprintf("%s: 扩展名不在白名单", a.FilePath))
			continue
		case len(a.Content) > maxArtifactSize:
			rejected = append(rejected, fmt.Sprintf("%s: 大小 %dB 超过上限 %dB", a.FilePath, len(a.Content), maxArtifactSize))
			continue
		}
		if err := store.SaveArtifact(&a); err != nil {
			rejected = append(rejected, fmt.Sprintf("%s: 写库失败 %v", a.FilePath, err))
			continue
		}
		saved++
	}
	return saved, rejected
}
