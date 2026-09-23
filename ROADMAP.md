# 成熟度演进计划表（ROADMAP）

> 目标：把 code-review-agent 从"能跑的原型"推进到**未来可用的生产级工具**。
> 配套文档：[PROJECT_GUIDE.md](PROJECT_GUIDE.md)（现状全量体检）、[dataset/](dataset/README.md)（质量评测数据集）。
>
> 制定日期：2026-09-23。工作量假设：业余时间每周 8–12 小时。

---

## 一、"成熟"的定义（可量化验收门槛）

"未来可用"不是感觉，是下面这张表的最后一列。所有指标都用 `dataset/` 数据集或可执行检查度量，不做主观判断。

| 维度 | 当前基线（2026-09-23） | v1.0 目标 |
|------|----------------------|-----------|
| 规则检出率（数据集） | 100%（20 样本，偏易） | ≥ 85%（≥ 60 样本，含 hard 层） |
| 精确率（误报率） | 100% / 0% | ≥ 90% / ≤ 10% |
| 敏感信息脱敏 | **2 处泄漏（P0-1）**，门禁 SKIP | 0 泄漏，脱敏测试为硬门禁 |
| 框架接入 | 2 处浅引用（local、sqlite 初始化） | skill / permission / container / e2b / artifact / telemetry 真接入 |
| LLM 能力 | 无 | `--fake-model` 确定性模式 + LLM 复核降噪（可开关） |
| 服务形态 | 单机 CLI | CLI + MCP server（可被 Claude Code/Cursor 调用） |
| CI / 自举 | 无 | GitHub Actions：测试 + 数据集门禁 + 用本工具审查本仓库 PR |
| 测试 | 10 包全绿 | 全绿 + 数据集门禁 + `-race`，门禁红不合代码 |

---

## 二、里程碑计划表

每个里程碑：一个分支一串提交，结束时打 tag、更新 §四指标看板和 PROJECT_GUIDE 对应章节。任务编号（A1/B2/C1…）对应 PROJECT_GUIDE §七扩展路线图。

### M0 · 修复与质量门禁（2026-09-24 → 09-30，约 6h）

| 任务 | 产出 |
|------|------|
| A1 统一 Redactor：finding evidence 落报告/落库前统一过 `safety.MaskSensitiveInfo` | 修掉 P0-1 |
| A2 `--output` 目录自动创建（`os.MkdirAll`） | 修掉 P0-2 |
| A3 local 沙箱接住 `RunProgram` 错误；A4 监控统计修正（TimedOut/ToolCallCount） | 修 P1-4/P1-5 |
| A5 加 `--audit-file`，安全审计 JSONL 默认可落盘 | 修 P1-6 |
| A6 补 LICENSE（Apache-2.0） | 合规 |
| 基础 CI：GitHub Actions 跑 `go test -race` + `go vet` + 数据集门禁 | D1 前半 |

**退出标准**：`go test ./... -race` 全绿；`TestDatasetRedaction` 从 SKIP 变 PASS（泄漏归零后自动转硬门禁）；数据集 v0 指标不回退。

### M1 · 框架真接入（2026-10-01 → 10-15，约 14h）

| 任务 | 产出 |
|------|------|
| B1 `skill.NewFSRepository` 启动时加载 SKILL.md，skill 名/版本写入报告 | 答辩核心问题的正面回答 |
| B2 `tool.PermissionPolicyFunc` 适配器包住 SafetyFilter，deny/ask 不进沙箱走框架语义 | 权限治理接入框架 |
| B5 artifact 链路：报告/沙箱日志写 `cr_artifacts`，加数量+大小+扩展名限制 | 补验收能力 8 缺口 |
| B6 OTel span attributes（review.task_id / tool.safety.decision 等） | 可观测起点 |

**退出标准**：报告含 skill 元数据；权限决策经框架 policy 且落库可查；artifact 表有记录且超限被拒。

### M2 · 数据集 v1 + 规则深化（2026-10-16 → 10-29，约 20h）

| 任务 | 产出 |
|------|------|
| 数据集扩到 ~50：新增 hard 层（间接数据流密钥、struct tag、跨 hunk 生命周期、生成文件排除、别名导入陷阱） | 质量标尺变硬 |
| D4 DB 生命周期专门规则（Begin→Commit/Rollback 配对） | 补齐 7 类规则最后一类 |
| D5 `--files` 文件路径列表输入 | 补齐能力 4 缺口 |
| finding 增加 evidence_chain 字段（hunk→fact→规则→置信度依据） | 可解释性 |

**退出标准**：v1 数据集 recall ≥ 85%、precision ≥ 90%、负样本误报率 ≤ 10%；新规则有正负样本覆盖；hard 样本标注先于实现手写。

### M3 · 沙箱生产化（2026-10-30 → 11-12，约 16h）

| 任务 | 产出 |
|------|------|
| B3/B4 沙箱后端：评估接框架 `codeexecutor/container` 子模块（保留手写版对比），实现 E2B 后端（`E2B_API_KEY` 开关） | 生产级隔离 |
| D2 staticcheck 接入：沙箱命令列表 + 输出解析为 findings（`source: "tool:staticcheck"`） | 工具链补充 |
| 沙箱限制测试：超时/输出截断/env 白名单/网络隔离用例 | 安全边界有测试 |

