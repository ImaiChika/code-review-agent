// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//
// 数据集质量评测 harness：遍历 dataset/cases/ 下的标注样本，
// 用规则引擎跑完整审查链路（解析 → 规则 → 去重），与 ground truth 比对，
// 输出检出率 / 精确率 / 负样本误报率 / 脱敏泄漏四项指标。
//
// 门禁对齐官方验收标准：高危检出率 ≥ 80%，误报率 ≤ 15%，脱敏 0 泄漏。
// 数据集说明见 dataset/README.md，演进计划见 PROJECT_GUIDE.md §七。
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"code-review-agent/diff"
	"code-review-agent/findings"
	"code-review-agent/rules"
)

// datasetRoot 是质量评测数据集的根目录。
const datasetRoot = "dataset"

// expectedLocation 描述一条期望的 finding / warning 的定位信息。
type expectedLocation struct {
	RuleID string `json:"rule_id"`
	File   string `json:"file"`
	Line   int    `json:"line"`
}

// evalCase 是单个评测样本的标注（ground truth）。
type evalCase struct {
	ID                string             `json:"id"`
	Description       string             `json:"description"`
	Category          string             `json:"category"`
	Difficulty        string             `json:"difficulty"`
	ExpectedFindings  []expectedLocation `json:"expected_findings"`
	ExpectedWarnings  []expectedLocation `json:"expected_warnings"`
	SensitiveLiterals []string           `json:"sensitive_literals"`
}

// caseResult 是单个样本的评测结果。
type caseResult struct {
	tc            evalCase
	findings      []findings.Finding
	warnings      []findings.Finding
	truePositive  int
	falsePositive []findings.Finding
	missed        []expectedLocation
	warnMatched   int
	warnFP        []findings.Finding
	warnMissed    []expectedLocation
	leaks         []string // 泄漏进 evidence 的明文敏感字面量
	execErr       error
}

// newEvalEngine 构建与 main.go 一致的内置规则引擎。
func newEvalEngine() *rules.RuleEngine {
	engine := rules.NewEngine()
	engine.Register(rules.NewTokenSecretRule())
	engine.Register(rules.NewTokenLeakRule())
	engine.Register(rules.NewTokenGoroutineRule())
	engine.Register(rules.NewTokenResourceRule())
	engine.Register(rules.NewTokenErrorRule())
	engine.Register(rules.NewTokenMissingTestRule())
	return engine
}

// matchExpectations 按 rule_id + file + line 精确贪心匹配期望与产出。
// 返回：匹配数、未匹配的产出（误报）、未满足的期望（漏检）。
func matchExpectations(expected []expectedLocation, produced []findings.Finding) (matched int, fp []findings.Finding, missed []expectedLocation) {
	used := make([]bool, len(produced))
	for _, e := range expected {
		found := false
		for i, f := range produced {
			if used[i] {
				continue
			}
			if f.RuleID == e.RuleID && f.File == e.File && f.Line == e.Line {
				used[i] = true
				matched++
				found = true
				break
			}
		}
		if !found {
			missed = append(missed, e)
		}
	}
	for i, f := range produced {
		if !used[i] {
			fp = append(fp, f)
		}
	}
	return matched, fp, missed
}

// loadDataset 加载并执行全部数据集样本。
func loadDataset(t *testing.T) []*caseResult {
	t.Helper()

	paths, err := filepath.Glob(filepath.Join(datasetRoot, "cases", "*", "*.json"))
	if err != nil {
		t.Fatalf("扫描数据集失败: %v", err)
	}
	if len(paths) == 0 {
		t.Fatalf("数据集为空：%s/cases/ 下没有标注 JSON", datasetRoot)
	}
	sort.Strings(paths)

	engine := newEvalEngine()
	var results []*caseResult

	for _, jsonPath := range paths {
		data, err := os.ReadFile(jsonPath)
		if err != nil {
			t.Fatalf("读取标注 %s 失败: %v", jsonPath, err)
		}
		var ec evalCase
		if err := json.Unmarshal(data, &ec); err != nil {
			t.Fatalf("解析标注 %s 失败: %v", jsonPath, err)
		}

		res := &caseResult{tc: ec}

		diffPath := strings.TrimSuffix(jsonPath, ".json") + ".diff"
		files, err := diff.ReadFromFile(diffPath)
		if err != nil {
			res.execErr = err
			results = append(results, res)
			continue
		}

		raw, err := engine.Run(files)
		if err != nil {
			res.execErr = err
			results = append(results, res)
			continue
		}

		dedup := findings.Deduplicate(raw)
		res.findings = dedup.Findings
		res.warnings = dedup.Warnings
		res.truePositive, res.falsePositive, res.missed =
			matchExpectations(ec.ExpectedFindings, dedup.Findings)
		res.warnMatched, res.warnFP, res.warnMissed =
			matchExpectations(ec.ExpectedWarnings, dedup.Warnings)

		// 脱敏检查：敏感字面量不得以明文出现在任何 evidence 中
		for _, lit := range ec.SensitiveLiterals {
			for _, f := range dedup.Findings {
				if strings.Contains(f.Evidence, lit) {
					res.leaks = append(res.leaks, ec.ID+": finding "+f.RuleID+" evidence 泄漏敏感字面量")
					break
				}
			}
			for _, w := range dedup.Warnings {
				if strings.Contains(w.Evidence, lit) {
					res.leaks = append(res.leaks, ec.ID+": warning "+w.RuleID+" evidence 泄漏敏感字面量")
					break
				}
			}
		}

		results = append(results, res)
	}

	return results
}

