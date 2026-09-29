// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//
// M8-C9：误报标记记忆降噪。
//
// 闭环：前端对某条 finding 点"标记误报"→ 落 cr_false_positive_marks →
// 后续审查遇到同模式问题时自动降置信度——低于 0.7 阈值即进入 warnings
// （需人工复核桶），不再作为正式 finding 刷屏。
//
// 匹配语义（v1，保守精准）：
//   - 精确命中（rule_id + file_path + line）→ confidence × 0.5
//   - 文件命中（rule_id + file_path，行号漂移时仍生效）→ confidence × 0.6
//
// 设计约束：记忆是增强不是权威——降级不删除（人仍能在 warnings 里看到并
// 撤销标记）；marks 表为空或 DB 不可用时零影响（dry-run/门禁行为不变）。
package review

import (
	"log"

	"code-review-agent/findings"
	"code-review-agent/storage"
)

// 降置信度系数。
const (
	fpExactFactor = 0.5 // rule + file + line 全匹配
	fpFileFactor  = 0.6 // rule + file 匹配（行号漂移容错）
)

// applyFalsePositiveMemory 依据历史误报标记降置信度。
// 在 Deduplicate 之前调用：降级后 confidence < 0.7 的条目自然进入 warnings 桶。
// DB 打开失败（如 dry-run）静默跳过——记忆降噪不阻塞审查主链路。
func applyFalsePositiveMemory(fs []findings.Finding, dbPath string) {
	if len(fs) == 0 || dbPath == "" {
		return
	}
	store, err := storage.NewSQLiteStore(dbPath)
	if err != nil {
		return
	}
	defer store.Close()

	marks, err := store.ListFalsePositiveMarks()
	if err != nil || len(marks) == 0 {
		return
	}

	// 建索引：rule → file → []line
	type fileMarks map[string][]int
	byRule := make(map[string]fileMarks, len(marks))
	for _, m := range marks {
		fm, ok := byRule[m.RuleID]
		if !ok {
			fm = fileMarks{}
			byRule[m.RuleID] = fm
		}
		fm[m.FilePath] = append(fm[m.FilePath], m.Line)
	}

	for i := range fs {
		f := &fs[i]
		fm, ok := byRule[f.RuleID]
		if !ok {
			continue
		}
		lines, ok := fm[f.File]
		if !ok {
			continue
		}
		factor := fpFileFactor
		for _, l := range lines {
			if l != 0 && l == f.Line {
				factor = fpExactFactor
				break
			}
		}
		before := f.Confidence
		f.Confidence *= factor
		log.Printf("🧠 记忆降噪: %s %s:%d confidence %.2f → %.2f（历史误报标记）",
			f.RuleID, f.File, f.Line, before, f.Confidence)
	}
}
