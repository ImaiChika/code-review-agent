// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
package review

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code-review-agent/storage"
)

func newTestStore(t *testing.T) *storage.SQLiteStore {
	t.Helper()
	store, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore 失败: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func mkArtifact(taskID, name, content string) storage.Artifact {
	return storage.Artifact{
		TaskID:       taskID,
		ArtifactType: "report",
		FilePath:     name,
		Content:      content,
		Size:         len(content),
		CreatedAt:    time.Now(),
	}
}

// TestSaveArtifacts_Normal 正常产物全部入库（M1-B5）。
func TestSaveArtifacts_Normal(t *testing.T) {
	store := newTestStore(t)
	arts := []storage.Artifact{
		mkArtifact("t1", "review_report.json", `{"task_id":"t1"}`),
		mkArtifact("t1", "review_report.md", "# 报告"),
	}
	saved, rejected := saveArtifacts(store, arts)
	if saved != 2 || len(rejected) != 0 {
		t.Fatalf("应保存 2 拒 0, 得到 saved=%d rejected=%v", saved, rejected)
	}
	got, err := store.GetArtifacts("t1")
	if err != nil {
		t.Fatalf("GetArtifacts 失败: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("库中应有 2 条产物, 得到 %d", len(got))
	}
}

// TestSaveArtifacts_ExtensionWhitelist 扩展名白名单外的产物被拒。
func TestSaveArtifacts_ExtensionWhitelist(t *testing.T) {
	store := newTestStore(t)
	arts := []storage.Artifact{mkArtifact("t2", "payload.exe", "MZ...")}
	saved, rejected := saveArtifacts(store, arts)
	if saved != 0 || len(rejected) != 1 || !strings.Contains(rejected[0], "扩展名") {
		t.Fatalf("应拒绝 .exe: saved=%d rejected=%v", saved, rejected)
	}
}

// TestSaveArtifacts_SizeLimit 超过 1MB 的产物被拒。
func TestSaveArtifacts_SizeLimit(t *testing.T) {
	store := newTestStore(t)
	big := strings.Repeat("x", maxArtifactSize+1)
	arts := []storage.Artifact{mkArtifact("t3", "big.log", big)}
	saved, rejected := saveArtifacts(store, arts)
	if saved != 0 || len(rejected) != 1 || !strings.Contains(rejected[0], "大小") {
		t.Fatalf("应拒绝超大产物: saved=%d rejected=%v", saved, rejected)
	}
}

// TestSaveArtifacts_CountLimit 超出单任务数量上限的产物被拒。
func TestSaveArtifacts_CountLimit(t *testing.T) {
	store := newTestStore(t)
	arts := make([]storage.Artifact, 0, maxArtifactsPerTask+3)
	for i := 0; i < maxArtifactsPerTask+3; i++ {
		arts = append(arts, mkArtifact("t4", fmt.Sprintf("a%d.json", i), "x"))
	}
	saved, rejected := saveArtifacts(store, arts)
	if saved != maxArtifactsPerTask || len(rejected) != 3 {
		t.Fatalf("应保存 %d 拒 3, 得到 saved=%d rejected=%d", maxArtifactsPerTask, saved, len(rejected))
	}
}

// TestCollectArtifacts 收集逻辑：报告三份（JSON/MD/HTML，M7-F6）+ 有沙箱记录时追加沙箱日志。
func TestCollectArtifacts(t *testing.T) {
	// 无沙箱记录 → 3 个产物
	arts := collectArtifacts("t5", []byte("{}"), []byte("# r"), []byte("<html></html>"), nil)
	if len(arts) != 3 {
		t.Fatalf("无沙箱应收集 3 个产物, 得到 %d", len(arts))
	}
	if arts[2].FilePath != "review_report.html" {
		t.Errorf("第三个产物应为 HTML 报告, 得到 %q", arts[2].FilePath)
	}

	// 有沙箱记录 → 4 个（追加 sandbox_output）
	runs := []storage.SandboxRun{{TaskID: "t5", Command: "go vet ./...", ExitCode: 0, Output: "ok"}}
	arts = collectArtifacts("t5", []byte("{}"), []byte("# r"), nil, runs)
	if len(arts) != 3 { // html 空内容跳过，仍 3 个（json/md/sandbox）
		t.Fatalf("有沙箱应收集 3 个产物（html 空跳过）, 得到 %d", len(arts))
	}
	last := arts[2]
	if last.ArtifactType != "sandbox_output" || !strings.HasSuffix(last.FilePath, "-sandbox.log") {
		t.Errorf("沙箱产物类型/命名不对: %+v", last)
	}
}

// TestRun_SavesArtifacts 端到端：非 dry-run 审查后产物入库且计数正确（B5 退出标准）。
func TestRun_SavesArtifacts(t *testing.T) {
	outDir := t.TempDir()
	dbPath := filepath.Join(outDir, "review.db")
	rep, err := Run(Options{
		DiffFile:    "../testdata/security_issue.diff",
		OutputDir:   outDir,
		DBPath:      dbPath,
		SandboxMode: SandboxOff,
	})
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}

	store, err := storage.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("打开库失败: %v", err)
	}
	defer store.Close()

	arts, err := store.GetArtifacts(rep.TaskID)
	if err != nil {
		t.Fatalf("GetArtifacts 失败: %v", err)
	}
	var hasJSON, hasMD bool
	for _, a := range arts {
		switch a.FilePath {
		case "review_report.json":
			hasJSON = true
			if !strings.Contains(a.Content, rep.TaskID) {
				t.Error("报告 JSON 产物内容应包含 task_id")
			}
		case "review_report.md":
			hasMD = true
		}
	}
	if !hasJSON || !hasMD {
		t.Fatalf("产物应包含报告 JSON 和 MD, 得到 %v", arts)
	}
	if rep.Monitor.ArtifactsSaved != len(arts) || rep.Monitor.ArtifactsRejected != 0 {
		t.Errorf("Monitor 计数不符: saved=%d rejected=%d 库中=%d",
			rep.Monitor.ArtifactsSaved, rep.Monitor.ArtifactsRejected, len(arts))
	}
}
