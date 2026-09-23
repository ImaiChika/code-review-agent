# Code Review Agent 全量项目指引

> 本文档是对本仓库的**全量体检报告 + 使用指引 + 扩展路线图**，基于 2026-09-22 对全部源码、测试和上层 trpc-agent-go v1.10.0 框架源码的逐文件通读产出。
>
> 阅读对象：未来的自己 / 想在此基础上继续开发的人 / 想快速理解这个项目的人。

---

## 目录

1. [项目是什么：背景与定位](#一项目是什么背景与定位)
2. [快速上手](#二快速上手)
3. [架构与数据流](#三架构与数据流)
4. [对 trpc-agent-go 框架的引用分析](#四对-trpc-agent-go-框架的引用分析)
5. [完成度全量检查（对照官方要求）](#五完成度全量检查对照官方要求)
6. [已知问题与技术债](#六已知问题与技术债)
7. [成熟度目标与演进路线图](#七成熟度目标与演进路线图)
8. [附录](#八附录)

---

## 一、项目是什么：背景与定位

### 1.1 竞赛背景

本仓库是**腾讯犀牛鸟 2026 开源人才计划 · tRPC-Agent 课题**的参赛作品。

tRPC-Agent 是腾讯开源的生产级 Agent 开发框架（Python + Go 双实现），计划共设 8 个课题（4 个 Go + 4 个对应的 Python，题目一一对应）：

| # | Go 版课题 | 对应 Python 版 |
|---|----------|---------------|
| 1 | **基于 Skills + 沙箱 + 数据库存储构建自动代码评审 Agent**（← 本项目） | 项目 5 |
| 2 | Evaluation + Optimization 自动回归与提示词优化闭环 | 项目 6 |
| 3 | Tool 执行安全扫描、Filter/Permission 拦截与监控 | 项目 7 |
| 4 | Session/Memory 多后端回放一致性测试框架 | 项目 8 |

题目原文在 `trpc.txt`（根目录，本地开发资料，不提交 git）和 `官方资料/` 目录中。

### 1.2 一句话定位

**输入 git diff → Token 感知规则引擎审查 → （可选）沙箱执行 go vet / go test → 结构化 findings 去重降噪 → 风险评分 → JSON + Markdown 报告 → SQLite 落库审计。**

题目反复强调的核心难点：这**不是**"让 LLM 评论代码"，而是把 Skills、沙箱执行、数据库、治理策略（Permission/Filter）、审查规则、结果结构化、监控审计和安全边界串成一个**可验证、可复现、可审计**的系统。本项目的实现思路是：核心审查逻辑用**确定性规则引擎**（go/scanner 词法分析），LLM 不参与主链路——这符合题目"dry-run / deterministic rule-only 模式"的硬性要求。

### 1.3 仓库内文档索引（避免混淆）

| 文件 | 性质 | 说明 |
|------|------|------|
| `README.md` | ✅ 对外交付 | 使用说明、规则表、评分说明 |
| `DESIGN.md` | ✅ 对外交付 | 300-500 字方案设计说明（题目要求的交付物） |
| `PROJECT_GUIDE.md` | ✅ 对外交付 | **本文档**，全量指引 |
| `GUIDE.md` | 📝 开发笔记 | 开工前的 4 周计划 + 创新方案清单（已过时，仅存档，gitignored） |
| `任务解读手册.md` | 📝 开发笔记 | 对 trpc.txt 的逐条白话解读（gitignored） |
| `project_gap_and_innovation_review.txt` | 📝 开发笔记 | 中期差距评估 + Patch-Aware 语义引擎创新方案（很有价值，建议保留） |
| `repair.txt` / `teach.txt` / `review.txt` | 📝 开发笔记 | 修复计划 / 答辩建议 / 腾讯 PR 的 CI 报错日志（gitignored） |
| `pr_description.md` | 📝 开发笔记 | 提交给腾讯仓库的 PR #2374 描述（PR 已关闭，仅存档） |
| `官方资料/` | 📝 参考资料 | 赛题 PPTX + trpc.txt 副本（含腾讯版权材料，gitignored，勿公开） |

---

## 二、快速上手

### 2.1 环境要求

| 依赖 | 要求 | 用途 |
|------|------|------|
| Go | ≥ 1.21 | 构建 |
| CGO | 需要（clang/gcc） | `mattn/go-sqlite3` 是 CGO 库；macOS 自带 clang 即可 |
| Docker | 可选 | container 沙箱模式（`--sandbox container`，默认）；不可用会自动回退 local |
| git | 可选 | `--repo-path` 模式执行 `git diff` |

### 2.2 构建与测试（2026-09-22 实测通过）

```bash
# 构建
go build -o code-review-agent .

# 全量测试（10 个包全部 PASS）
go test ./... -count=1

# 竞态检测 + vet（提交 PR 时的验收命令）
go test ./... -count=1 -race
go vet ./...
gofmt -l .
```

### 2.3 运行

```bash
# 审查一个 diff 文件（最常用，纯规则审查，不碰沙箱和数据库则加 --dry-run）
./code-review-agent --diff-file testdata/security_issue.diff --verbose

# dry-run：不写数据库、不执行沙箱
./code-review-agent --diff-file testdata/security_issue.diff --dry-run

# 审查一个 git 仓库的未提交变更，并在沙箱里跑 go vet / go test，结果落库
./code-review-agent --repo-path /path/to/repo --sandbox container --verbose

# 加载自定义 YAML 规则
./code-review-agent --diff-file x.diff --rules-dir ./rules/custom
```

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `--diff-file` | - | diff 文件路径（与 `--repo-path` 二选一） |
| `--repo-path` | - | git 仓库路径，取工作区变更；**只有此模式才会触发沙箱执行** |
| `--rules-dir` | - | YAML 自定义规则目录 |
| `--db` | `review.db` | SQLite 路径 |
| `--output` | `.` | 报告输出目录（**必须已存在**，见已知问题 #1） |
| `--sandbox` | `container` | `container` / `local`（local 仅开发 fallback） |
| `--dry-run` | false | 不写库、不跑沙箱 |
| `--verbose` | false | 详细输出 |

### 2.4 产物

每次审查输出：

- `review_report.json` / `review_report.md` — 报告（示例见 `report/example_report.*` 和根目录样例）
- `review.db` — SQLite，6 张表（`cr_review_tasks` / `cr_findings` / `cr_sandbox_runs` / `cr_permission_decisions` / `cr_reports` / `cr_artifacts`），按 task_id 可查询完整链路：

```bash
sqlite3 review.db "SELECT task_id, status, input_path FROM cr_review_tasks ORDER BY started_at DESC LIMIT 5;"
sqlite3 review.db "SELECT severity, rule_id, file_path, line FROM cr_findings WHERE task_id='task-xxx';"
```

### 2.5 质量评测数据集

根目录 `dataset/` 是带 ground truth 标注的质量评测数据集（v0：20 样本 = 10 正 / 8 误报陷阱 / 2 脱敏），配套 harness `dataset_eval_test.go` 自动输出检出率/精确率/负样本误报率/脱敏泄漏四项指标并断言官方门禁：

```bash
go test -run TestDataset -v .   # 当前基线：recall 100%、precision 100%、negFPR 0%、脱敏 2 处泄漏（P0-1）
```

标注 schema、指标定义、新增样本流程见 `dataset/README.md`；成熟度里程碑与指标看板见本文 §七。

---

## 三、架构与数据流

### 3.1 目录结构

```
code-review-agent/
├── main.go                 # CLI 入口，串联 8 步流程
├── integration_test.go     # 端到端验收测试（遍历 testdata/*.diff）
├── diff/                   # unified diff 解析（hunk/行号/包名提取/git diff 调用）
├── analyzer/               # 两层分析器
│   ├── token.go            #   go/scanner 词法分析 → TokenFact（规则引擎实际使用的层）
│   └── analyzer.go         #   go/ast 完整文件分析（目前休眠，未接入规则，见 §6.2）
├── rules/                  # 规则引擎
│   ├── rule.go             #   Rule / MultiFileRule 接口
│   ├── engine.go           #   注册与调度（先跑多文件规则，再跑单文件规则 × 每文件）
│   ├── token_rules.go      #   6 条内置 Token 感知规则（核心资产，~980 行）
│   ├── dsl.go              #   YAML 规则 DSL 加载器（token_facts 匹配）
│   └── custom/security.yaml#   自定义规则示例
├── findings/               # Finding 结构体 + 去重分组（file+line+category+rule_id 为键）
├── scoring/                # 0-100 风险评分，6 维度加权，A-F 等级
├── safety/                 # 安全治理
│   ├── filter.go           #   7 层命令安全过滤（allow/deny/ask）+ JSONL 审计日志
│   └── mask.go             #   10 种敏感信息脱敏正则
├── sandbox/                # 沙箱执行
│   ├── sandbox.go          #   Sandbox 接口
│   ├── local.go            #   本地执行：包装 trpc-agent-go codeexecutor/local
│   └── container.go        #   Docker 执行：手写 docker run（网络隔离/只读/非root/限资源）
├── storage/                # SQLite 存储（Store 接口 + 6 张表 CRUD）
├── report/                 # JSON + Markdown 报告生成（10 个章节）
├── skills/code-review/     # CR Skill：SKILL.md + RULES.md + scripts/run_review.sh
├── testdata/               # 9 个 diff fixture（官方要求 8 个 + sensitive_info）
└── Dockerfile              # 沙箱镜像（golang:1.21-alpine + staticcheck/golangci-lint）
```

### 3.2 主流程（main.go 的 8 步）

```
Step 1  读 diff        --diff-file（读文件）或 --repo-path（exec git diff）
Step 2  初始化规则引擎  6 条内置规则 + 可选 YAML DSL 规则
Step 3  规则审查       engine.Run(files) → 原始 findings
Step 3.5 沙箱执行      仅 --repo-path 且非 dry-run 时：
                        对 "go vet ./..." 和 "go test -count=1 -timeout=30s ./..."
                        先过 safety.Filter（deny/ask 直接拦截并记录）
                        再进 Sandbox（container 优先，失败回退 local）
Step 4  去重降噪       同 file+line+category+rule_id 保留最高置信度；
                        confidence ≥ 0.7 → findings，< 0.7 → warnings（人工复核）
Step 5  风险评分       6 维度加权 0-100 分 + A-F 等级
Step 6  生成报告       review_report.json + review_report.md
Step 7  落库           task / findings / sandbox_runs / permission_decisions / reports
Step 8  汇总输出
```

### 3.3 六条内置规则（项目核心资产）

| 规则 ID | 检测 | 机制要点 | 置信度 |
|---------|------|---------|--------|
| SEC-AST-001 | 硬编码密钥 | TokenAnalysis.GetAssignedValue()：敏感标识符（password/apikey/...）+ 赋值 + 字符串字面量；占位符白名单（your-/example/test/...）排除 | 0.90 |
| SEC-AST-002 | 敏感信息泄漏 | 字符串字面量匹配 AKIA/ghp_/sk_live_/xox/JWT(eyJ..)/私钥头/DB 连接串/URL 内嵌凭据；evidence 先脱敏再入报告 | 0.85-0.99 |
| GOR-AST-001 | goroutine 泄漏 | hunk 内有 `go` 语句 token，且无 select/ctx/cancel/stop/errgroup 等退出标识 | 0.85 |
| RES-AST-001 | 资源泄漏 | 14 种 open 调用（os.Open/http.Get/sql.Query/net.Dial...）在 hunk 内找不到对应 Close | 0.80 |
| ERR-AST-001 | 错误处理 | `_` 丢弃返回值 / panic / 库代码 log.Fatal / `if err != nil` 后 `return nil`（跨行） | 0.75-0.85 |
| TST-AST-001 | 测试缺失 | MultiFileRule：新增导出函数在所有测试文件中找不到 Test* 对应 | 0.65 |

所有规则只扫**新增行**（`fd.AddedLines()`），不报旧代码的问题——这是 diff 审查的正确语义。

### 3.4 YAML 规则 DSL 示例

```yaml
rules:
  - id: MY-001
    name: "检测硬编码端口"
    severity: medium
    category: security
    match:
      token_facts:                # 基于 go/scanner 的 token 匹配，不是纯正则
        - kind: identifier
          value_contains: ["port"]
        - kind: string_literal
          value_pattern: "^\\d{4,5}$"
    exclude:
      line_contains: ["localhost", "127.0.0.1"]
    message: "疑似硬编码端口号"
    recommendation: "使用配置文件管理端口"
    confidence: 0.75
```

---

## 四、对 trpc-agent-go 框架的引用分析

上层框架源码在 `../trpc-agent-go`（v1.10.0，与本项目并排）。本节回答三个问题：**真用了什么、借鉴了什么但自实现、有什么可用但没用**。

### 4.1 真正 import 的框架代码（3 处）

| 框架模块 | 本项目落点 | 用法 |
|---------|-----------|------|
| `codeexecutor` + `codeexecutor/local`（主模块） | `sandbox/local.go` | `local.NewRuntimeWithOptions(workRoot, local.WithRuntimeWorkspaceMode(local.WorkspaceModeTrustedLocal))`，执行时构造 `codeexecutor.RunProgramSpec{Cmd:"sh", Args:["-c",command], Cwd, Env, Timeout}` 调 `runtime.RunProgram(ctx, ws, spec)`，得到 `RunResult{Stdout,Stderr,ExitCode,TimedOut}` |
| `session/sqlite`（独立子模块） | `storage/storage.go` | `sqlite.NewService(db, sqlite.WithTablePrefix("cr_"))` 初始化数据库基础设施（会建框架自己的 `cr_session_states`/`cr_session_events` 表）；业务表由本项目自建 |
| go.mod 依赖 | `trpc.group/trpc-go/trpc-agent-go v1.10.0` + `session/sqlite v1.10.0` | 其余大量间接依赖（otel、grpc、zap 等）由框架带入 |

**注意**：`session/sqlite` 的 Service 实际只起了"初始化 + 关连接"的作用，会话存取能力（events/state/TTL）完全没用上；业务 CRUD 走的是裸 `database/sql`。这是"形式性引用"，答辩如果被问"为什么用 session/sqlite 却不用它的 session 能力"要有准备（合理回答：借它的建库/生命周期管理，业务表自己设计；或干脆按 §7-B8 升级成真用）。

### 4.2 概念借鉴但自实现（未 import 框架对应物）

| 能力 | 框架提供 | 本项目实际做法 | 差距 |
|------|---------|--------------|------|
| 权限策略 | `tool/permission.go`：`PermissionPolicy`/`PermissionChecker` 接口、`PermissionActionAllow/Deny/Ask`、`PermissionPolicyFunc` 适配器 | `safety/filter.go` 自研 `SafetyFilter`，自有 `Decision allow/deny/ask` 枚举（**枚举值与框架语义对齐**），7 层检查 + JSONL 审计 | 未实现 `tool.PermissionPolicyFunc` 适配器，治理没接到框架工具链上 |
| Skill 加载 | `skill.NewFSRepository(roots...)` 解析 SKILL.md（front matter + 正文 + docs），`tool/skill.NewLoadTool/NewRunTool` 可 load/run | `skills/code-review/SKILL.md` **格式**遵循规范（front matter name/description/version），但程序运行时**不加载它**，纯文档 | SKILL.md 是"给人看的"，不是"给框架用的" |
| 容器沙箱 | `codeexecutor/container`（独立子模块）：`container.New(WithDockerFilePath/WithBindMount...)`，走 Docker API | `sandbox/container.go` 手写 `exec docker run --network=none --memory=512m --cpus=1 --read-only --tmpfs /tmp --user 65532:65532` | 未用框架 container 子模块；但手写版的安全 flags 反而更"狠"（框架默认镜像还是 python:3.9-slim） |
| 命令安全解析 | `internal/shellsafe`：`Parse(command)` 手写 shell lexer + `Policy{Allow,Deny}`（**internal 包，外部仓库无法 import**） | `safety/filter.go` 用 `strings.Contains` + 词边界校验 | shellsafe 本来就 import 不到，自实现是合理的；但解析强度弱于真 lexer（见 §6.7） |
| 产物管理 | `artifact.Service`（Save/Load/ListVersions），实现有 inmemory/s3/cos | `storage` 里自建 `cr_artifacts` 表 + `Store.SaveArtifact` 接口（已预留，main.go 尚未调用） | 接口已留，链路未通 |

### 4.3 框架有、本项目完全没碰的能力（扩展空间）

一览表，详细落地方案见 §7：

| 框架能力 | 位置 | 对本项目的潜在价值 |
|---------|------|------------------|
| LLM 模型层 | `model/{openai,anthropic,gemini,ollama,hunyuan,bedrock}` + `failover`/`hedge` | 让 LLM 复核规则引擎的候选 findings、生成修复建议 |
| Fake 模型 | `test` 子模块 `QueueModel`（Push 回放） | 落实官方要求的 `--fake-model` 可复现模式 |
| Agent/Runner | `agent/llmagent.New(name, WithModel, WithTools)` + `runner.NewRunner(appName, agent)` | 从"CLI 管道"升级为"真 Agent" |
| 图编排 | `graph/`（StateGraph、checkpoint、interrupt/resume） | review 流水线可视化编排；ask 决策挂 interrupt 人工介入 |
| MCP | `tool/mcp.NewMCPToolSet`（客户端）+ `server/`（服务端） | 把本工具包成 MCP server，Claude Code/Cursor 直接调用 |
| A2A / AG-UI / OpenAI 兼容 server | `server/{a2a,agui,openai,trpcagent}` | 服务化：PR webhook → 自动审查 → 回评 |
| 评测框架 | `evaluation/`（evalset/metric/LLM rubric/promptiter） | 隐藏样本 precision/recall 评测，回应"检出率≥80%、误报≤15%" |
| OTel 可观测 | `telemetry/trace.Start` + langfuse | span attributes：review.task_id / tool.safety.decision 等 |
| 记忆/知识 | `memory/`（含 sqlitevec）/ `knowledge/`（RAG） | 历史审查经验沉淀、"这条 warning 上次被忽略"式降噪 |
| E2B 云沙箱 | `codeexecutor/e2b.New`（需 `E2B_API_KEY`） | 无 Docker 环境的生产沙箱方案 |

---

## 五、完成度全量检查（对照官方要求）

### 5.1 官方 9 大能力

| # | 能力 | 状态 | 证据 / 差距 |
|---|------|------|------------|
| 1 | CR Skill（SKILL.md + 规则文档 + 脚本） | 🟡 | `skills/code-review/` 三件套齐全；但未用 `skill.NewFSRepository` 加载，规则 ≥4 类要求达成（实际覆盖 6 类，DB 生命周期由 RES-AST-001 间接覆盖，无专门的 Begin/Commit/Rollback 规则） |
| 2 | 沙箱执行（container/e2b，local 仅 fallback） | 🟡 | container 已实现（手写 docker run，网络/内存/CPU/只读/非 root 全套隔离），Docker 不可用自动回退 local；**e2b 未实现**（SKILL.md 却声称支持，文档与实现不符） |
| 3 | 工具链接入（高风险命令先过 PermissionPolicy） | ✅ | `main.go` 中每条沙箱命令先 `filter.Check()`，deny/ask 不进沙箱并记录 `cr_permission_decisions`；但仅覆盖 2 条固定命令 |
| 4 | 输入解析（unified diff / 文件列表 / git 工作区） | 🟡 | diff 文件 ✅、git 工作区 ✅（`ReadFromGitDiff` 支持 range 参数）；**文件路径列表输入未实现** |
| 5 | 结构化 findings（10 个字段） | ✅ | severity/category/file/line/title/evidence/recommendation/confidence/source/rule_id 全齐 |
| 6 | 数据库存储（task/sandbox/permission/finding/report + 接口可换后端） | ✅ | 6 张表 + `Store` 接口 + 按 task_id 查询（`GetTaskSummary` 等）；artifact 表已建但 main 未写入 |
| 7 | 去重降噪 | ✅ | file+line+category+rule_id 去重保留最高置信度；<0.7 进 warnings 人工复核 |
| 8 | 安全边界（超时/输出限制/env 白名单/脱敏/artifact 限制/失败记录） | 🟡 | 超时 ✅、输出 1MB 截断 ✅、env 白名单 ✅（container 模式生效）、脱敏 🟡（沙箱输出脱敏 ✅，**finding evidence 有明文密钥泄漏路径**，见 §6.1）、artifact 限制 ❌、失败记录 ✅（不崩溃，`sandbox_failure.diff` 有测） |
| 9 | 监控审计 | 🟡 | Monitor 字段齐全（总耗时/规则耗时/沙箱耗时/拦截数/异常数/评分）；但 ToolCallCount 语义错、severity 分布只在报告里有、**审计 JSONL 默认不落盘**、无 OTel |

### 5.2 官方 8 条验收标准

| # | 标准 | 状态 |
|---|------|------|
| 1 | 8 条 diff 样本全部可运行 | ✅ 9 个 fixture（多的 sensitive_info），`integration_test.go` 全遍历 |
| 2 | 高危检出率 ≥ 80%（隐藏样本） | 🟡 无评测集佐证，仅自测置信度设计 |
| 3 | 误报率 ≤ 15%（隐藏样本） | 🟡 同上；占位符白名单/词边界校验是正向设计 |
| 4 | 数据库完整记录 + 按 task id 查询 | ✅（artifact 链路除外） |
| 5 | 沙箱超时/失败不崩溃 | ✅ context.WithTimeout + 容错记录 |
| 6 | 脱敏检出率 ≥ 95%，报告和 DB **无明文密钥** | 🔴 **报告/DB 中 evidence 存在明文密钥**（SEC-AST-001 的 evidence 是原始代码行），见 §6.1 |
| 7 | dry-run ≤ 2 分钟 | ✅ 实测毫秒级 |
| 8 | 高风险命令 deny/ask 不进沙箱 | ✅（但命令面窄） |

### 5.3 交付物清单

| 交付物 | 状态 |
|--------|------|
| main.go / CLI | ✅ |
| SKILL.md + 规则文档 + 脚本 | ✅ |
| DB schema + 存储实现 | ✅（无独立 migration 文件，建表在 `initTables()`） |
| ≥8 条测试样例 | ✅ 9 条 |
| review_report.json/md 示例 | ✅ |
| README | ✅ |
| 300-500 字设计说明 | ✅ DESIGN.md |
| 单测覆盖（diff 解析/去重/脱敏/落库/沙箱失败） | ✅ 10 包全绿，含 `-race` |

**总体判断：交付物形态完整，验收 8 条中 6 条扎实、2 条（脱敏入库、检出率佐证）有实质缺口。**

---

## 六、已知问题与技术债

按严重度排序，均已在源码中定位（2026-09-22 实测验证）：

### 🔴 P0-1 finding evidence 明文密钥进报告和数据库

`rules/token_rules.go:68-77`（SEC-AST-001）：`evidence` 直接用原始代码行 `content`，**硬编码密钥的明文会原样出现在 review_report.md/json 和 `cr_findings.evidence` 表里**。实测：审查 `security_issue.diff`，报告里出现 `APIKey string = "sk-abc123secretkey2024"` 明文。SEC-AST-002 有 `sanitizeTokenEvidence()` 做了脱敏，SEC-AST-001 没有。直接违反验收标准 6。

**修法**：统一 Redactor——所有 `NewFinding(...)` 的 evidence / recommendation 在落报告和落库前过 `safety.MaskSensitiveInfo()`（一处修改：`report.SetResult()` 和 `storage.SaveFindings()` 入口，或干脆在 `findings.NewFinding` 里做）。

### 🔴 P0-2 `--output` 目录不存在直接 fatal

`main.go:345` 写报告前没有 `os.MkdirAll(*outputDir)`，实测 `--output /tmp/new-dir` 直接 `log.Fatalf`。**修法**：main.go 读参后加一行 `os.MkdirAll(*outputDir, 0755)`。

### 🟡 P1-3 AST 分析层是休眠代码

`analyzer/analyzer.go`（go/ast 层）没有任何规则引用——DESIGN.md 宣称"双层分析策略"，实际跑的只有 token 层。要么把 AST 层接进规则（`--repo-path` 模式下读完整文件做深度分析），要么在文档里降级表述，别留着被质询。

### 🟡 P1-4 本地沙箱吞执行错误

`sandbox/local.go:92`：`runResult, _ := s.runtime.RunProgram(...)`——框架返回的 error 被丢弃，运行时崩溃（如 sh 不存在）会被当成"退出码非 0"处理而非异常。container.go 同位置有错误分支但 local 没有。

### 🟡 P1-5 监控统计两处失真

- `main.go:296`：`sandboxTimedOut` 声明后从未累加，`SetSandboxSummary` 永远收到 0（`result.TimedOut` 有值没用上）。
- `main.go:286`：`Monitor.ToolCallCount = len(allFindings)`——findings 数被当工具调用次数，语义错误。

### 🟡 P1-6 审计日志默认不落盘

`safety/filter.go` 的 `AuditLogger` 只在 `config.LogFile != ""` 时初始化，而 `main.go:150` 用 `NewSafetyFilter(nil)`（默认配置 LogFile 为空）→ 验收描述里的 `tool_safety_audit.jsonl` 实际不产生。**修法**：加 `--audit-file` 参数或默认写 `audit.jsonl`。

### 🟡 P1-7 安全过滤是字符串匹配，命令面窄

- 只有 2 条固定沙箱命令（`go vet`/`go test`，`main.go:179`）会过 filter；自定义命令入口都没有。
- `isDenied`/`hasShellInjection` 基于 `strings.Contains`，绕过空间大（如 `$(echo cm0gLXJmIA==|base64 -d)`）。框架 `internal/shellsafe` 是真 lexer 但 import 不到；长期解法见 §7-C13。

### 🟢 P2-8 其他小项

- `skills/code-review/SKILL.md:131` 声称支持 e2b 后端，实际未实现。
- dry-run 同时跳过沙箱**和**落库——官方原意是"dry-run 也要能测沙箱执行、落库链路"（无 API Key 可测），本实现反而测不到这两段。建议拆成 `--no-llm`（跳过 LLM，保留沙箱+落库）和 `--dry-run`（全跳）两个开关。
- `--fake-model` 参数在 GUIDE 计划里有、官方输入要求里有，未实现（当前本就无 LLM，暂无影响）。
- 无 LICENSE 文件（README 声称 Apache 2.0、源码带腾讯头，但仓库缺 LICENSE 正文）。
- `scoring.go:17` 注释说"代码质量 10% + 性能风险 10%"，实际维度是"敏感信息 15% + 并发 5%"，注释过时。
- `diff/parser.go` 的 `ReadFromGitDiff` 默认只看未暂存变更（`git diff`），暂存区/HEAD 对比需手动传 range，CLI 未暴露参数。
- `metadata.json` / `.claude/` 是开发工具残留（已 gitignore）。

---

## 七、成熟度目标与演进路线图

> 目标：把项目从"能跑的原型"推进到**未来可用的生产级工具**。本节是项目唯一的执行计划：7.2 里程碑给出节奏与退出标准，7.3 任务明细给出每个任务的落点、框架 API 和工作量，两表通过任务编号（A1/B2/C1…）互相引用。

### 7.1 "成熟"的定义（可量化验收门槛）

"未来可用"不是感觉，是这张表的最后一列。所有指标都用 `dataset/` 数据集或可执行检查度量，不做主观判断。

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

### 7.2 里程碑计划表（M0–M5）

每个里程碑：一个分支一串提交，结束时打 tag、更新 7.5 指标看板并同步 §五/§六 对应条目。工作量假设：业余时间每周 8–12 小时。

#### M0 · 修复与质量门禁（2026-09-24 → 09-30，约 6h）

| 任务 | 产出 |
|------|------|
| A1 统一 Redactor：finding evidence 落报告/落库前统一过 `safety.MaskSensitiveInfo` | 修掉 P0-1 |
| A2 `--output` 目录自动创建（`os.MkdirAll`） | 修掉 P0-2 |
| A3 local 沙箱接住 `RunProgram` 错误；A4 监控统计修正（TimedOut/ToolCallCount） | 修 P1-4/P1-5 |
| A5 加 `--audit-file`，安全审计 JSONL 默认可落盘 | 修 P1-6 |
| A6 补 LICENSE（Apache-2.0） | 合规 |
| 基础 CI：GitHub Actions 跑 `go test -race` + `go vet` + 数据集门禁 | D1 前半 |

**退出标准**：`go test ./... -race` 全绿；`TestDatasetRedaction` 从 SKIP 变 PASS（泄漏归零后自动转硬门禁）；数据集 v0 指标不回退。

#### M1 · 框架真接入（2026-10-01 → 10-15，约 14h）

| 任务 | 产出 |
|------|------|
| B1 `skill.NewFSRepository` 启动时加载 SKILL.md，skill 名/版本写入报告 | 答辩核心问题的正面回答 |
| B2 `tool.PermissionPolicyFunc` 适配器包住 SafetyFilter，deny/ask 不进沙箱走框架语义 | 权限治理接入框架 |
| B5 artifact 链路：报告/沙箱日志写 `cr_artifacts`，加数量+大小+扩展名限制 | 补验收能力 8 缺口 |
| B6 OTel span attributes（review.task_id / tool.safety.decision 等） | 可观测起点 |

**退出标准**：报告含 skill 元数据；权限决策经框架 policy 且落库可查；artifact 表有记录且超限被拒。

#### M2 · 数据集 v1 + 规则深化（2026-10-16 → 10-29，约 20h）

| 任务 | 产出 |
|------|------|
| 数据集扩到 ~50：新增 hard 层（间接数据流密钥、struct tag、跨 hunk 生命周期、生成文件排除、别名导入陷阱） | 质量标尺变硬 |
| D4 DB 生命周期专门规则（Begin→Commit/Rollback 配对） | 补齐 7 类规则最后一类 |
| D5 `--files` 文件路径列表输入 | 补齐能力 4 缺口 |
| finding 增加 evidence_chain 字段（hunk→fact→规则→置信度依据） | 可解释性 |

**退出标准**：v1 数据集 recall ≥ 85%、precision ≥ 90%、负样本误报率 ≤ 10%；新规则有正负样本覆盖；hard 样本标注先于实现手写。

#### M3 · 沙箱生产化（2026-10-30 → 11-12，约 16h）

| 任务 | 产出 |
|------|------|
| B3/B4 沙箱后端：评估接框架 `codeexecutor/container` 子模块（保留手写版对比），实现 E2B 后端（`E2B_API_KEY` 开关） | 生产级隔离 |
| D2 staticcheck 接入：沙箱命令列表 + 输出解析为 findings（`source: "tool:staticcheck"`） | 工具链补充 |
| 沙箱限制测试：超时/输出截断/env 白名单/网络隔离用例 | 安全边界有测试 |

**退出标准**：`--repo-path` 全链路在 Docker 沙箱下可跑通；E2B 在有 key 的环境可跑通；staticcheck 发现出现在报告中。

#### M4 · Agent 升级（2026-11-13 → 11-26，约 24h）

| 任务 | 产出 |
|------|------|
| C2 `--fake-model`：框架 `test.QueueModel` 确定性回放，无 API Key 跑全链路 | 官方硬要求补齐 |
| C1 LLM 复核降噪：规则产出候选 → LLM 逐条复核 → 调整 confidence（model/openai 或 ollama） | 核心价值升级 |
| C8 评测对照：LLM 开/关在数据集 v1 上的 precision/recall 对比入报告 | 效果可量化 |

**退出标准**：`--fake-model` 全链路确定性且 ≤ 2 分钟；数据集 v1 上 LLM 复核使 precision ≥ 95% 且 recall 不降。

#### M5 · 服务化与 v1.0（2026-11-27 → 12-10，约 16h）

| 任务 | 产出 |
|------|------|
| C6 MCP server：把 code_review 包成 MCP 工具 | Claude Code/Cursor 可直连 |
| D1 CI 自举：用本工具审查本仓库 PR diff（吃自己狗粮） | 实用性证明 |
| 文档收尾：fresh-clone 快速上手实测、示例更新 | 可用性 |
| 打 `v1.0.0` tag | 里程碑 |

**退出标准**：MCP 客户端可调用 code_review 工具并拿到结构化结果；CI 自举在 GitHub Actions 跑通；7.1 表格全列达标。

机动缓冲：2026-12-11 → 12-31（顺延或做 v1.1 backlog：C4/C5 Agent/Graph 编排、C7 PR 机器人、C9 记忆降噪、D3 go/types、D8 PatchView 语义层重构）。

### 7.3 扩展任务明细（A/B/C/D 层完整任务库）

> 7.2 只列每期重点；这里是完整任务库。排期映射：**M0** = A1–A6 + D1（基础 CI）；**M1** = B1/B2/B5/B6；**M2** = D4/D5 + 数据集 hard 层 + evidence_chain；**M3** = B3/B4/D2；**M4** = C1/C2/C8；**M5** = C6 + D1（自举）；其余为 backlog（v1.1 候选见上）。工作量：S=小时级，M=天级，L=周级。

#### A 层：修复与加固（先做，全是小改动）

| # | 扩展项 | 落点 | 工作量 |
|---|--------|------|--------|
| A1 | 统一 Redactor（修 P0-1） | `findings.NewFinding` 或 report/storage 入口统一过 `safety.MaskSensitiveInfo`；补一条"报告和 DB 无明文密钥"的测试 | S |
| A2 | 输出目录自动创建（修 P0-2） | `main.go` 加 `os.MkdirAll` | S |
| A3 | local 沙箱错误处理（修 P1-4） | `sandbox/local.go:92` 接住 error 返回 | S |
| A4 | 监控统计修正（修 P1-5） | `main.go` 累加 TimedOut；ToolCallCount 改为真实计数 | S |
| A5 | 审计日志默认开启（修 P1-6） | 加 `--audit-file` 参数 → `safety.LoadConfig` → `NewSafetyFilter(cfg)` | S |
| A6 | 补 LICENSE 文件 | Apache-2.0 正文 | S |

#### B 层：深度接入 trpc-agent-go（把"借鉴"变"真用"）

| # | 扩展项 | 做法 | 框架 API | 工作量 |
|---|--------|------|---------|--------|
| B1 | 真正加载 SKILL.md | 启动时 `repo, _ := skill.NewFSRepository("./skills")`，`repo.Get("code-review")` 校验存在并把 skill 名/版本写进报告 | `skill.NewFSRepository` / `skill.Repository` | S |
| B2 | SafetyFilter 接入框架权限体系 | 写一个适配器：`tool.PermissionPolicyFunc(func(ctx, req) (tool.PermissionDecision, error) {...})` 内部调 `filter.Check(string(req.Arguments))`，返回 `tool.AllowPermission()/DenyPermission()/AskPermission()` | `tool.PermissionPolicyFunc` | S |
| B3 | 换框架 container 沙箱 | go.mod 加 `trpc.group/trpc-go/trpc-agent-go/codeexecutor/container` 子模块；`container.New(container.WithBindMount(repoPath, "/workspace"))` 替换手写 docker run；保留手写版做对比 | `container.New` + `Engine()` | M |
| B4 | E2B 云沙箱后端 | `sandbox/e2b.go`：`e2b.New(e2b.WithAPIKey(os.Getenv("E2B_API_KEY")))` 实现 `Sandbox` 接口（接口已留好，`Name() "e2b"`） | `e2b.New` | M |
| B5 | artifact 链路打通 | 用已建好的 `cr_artifacts` 表（或 `artifact/inmemory.NewService()`）保存报告/沙箱日志，加数量+大小+扩展名限制（验收要求） | `artifact.Service` | S |
| B6 | OTel 埋点 | `telemetry/trace.Start(ctx)` 起全局 tracer；review 主流程一个 span，attributes：`review.task_id`、`review.findings_total`、`sandbox.backend`、`tool.safety.decision`、`tool.safety.rule_id` | `telemetry/trace` | M |
| B7 | skill run 执行脚本 | 把 `skills/code-review/scripts/run_review.sh` 真正用 `tool/skill.NewRunTool(repo, codeExecutor)` 跑起来，替代 main.go 里硬编码的两条命令 | `tool/skill.NewRunTool` | M |
| B8 | session/sqlite 真用起来 | 把每次 review 存成一个 session（事件流：解析→规则→拦截→执行→报告），支持回放 | `session/sqlite.Service` | M |

#### C 层：从"规则引擎"升级为"真 Agent"（框架最有价值的能力）

| # | 扩展项 | 做法 | 工作量 |
|---|--------|------|--------|
| C1 | **LLM 复核降噪**（最推荐） | 规则引擎产出候选 findings → LLM 逐条复核（"这是真问题吗"）→ 调 confidence。规则保证召回，LLM 保证精度，直接回应"误报率 ≤ 15%"。模型选 `model/openai` 或本地 `model/ollama` | L |
| C2 | **fake model 模式** | 引入框架 `test` 子模块的 `QueueModel`（Push 预设回复按序回放）实现 `--fake-model`，无 API Key 全链路可测可复现（官方硬要求） | S |
| C3 | LLM 修复建议生成 | 对每条 finding 让 LLM 生成带上下文 patch 的修复建议，替代静态 recommendation 文案 | M |
| C4 | Agent 化编排 | `llmagent.New("reviewer", llmagent.WithModel(m), llmagent.WithTools(reviewTools))` + `runner.NewRunner("code-review", agent)`；把"解析/规则/沙箱/评分"注册成 FunctionTool | L |
| C5 | GraphAgent 流水线 | `graph/` StateGraph 编排：parse → rules → sandbox → LLM 复核 → scoring → report；`ask` 决策挂 **interrupt/resume** 人工审批后继续——这是框架 checkpoint/interrupt 的教科书场景 | L |
| C6 | **包成 MCP server** | 让 Claude Code / Cursor 等 MCP 客户端直接调 `code_review` 工具审查当前 diff；框架 `server/` 有 MCP 服务端支持 | M |
| C7 | 服务化（PR 机器人） | `server/a2a` 或 `server/openai`（OpenAI 兼容 API）暴露服务；接 GitHub webhook 实现 PR 自动审查回评 | L |
| C8 | **评测集** | 用框架 `evaluation/` 建 evalset：N 个已知问题的 diff + 期望 findings，跑出 precision/recall/FPR，量化"检出率 ≥80%、误报 ≤15%"；LLM rubric 可评报告质量 | M |
| C9 | 记忆降噪 | `memory/sqlitevec`：记录"某文件某规则的历史 finding 被人工标记误报"，下次降置信度 | M |
| C10 | prompt 迭代优化 | `evaluation/workflow/promptiter` 优化 C1/C3 的复核 prompt，防过拟合 | L |

#### D 层：工程化增强（与框架无关但实用）

| # | 扩展项 | 说明 | 工作量 |
|---|--------|------|--------|
| D1 | GitHub Actions CI | `go test -race` + vet + **自举**：用本工具审查本仓库 PR 的 diff（吃自己的狗粮） | S |
| D2 | 接入更多静态工具 | Dockerfile 已预装 staticcheck/golangci-lint 但 CLI 没用——沙箱命令列表加 `staticcheck ./...`，输出解析成 findings（`source: "tool:staticcheck"`） | M |
| D3 | go/types 类型增强 | `--repo-path` 模式下加载包类型信息：资源变量是否实现 io.Closer、函数是否真返回 error——把 ERR/RES 规则从"猜"变"知道" | L |
| D4 | DB 生命周期专门规则 | Begin→Commit/Rollback 配对检查（题目 7 类规则里唯一没专门做的） | S |
| D5 | 文件路径列表输入 | 补 `--files a.go,b.go` 第三种输入模式（能力 4 缺口） | S |
| D6 | HTML 交互式报告 | GUIDE 创新清单遗留项：可折叠 finding、severity 图表 | M |
| D7 | 规则热加载 / 增量审查 | fsnotify 监听 rules 目录；按上次审查结果只审增量 | M |
| D8 | 按 `project_gap_and_innovation_review.txt` 的 PatchView 方案重构语义层 | 该文档第六节的"变更视图层→语义事实层→规则层"设计是现成的进阶蓝图 | L |

### 7.4 数据集演进计划

| 版本 | 时间点 | 规模 | 内容 | 对应质量门禁 |
|------|--------|------|------|-------------|
| v0（已完成） | 2026-09-23 | 20 | easy/medium 正样本 + trap 陷阱负样本 + 脱敏样本 | recall ≥ 80%，precision ≥ 85%，negFPR ≤ 15%，脱敏 SKIP→PASS |
| v1 | M2 | ~50 | +hard 难度层、DB 生命周期、evidence_chain 断言 | recall ≥ 85%，precision ≥ 90%，negFPR ≤ 10%，脱敏 0 泄漏 |
| v2 | M4 | ~60 | +LLM 复核对照样本（误报样本预期被 LLM 降置信度） | 同 v1 + LLM 开启后 precision ≥ 95% |

**标注纪律**：hard 样本先手写标注再跑引擎（防止"照抄实现"导致数据集失去检验能力）；每个里程碑结束后抽查标注与实现的独立性。

### 7.5 指标看板（每个里程碑结束时更新此表）

| 指标 | 基线 09-23 | M0 门禁 | M2 门禁 | v1.0 门禁 | 当前实际 |
|------|-----------|---------|---------|-----------|---------|
| 数据集样本数 | 20 | 20 | ≥ 50 | ≥ 60 | **20** |
| 检出率 recall | 100% | ≥ 80% | ≥ 85% | ≥ 85% | **100%** |
| 精确率 precision | 100% | ≥ 85% | ≥ 90% | ≥ 90% | **100%** |
| 负样本误报率 | 0% | ≤ 15% | ≤ 10% | ≤ 10% | **0%** |
| 脱敏泄漏 | 2（P0-1） | 0（硬门禁） | 0 | 0 | **2** |
| 红色项 | 脱敏 | — | — | — | P0-1 待修 |

更新方法：跑 `go test -run TestDataset -v .`，把"数据集质量报告"数字填入"当前实际"列。

### 7.6 风险与对策

| 风险 | 对策 |
|------|------|
| 业余时间不足，里程碑顺延 | 优先保 M0（质量门禁）和 M2（数据集变硬）；M3/M4/M5 可各自独立顺延不阻塞 |
| 规则过拟合数据集（指标好看但隐藏样本拉胯） | hard 样本标注先于实现手写；v1 起保留 20% 样本作为不常跑的"私有隐藏集" |
| E2B/LLM 外部依赖不可用 | E2B 无 key 自动 skip 集成；LLM 用 fake-model 和 ollama 本地兜底 |
| CI 环境 CGO（go-sqlite3） | 先用 ubuntu runner + clang；M3 评估 modernc.org/sqlite 纯 Go 替换 |
| 框架版本升级破坏 API | go.mod 锁定 minor 版本；升级在独立分支跑全量门禁后再合 |

### 7.7 节奏约定

1. **门禁纪律**：数据集门禁红了不合代码；新增/修改规则必须带正负样本。
2. **提交纪律**：一个任务一个提交，message 引用任务编号（如 `fix(A1): unify evidence redaction`）。
3. **里程碑收尾**：打 tag（M0 → `v0.2.0`，M2 → `v0.3.0`，M5 → `v1.0.0`）+ 更新 7.5 指标看板 + 同步 §五/§六。
4. **文档即验收**：每个里程碑的退出标准都是可执行命令或可检查产物，不接受"应该可以了"。

---

## 八、附录

### 8.1 trpc-agent-go 框架能力速查（本项目相关部分）

| 模块 | 关键 API | 外部依赖 |
|------|---------|---------|
| `codeexecutor/local` | `local.NewRuntime(workRoot)` / `NewRuntimeWithOptions`；`Runtime.RunProgram(ctx, ws, RunProgramSpec{Cmd,Args,Env,Cwd,Timeout})` → `RunResult{Stdout,Stderr,ExitCode,Duration,TimedOut}` | 无 |
| `codeexecutor/container`（子模块） | `container.New(opts...)`，必须给 image 或 dockerFilePath；`WithBindMount/WithHost` | Docker daemon |
| `codeexecutor/e2b` | `e2b.New(e2b.WithAPIKey(k))`，env `E2B_API_KEY` | E2B 云服务 |
| `session/sqlite`（子模块） | `sqlite.NewService(db, WithTablePrefix/WithSessionTTL/...)`；**Service 拥有 db 并在 Close() 关闭** | CGO sqlite |
| `tool/permission.go` | `PermissionAction{allow,deny,ask}`；`PermissionPolicyFunc(fn)`；`Allow/Deny/AskPermission()`；`PermissionRequest{ToolName,Arguments,Metadata}` | 无 |
| `skill` + `tool/skill` | `skill.NewFSRepository(roots...)`；`tool/skill.NewLoadTool(repo)` / `NewRunTool(repo, codeExecutor)`（默认 5 分钟超时，自动导出 out/** 上限 20 文件） | 无 |
| `artifact` | `artifact.Service`：`SaveArtifact/LoadArtifact/ListVersions`；实现 inmemory / s3 / cos | - |
| `model/*` | openai/anthropic/gemini/ollama/hunyuan/bedrock + failover/hedge/tiktoken；fake：`test` 子模块 `QueueModel` | 各家 API Key |
| `agent` + `runner` | `llmagent.New(name, WithModel, WithTools)`；`runner.NewRunner(appName, agent)`；chain/parallel/cycle/graphagent 编排 | - |
| `tool/*` | function/mcp/workspaceexec/hostexec/codeexec/file/webfetch/todo/... 30+ 工具 | - |
| `telemetry` | `trace.Start(ctx)`；langfuse 导出器；SpanAttributePolicy 脱敏 | OTel collector（可选） |
| `evaluation`（子模块） | `evaluation.New(appName, runner)`；evalset/metric/toolmock/usersimulation/promptiter | - |
| `server/*` | a2a / agui / openai 兼容 / trpcagent / mcp | - |
| `graph` / `team` / `planner` / `knowledge` / `memory` | 图工作流（checkpoint/interrupt）/ Swarm 多 Agent / 规划器 / RAG / 长期记忆（含 sqlitevec） | - |

注意事项：

- `internal/shellsafe` 是 **internal 包**，外部仓库 import 不到，只能经 `tool/workspaceexec` 间接享受。
- `codeexecutor` 的 `Capabilities.SupportsCleanEnv` 是安全契约：依赖 CleanEnv 做 env 隔离的工具会 fail-closed。
- container/e2b/local 三个后端都实现了完整 workspace 语义（PutFiles/StageInputs/CollectOutputs），本项目目前只用了 RunProgram 这一个方法。

### 8.2 框架源码本地位置

```
/Users/imaichika/Documents/trpc-agent/trpc-agent-go    # v1.10.0 源码（与本项目并排）
├── examples/          # 官方示例：skill / skillrun / codeexecution / evaluation / session ...
├── docs/              # Session / Memory / Knowledge / Tools / MCP / A2A / Evaluation 文档
└── AGENTS.md          # 框架贡献指南
```

写扩展代码前先翻 `examples/` 对应目录，基本都有可抄的最小示例。

### 8.3 测试 fixture 清单

| fixture | 验证 | 期望 |
|---------|------|------|
| `no_issue.diff` | 干净代码 | 0 findings |
| `security_issue.diff` | 硬编码密钥 | ≥1 high |
| `sensitive_info.diff` | AWS/GH/JWT/私钥/DB串 | ≥3 findings |
| `goroutine_leak.diff` | goroutine 泄漏 | ≥1 finding |
| `resource_leak.diff` | 资源未关闭 | ≥1 finding |
| `db_lifecycle.diff` | DB 生命周期 | 由 RES-AST-001 覆盖 |
| `missing_test.diff` | 测试缺失 | ≥1 low |
| `duplicate_finding.diff` | 去重 | 去重后 ≤ 原始数 |
| `sandbox_failure.diff` | 容错 | 不崩溃 |

---

*本文档由全量源码通读 + 实测验证产出（构建 ✅ / vet ✅ / 10 包测试 ✅ / CLI 冒烟 ✅）。修改代码后请同步更新第五、六节的对应条目。*