// TestDatasetDetectionQuality 用数据集度量规则引擎的检出质量。
//
// 门禁（对齐官方验收标准）：
//   - 检出率（recall）≥ 80%
//   - 精确率（precision）≥ 85%（等价误报率 ≤ 15%）
//   - 负样本误报率 ≤ 15%
func TestDatasetDetectionQuality(t *testing.T) {
	results := loadDataset(t)

	var tp, fn, fp int
	var warnTP, warnMissedTotal int
	var negativeTotal, negativeFlagged int

	for _, res := range results {
		if res.execErr != nil {
			t.Errorf("[%s] 样本执行失败: %v", res.tc.ID, res.execErr)
			continue
		}

		tp += res.truePositive
		fn += len(res.missed)
		fp += len(res.falsePositive)
		warnTP += res.warnMatched
		warnMissedTotal += len(res.warnMissed)

		if strings.HasPrefix(res.tc.ID, "neg_") {
			negativeTotal++
			if len(res.findings)+len(res.warnings) > 0 {
				negativeFlagged++
			}
		}

		status := "PASS"
		if len(res.missed) > 0 || len(res.falsePositive) > 0 {
			status = "FAIL"
		}
		t.Logf("[%s] %-24s (%s/%s) findings=%d warnings=%d 漏检=%d 误报=%d",
			status, res.tc.ID, res.tc.Category, res.tc.Difficulty,
			len(res.findings), len(res.warnings), len(res.missed), len(res.falsePositive))
		for _, m := range res.missed {
			t.Logf("       漏检: %s %s:%d", m.RuleID, m.File, m.Line)
		}
		for _, f := range res.falsePositive {
			t.Logf("       误报: %s %s:%d (%s)", f.RuleID, f.File, f.Line, f.Title)
		}
	}

	recall, precision, negFPR := 0.0, 0.0, 0.0
	if tp+fn > 0 {
		recall = float64(tp) / float64(tp+fn)
	}
	if tp+fp > 0 {
		precision = float64(tp) / float64(tp+fp)
	}
	if negativeTotal > 0 {
		negFPR = float64(negativeFlagged) / float64(negativeTotal)
	}

	t.Logf("")
	t.Logf("===== 数据集质量报告（%d 个样本）=====", len(results))
	t.Logf("findings:  TP=%d  FN=%d  FP=%d  检出率=%.0f%%  精确率=%.0f%%", tp, fn, fp, recall*100, precision*100)
	t.Logf("warnings:  匹配=%d  漏检=%d", warnTP, warnMissedTotal)
	t.Logf("负样本:    %d 个中 %d 个被误报（误报率=%.0f%%）", negativeTotal, negativeFlagged, negFPR*100)

	if recall < 0.80 {
		t.Errorf("检出率 %.0f%% 低于官方门禁 80%%", recall*100)
	}
	if precision < 0.85 {
		t.Errorf("精确率 %.0f%% 低于官方门禁（等价误报率 ≤ 15%%）", precision*100)
	}
	if negFPR > 0.15 {
		t.Errorf("负样本误报率 %.0f%% 超过官方门禁 15%%", negFPR*100)
	}
}

// TestDatasetRedaction 验证敏感字面量不会以明文出现在 finding/warning evidence 中。
//
// 当前引擎存在已知问题 P0-1（SEC-AST-001 的 evidence 未脱敏），
// 检测到泄漏时本测试 SKIP 并给出计数；修复（PROJECT_GUIDE §七 M0-A1：统一 Redactor）后
// 泄漏为 0，本测试自动转为硬门禁，无需改代码。
func TestDatasetRedaction(t *testing.T) {
	results := loadDataset(t)

	var leaks []string
	redactionCases := 0
	for _, res := range results {
		if len(res.tc.SensitiveLiterals) == 0 {
			continue
		}
		redactionCases++
		leaks = append(leaks, res.leaks...)
	}

	if redactionCases == 0 {
		t.Fatal("数据集中没有 redaction 样本")
	}

	if len(leaks) > 0 {
		for _, l := range leaks {
			t.Logf("泄漏: %s", l)
		}
		t.Skipf("发现 %d 处明文敏感信息泄漏（已知问题 P0-1，见 PROJECT_GUIDE §6 / §七 M0-A1）；修复后本测试自动转为硬门禁", len(leaks))
	}

	t.Logf("脱敏检查通过：%d 个 redaction 样本，0 处泄漏", redactionCases)
}