**退出标准**：`--repo-path` 全链路在 Docker 沙箱下可跑通；E2B 在有 key 的环境可跑通；staticcheck 发现出现在报告中。

### M4 · Agent 升级（2026-11-13 → 11-26，约 24h）

| 任务 | 产出 |
|------|------|
| C2 `--fake-model`：框架 `test.QueueModel` 确定性回放，无 API Key 跑全链路 | 官方硬要求补齐 |
| C1 LLM 复核降噪：规则产出候选 → LLM 逐条复核 → 调整 confidence（model/openai 或 ollama） | 核心价值升级 |
| C8 评测对照：LLM 开/关在数据集 v1 上的 precision/recall 对比入报告 | 效果可量化 |

**退出标准**：`--fake-model` 全链路确定性且 ≤ 2 分钟；数据集 v1 上 LLM 复核使 precision ≥ 95% 且 recall 不降。

### M5 · 服务化与 v1.0（2026-11-27 → 12-10，约 16h）

| 任务 | 产出 |
|------|------|
| C6 MCP server：把 code_review 包成 MCP 工具 | Claude Code/Cursor 可直连 |
| D1 CI 自举：用本工具审查本仓库 PR diff（吃自己狗粮） | 实用性证明 |
| 文档收尾：fresh-clone 快速上手实测、示例更新 | 可用性 |
| 打 `v1.0.0` tag | 里程碑 |

**退出标准**：MCP 客户端可调用 code_review 工具并拿到结构化结果；CI 自举在 GitHub Actions 跑通；§一表格全列达标。

机动缓冲：2026-12-11 → 12-31（顺延或做 v1.1 backlog：C4/C5 Agent/Graph 编排、C7 PR 机器人、C9 记忆降噪、D3 go/types、D8 PatchView 语义层重构）。

---

## 三、数据集演进计划

| 版本 | 时间点 | 规模 | 内容 | 对应质量门禁 |
|------|--------|------|------|-------------|
| v0（已完成） | 2026-09-23 | 20 | easy/medium 正样本 + trap 陷阱负样本 + 脱敏样本 | recall ≥ 80%，precision ≥ 85%，negFPR ≤ 15%，脱敏 SKIP→PASS |
| v1 | M2 | ~50 | +hard 难度层、DB 生命周期、evidence_chain 断言 | recall ≥ 85%，precision ≥ 90%，negFPR ≤ 10%，脱敏 0 泄漏 |
| v2 | M4 | ~60 | +LLM 复核对照样本（误报样本预期被 LLM 降置信度） | 同 v1 + LLM 开启后 precision ≥ 95% |

**标注纪律**：hard 样本先手写标注再跑引擎（防止"照抄实现"导致数据集失去检验能力）；每个里程碑结束后抽查标注与实现的独立性。

---

## 四、指标看板（每个里程碑结束时更新此表）

| 指标 | 基线 09-23 | M0 门禁 | M2 门禁 | v1.0 门禁 | 当前实际 |
|------|-----------|---------|---------|-----------|---------|
| 数据集样本数 | 20 | 20 | ≥ 50 | ≥ 60 | **20** |
| 检出率 recall | 100% | ≥ 80% | ≥ 85% | ≥ 85% | **100%** |
| 精确率 precision | 100% | ≥ 85% | ≥ 90% | ≥ 90% | **100%** |
| 负样本误报率 | 0% | ≤ 15% | ≤ 10% | ≤ 10% | **0%** |
| 脱敏泄漏 | 2（P0-1） | 0（硬门禁） | 0 | 0 | **2** |
| 红色项 | 脱敏 | — | — | — | P0-1 待修 |

更新方法：跑 `go test -run TestDataset -v .`，把"数据集质量报告"数字填入"当前实际"列。

---

## 五、风险与对策

| 风险 | 对策 |
|------|------|
| 业余时间不足，里程碑顺延 | 优先保 M0（质量门禁）和 M2（数据集变硬）；M3/M4/M5 可各自独立顺延不阻塞 |
| 规则过拟合数据集（指标好看但隐藏样本拉胯） | hard 样本标注先于实现手写；v1 起保留 20% 样本作为不常跑的"私有隐藏集" |
| E2B/LLM 外部依赖不可用 | E2B 无 key 自动 skip 集成；LLM 用 fake-model 和 ollama 本地兜底 |
| CI 环境 CGO（go-sqlite3） | 先用 ubuntu runner + clang；M3 评估 modernc.org/sqlite 纯 Go 替换 |
| 框架版本升级破坏 API | go.mod 锁定 minor 版本；升级在独立分支跑全量门禁后再合 |

---

## 六、节奏约定

1. **门禁纪律**：数据集门禁红了不合代码；新增/修改规则必须带正负样本。
2. **提交纪律**：一个任务一个提交，message 引用任务编号（如 `fix(A1): unify evidence redaction`）。
3. **里程碑收尾**：打 tag（M0 → `v0.2.0`，M2 → `v0.3.0`，M5 → `v1.0.0`）+ 更新指标看板 + 同步 PROJECT_GUIDE §五/§六。
4. **文档即验收**：每个里程碑的退出标准都是可执行命令或可检查产物，不接受"应该可以了"。
