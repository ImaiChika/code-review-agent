# 质量评测数据集（dataset/）

> 用于度量 code-review-agent 规则引擎质量的标注数据集 + 评测 harness。
> 对齐官方验收三项质量指标：**高危检出率 ≥ 80%、误报率 ≤ 15%、敏感信息脱敏 ≥ 95%**。
> 演进计划与指标看板见 [PROJECT_GUIDE.md](../PROJECT_GUIDE.md) §七。

---

## 1. 目录结构

```
dataset/
├── README.md                # 本文档
└── cases/
    ├── positive/            # 正样本：含已知问题，期望产出 findings/warnings
    │   ├── sec_password_001.diff      # 样本 diff（输入）
    │   └── sec_password_001.json      # ground truth 标注（同名）
    ├── negative/            # 负样本：干净代码（含误报陷阱），期望 0 findings 0 warnings
    └── redaction/           # 脱敏样本：既有 finding，又要求 evidence 中无明文敏感字面量
```

## 2. 标注 schema（每个样本的 .json）

```jsonc
{
  "id": "sec_password_001",          // 全局唯一 ID；负样本以 neg_ 开头（harness 依赖此前缀统计误报率）
  "description": "var 赋值硬编码密码", // 一句话说明
  "category": "security",             // security / sensitive_leak / resource / error_handling / testing / lifecycle / redaction / none
  "difficulty": "easy",               // easy / medium / trap / hard（hard 层计划在 M2 引入）
  "expected_findings": [              // 期望的高置信度 findings（confidence ≥ 0.7 桶）
    {"rule_id": "SEC-AST-001", "file": "config.go", "line": 3}
  ],
  "expected_warnings": [],            // 期望的低置信度 warnings（confidence < 0.7 桶，如 TST-AST-001）
  "sensitive_literals": []            // 脱敏断言：这些字面量不得以明文出现在任何 evidence 中
}
```

**匹配语义**：产出 finding 与期望按 `rule_id + file + line` **精确匹配**（file 为 diff 中 `+++ b/` 后的路径，line 为新文件行号）。多规则命中同一行（如 SEC-AST-001 + SEC-AST-002）是合法的，标注需分别列出。

## 3. 运行方法

```bash
# 运行评测（harness 在 dataset_eval_test.go）
go test -run TestDataset -v .

# 只看质量报告
go test -run TestDatasetDetectionQuality -v . 2>&1 | grep -A5 "数据集质量报告"

# 脱敏门禁（当前因 P0-1 处于 SKIP，修复后自动变硬门禁）
go test -run TestDatasetRedaction -v .
```

Harness 自动完成：读标注 → 解析 diff → 跑与 main.go 一致的 6 条内置规则 → 去重分组 → 与标注比对 → 输出指标 → 断言门禁。

## 4. 指标定义

| 指标 | 定义 | 门禁 |
|------|------|------|
| 检出率 recall | TP / (TP + FN)，TP=与标注精确匹配的产出 finding | ≥ 80% |
| 精确率 precision | TP / (TP + FP)，FP=标注之外的产出 finding | ≥ 85% |
| 负样本误报率 | 产出了任何 finding/warning 的负样本数 / 负样本总数 | ≤ 15% |
| 脱敏泄漏 | sensitive_literals 中的字面量明文出现在 evidence 的次数 | = 0 |

## 5. 当前基线（2026-09-23，v0 数据集）

| 指标 | 数值 |
|------|------|
| 样本数 | 20（正 10 / 负 8 / 脱敏 2） |
| findings | TP=19，FN=0，FP=0 |
| 检出率 / 精确率 | 100% / 100% |
| warnings | 匹配 1/1（tst_missing_001） |
| 负样本误报率 | 0%（8 个陷阱全防住） |
| 脱敏泄漏 | **2 处**（SEC-AST-001 evidence 明文，即已知问题 P0-1） |

> 注意：v0 是"冒烟 + 陷阱"级数据集，100% 不代表隐藏样本上的真实表现。
> hard 难度层（间接数据流、struct tag 密钥、跨 hunk 生命周期等引擎当前盲区）计划在 M2 引入，
> 届时指标会下降——这正是数据集的用途：让质量可测量、可回归。

## 6. 如何新增样本

1. 在 `cases/positive|negative|redaction/` 下创建 `<id>.diff`（unified diff，行号必须与标注一致）；
2. 创建同名 `<id>.json`，按 §2 schema 填写标注；
3. 先手写标注（期望结果），再跑 harness 验证——**不要先跑引擎再照抄结果**（否则测的是实现不是质量）；
4. 负样本 ID 必须以 `neg_` 开头；
5. 跑 `go test -run TestDataset -v .` 确认全绿后提交。

## 7. 约定

- **只用合成密钥**：所有敏感字面量都是编造的（与 `testdata/` 同一约定），严禁放入真实凭据；
- 一个样本只构造**一种**问题模式，便于失败定位；
- 陷阱类负样本（difficulty=trap）针对的是"正则式实现会误报、语义感知实现不应误报"的模式；
- 修改规则后必须重跑数据集；新增规则必须带对应正负样本；
- 门禁红了不合代码——数据集是回归底线，不是摆设。

## 8. 样本清单（v0）

| ID | 类别 | 难度 | 覆盖 |
|----|------|------|------|
| sec_password_001 | security | easy | var 赋值硬编码密码/APIKey（双行） |
| sec_leak_tokens_002 | sensitive_leak | easy | AWS Key + GitHub Token，同行双规则命中 |
| res_file_001 | resource | easy | os.Open 未 Close |
| res_http_001 | resource | easy | http.Get Body 未 Close |
| gor_leak_001 | resource | medium | 无退出机制的 goroutine |
| err_swallow_001 | error_handling | medium | if err != nil 后 return nil |
| err_ignore_panic_001 | error_handling | easy | `_` 丢弃错误 ×2 + panic |
| tst_missing_001 | testing | easy | 导出函数无测试（warning 桶） |
| db_lifecycle_001 | lifecycle | medium | db.Query rows 未 Close |
| sec_dsn_001 | sensitive_leak | easy | postgres 连接串内嵌密码 |
| neg_env_secret_001 | security | trap | 从 env 读密钥（正确做法） |
| neg_placeholder_001 | security | trap | changeme/xxx-test/your- 占位符 |
| neg_comment_trap_001 | security | trap | 密钥只出现在注释里 |
| neg_main_fatal_001 | error_handling | trap | main 包 log.Fatal（合理） |
| neg_goroutine_ctx_001 | resource | trap | select/ctx.Done 退出机制 |
| neg_resource_closed_001 | resource | trap | defer Close 正确关闭 |
| neg_clean_misc_001 | none | easy | 普通字符串代码 |
| neg_testfile_001 | testing | trap | 测试文件 t.Fatal |
| redact_evidence_001 | redaction | easy | evidence 不得含明文 API Key/DB 密码 |
| redact_multi_001 | redaction | medium | AWS + JWT + 密码混合脱敏 |
