# Code Review Agent 全量项目指引

> 本文档是对本仓库的**全量体检报告 + 使用指引 + 扩展路线图**，基于 2026-09-22 对全部源码、测试和上层 trpc-agent-go v1.10.0 框架源码的逐文件通读产出。
>
> 最近更新：2026-09-28（v1.0 后全量测试 + v2-hard 数据集盲评 + M7/M8 上线与智能化规划）。
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
| `CONTRIBUTING.md` | ✅ 对外交付 | 贡献指南：构建/测试命令、Conventional Commits、hooks 安装、门禁纪律 |
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

> 提交前先执行一次 `bash scripts/install-hooks.sh` 安装本地钩子（提交信息格式 + gofmt 检查）；CI 门禁与提交规范详见 `CONTRIBUTING.md` 与 `.github/workflows/ci.yml`。

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

### 2.4 Web 服务与前端（M6-Part1，2026-09-25 起可用）

```bash
scripts/start.sh             # 一键启动（自动构建），默认 http://localhost:8080
scripts/stop.sh              # 一键关闭（PID 文件 + 按端口兜底）
PORT=9090 scripts/start.sh   # 自定义端口
# 等价手工命令: go build -o code-review-agent . && ./code-review-agent serve --port 8080
```

- **前端**：内嵌二进制的 SPA（`server/web/`，go:embed，无外部依赖），四个视图——总览看板 / 新建审查（一键载入 `testdata` 示例）/ 任务记录 / 规则引擎（含评分维度与业务管线展示）。
- **API**：`GET /api/health`、`POST /api/reviews`（**M7-F1 起异步**：入队即返回 `202 + {task_id, status:"queued"}`，结果轮询 `GET /api/tasks/{id}`；非法输入 400 / 无新增行 422 仍同步返回；**M7-F2 起受 IP 限流与可选认证保护**）、`GET /api/tasks`、`GET /api/tasks/{id}`（queued/running 进行中态由内存注册表提供，`report` 为 null；failed 任务含 `error_msg`）、`GET /api/tasks/{id}/report`、`GET /api/stats`、`GET /api/rules`、`GET /api/samples`。
- **架构关键**：CLI 与 API 共用 `review.Run()` 同一条管线，前端展示的就是真实业务逻辑；单二进制分发。
- **异步队列（M7-F1）**：`server/queue.go` worker 池（`--queue-workers`，默认 1 串行=SQLite 单写最稳，HTTP 已不被彼此阻塞）；单任务看门狗 `--task-timeout`（默认 10m，超时标 failed 并经 `CreateFailedTask` 补记 DB）；进行中状态在内存注册表（服务重启丢失未完成任务属预期）；runFn 可注入支撑状态机单测；runFn panic 被兜住不影响服务。
- **认证与边界（M7-F2）**：`server/auth.go`——`--auth-token` 启用写保护（读公开，前端 `?token=<token>` 链接自动保存）；IP 令牌桶限流（2 req/s burst 10）只包提交端点；请求体 ≤10MB（413）；安全响应头 + 连接层超时。全部默认关闭/宽松，不改变旧行为。

### 2.5 产物

每次审查输出：

- `review_report.json` / `review_report.md` — 报告（示例见 `report/example_report.*` 和根目录样例）
- `review.db` — SQLite，6 张表（`cr_review_tasks` / `cr_findings` / `cr_sandbox_runs` / `cr_permission_decisions` / `cr_reports` / `cr_artifacts`），按 task_id 可查询完整链路：

```bash
sqlite3 review.db "SELECT task_id, status, input_path FROM cr_review_tasks ORDER BY started_at DESC LIMIT 5;"
sqlite3 review.db "SELECT severity, rule_id, file_path, line FROM cr_findings WHERE task_id='task-xxx';"
```

### 2.5 质量评测数据集

根目录 `dataset/` 是带 ground truth 标注的质量评测数据集（当前 39 样本 = 20 v0 + 10 v1-hard + 9 v2-hard2 批次），配套 harness `dataset_eval_test.go` 自动输出检出率/精确率/负样本误报率/脱敏泄漏四项指标并断言官方门禁：

```bash
go test -run TestDataset -v .   # 当前基线：recall 100%、precision 100%、negFPR 0%、脱敏 0 泄漏（39 样本，门禁 85/90/10）
```

标注 schema、指标定义、新增样本流程见 `dataset/README.md`；成熟度里程碑与指标看板见本文 §七。

---

## 三、架构与数据流

### 3.1 目录结构

```
code-review-agent/
├── main.go                 # CLI 入口 + serve 子命令（薄壳，业务全在 review 包）
├── review/                 # ★ 审查管线：8 步流程唯一实现，CLI 与 HTTP API 共用
├── server/                 # ★ HTTP 服务：REST API + 内嵌 Web 前端（go:embed）
│   ├── queue.go            #   M7-F1 异步审查队列：worker 池 + 状态机 + 看门狗
│   └── web/                #   前端三件套（vanilla SPA，零外部依赖）
├── scripts/                # ★ start.sh / stop.sh 一键启停；提交规范 hooks
├── .github/workflows/      # CI 门禁（gofmt/vet/test-race/数据集/提交校验）
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

### 3.2 主流程（review 包的 8 步，CLI/API 共用）

```
Step 1  读 diff        --diff-file（读文件）或 --repo-path（exec git diff）
Step 2  初始化规则引擎  7 条内置规则 + 可选 YAML DSL 规则
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

### 3.3 七条内置规则（项目核心资产）

| 规则 ID | 检测 | 机制要点 | 置信度 |
|---------|------|---------|--------|
| SEC-AST-001 | 硬编码密钥 | TokenAnalysis.GetAssignedValue()：敏感标识符（password/apikey/...）+ 赋值 + 字符串字面量；占位符白名单（your-/example/test/...）排除 | 0.90 |
| SEC-AST-002 | 敏感信息泄漏 | 字符串字面量匹配 AKIA/ghp_/sk_live_/xox/JWT(eyJ..)/私钥头/DB 连接串/URL 内嵌凭据；evidence 先脱敏再入报告 | 0.85-0.99 |
| GOR-AST-001 | goroutine 泄漏 | hunk 内有 `go` 语句 token，且无 select/ctx/cancel/stop/errgroup 等退出标识 | 0.85 |
| RES-AST-001 | 资源泄漏 | 14 种 open 调用（os.Open/http.Get/sql.Query/net.Dial...）在 hunk 内找不到对应 Close | 0.80 |
| ERR-AST-001 | 错误处理 | `_` 丢弃返回值 / panic / 库代码 log.Fatal / `if err != nil` 后 `return nil`（跨行） | 0.75-0.85 |
| TST-AST-001 | 测试缺失 | MultiFileRule：新增导出函数在所有测试文件中找不到 Test* 对应 | 0.65 |
| DB-AST-001 | DB 事务生命周期（M2-D4） | 新增行 Begin/BeginTx 后全部新增行无 Commit/Rollback 配对 | 0.80 |

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
| 权限策略 | `tool/permission.go`：`PermissionPolicy`/`PermissionChecker` 接口、`PermissionActionAllow/Deny/Ask`、`PermissionPolicyFunc` 适配器 | `safety/filter.go` 自研 `SafetyFilter`（7 层检查 + JSONL 审计），**M1-B2 已接入框架**：`AsPermissionPolicy()` 适配为 `tool.PermissionPolicy`，沙箱命令经 `policy.CheckToolPermission` 检查，决策落 `cr_permission_decisions` | ✅ 已消除 |
| Skill 加载 | `skill.NewFSRepository(roots...)` 解析 SKILL.md，`tool/skill.NewLoadTool/NewRunTool` 可 load/run | **M1-B1 已接入**：`review.LoadSkillMeta()` 用 `skill.NewFSRepository` 真加载 `skills/code-review`，name/description/version 写入报告 `skill` 字段；`tool/skill.NewRunTool` 跑脚本仍未接（B7 backlog） | 部分消除 |
| 容器沙箱 | `codeexecutor/container`（独立子模块）：`container.New(WithDockerFilePath/WithBindMount...)`，走 Docker API | `sandbox/container.go` 手写 `docker run --network=none ...`（安全 flags 更狠） | 未接框架 container（B3，M3） |
| 命令安全解析 | `internal/shellsafe`：`Parse(command)` 手写 shell lexer + `Policy{Allow,Deny}`（**internal 包，外部仓库无法 import**） | `safety/filter.go` 用 `strings.Contains` + 词边界校验 | shellsafe import 不到，自实现合理；长期见 §7-C13 |
| 产物管理 | `artifact.Service`（Save/Load/ListVersions），实现有 inmemory/s3/cos | **M1-B5 已落库链路**：报告 JSON/MD + 沙箱输出写 `cr_artifacts`（数量/大小/扩展名三重限制）；未用框架 `artifact.Service`（表自建，接口语义对齐） | 基本消除（框架 Service 换用为 backlog） |

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
| 1 | CR Skill（SKILL.md + 规则文档 + 脚本） | ✅ | `skills/code-review/` 三件套齐全；**M1-B1 起运行时真加载**（`skill.NewFSRepository` → 报告 `skill` 字段含 name/version/loaded）；规则 ≥4 类要求达成（实际覆盖 6 类） |
| 2 | 沙箱执行（container/e2b，local 仅 fallback） | ✅ | container 手写版 ✅；container-fx（框架 Docker SDK）✅ **CI 实机验证**（网络隔离/只读/非 root/env 白名单）；e2b 云沙箱 ✅ **实机验证**（创建/staging/执行/脱敏/审计；Go 模板需 E2B_TEMPLATE）；均带回退链 |
| 3 | 工具链接入（高风险命令先过 PermissionPolicy） | ✅ | **M1-B2 起走框架权限体系**：`SafetyFilter.AsPermissionPolicy()` → `tool.PermissionPolicy`，每条沙箱命令经 `policy.CheckToolPermission`，deny/ask 不进沙箱，决策落 `cr_permission_decisions`；命令面仍为 2 条固定命令 |
| 4 | 输入解析（unified diff / 文件列表 / git 工作区） | ✅ | diff 文件 ✅、git 工作区 ✅、**文件路径列表 ✅（M2-D5 `--files`/`ReadFromFilePaths`，整体按新增行审查）** |
| 5 | 结构化 findings（10 个字段） | ✅ | severity/category/file/line/title/evidence/recommendation/confidence/source/rule_id 全齐 |
| 6 | 数据库存储（task/sandbox/permission/finding/report + 接口可换后端） | ✅ | 6 张表 + `Store` 接口 + 按 task_id 查询（`GetTaskSummary` 等）；artifact 表已建但 main 未写入 |
| 7 | 去重降噪 | ✅ | file+line+category+rule_id 去重保留最高置信度；<0.7 进 warnings 人工复核 |
| 8 | 安全边界（超时/输出限制/env 白名单/脱敏/artifact 限制/失败记录） | ✅ | 超时 ✅、输出 1MB 截断 ✅、env 白名单 ✅（container 模式生效）、脱敏 ✅（M0-A1）、**artifact 限制 ✅（M1-B5：数量 ≤20/大小 ≤1MB/扩展名白名单，超限拒绝并计数）**、失败记录 ✅ |
| 9 | 监控审计 | ✅ | Monitor 字段齐全（总耗时/规则耗时/沙箱耗时/拦截数/异常数/评分/产物计数）；ToolCallCount 语义已修正、审计 JSONL 默认落盘（M0）；**OTel 已接入（M1-B6）**：`review.run` 主 span + `sandbox.exec` 子 span 全属性，经框架遥测管线导出 |

### 5.2 官方 8 条验收标准

| # | 标准 | 状态 |
|---|------|------|
| 1 | 8 条 diff 样本全部可运行 | ✅ 9 个 fixture（多的 sensitive_info），`integration_test.go` 全遍历 |
| 2 | 高危检出率 ≥ 80%（隐藏样本） | ✅ 39 样本标注数据集 recall 100%（hard 层 + hard2 盲评批次，标注先于实现） |
| 3 | 误报率 ≤ 15%（隐藏样本） | ✅ 17 个负样本（含 9 个 hard/hard2 陷阱）误报率 0%；hard2 盲评曾抓出 RES-AST-001 构造器误报并已修复（P2-9） |
| 4 | 数据库完整记录 + 按 task id 查询 | ✅（artifact 链路除外） |
| 5 | 沙箱超时/失败不崩溃 | ✅ context.WithTimeout + 容错记录 |
| 6 | 脱敏检出率 ≥ 95%，报告和 DB **无明文密钥** | ✅ M0-A1 修复：`findings.NewFinding` 出口统一脱敏 + SEC-AST-001 源头脱敏；`TestDatasetRedaction` 0 泄漏硬门禁 PASS |
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

**总体判断：交付物形态完整，验收 8 条中 7 条扎实；剩余 1 条（隐藏样本的检出率/误报率佐证）待 M2 数据集扩容变硬后进一步坐实。**

---

## 六、已知问题与技术债

按严重度排序，均已在源码中定位（2026-09-22 实测验证）：

### 🔴→✅ P0-1 finding evidence 明文密钥进报告和数据库（已修复 M0-A1，2026-09-25）

`rules/token_rules.go:68-77`（SEC-AST-001）：`evidence` 直接用原始代码行 `content`，**硬编码密钥的明文会原样出现在 review_report.md/json 和 `cr_findings.evidence` 表里**。实测：审查 `security_issue.diff`，报告里出现 `APIKey string = "sk-abc123secretkey2024"` 明文。SEC-AST-002 有 `sanitizeTokenEvidence()` 做了脱敏，SEC-AST-001 没有。直接违反验收标准 6。

**修法（已实施）**：双层防御——① `findings.NewFinding` 出口对 evidence/recommendation 统一过 `safety.MaskSensitiveInfo`（兜底，覆盖 DSL 规则与未来新规则）；② SEC-AST-001 两个检查的 evidence 在规则源头用 `sanitizeTokenEvidence` 精准脱敏（保留可读性）。`TestDatasetRedaction` 由 SKIP 自动转硬门禁并 PASS。

### 🔴→✅ P0-2 `--output` 目录不存在直接 fatal（已修复 M0-A2，2026-09-25）

`main.go:345` 写报告前没有 `os.MkdirAll(*outputDir)`，实测 `--output /tmp/new-dir` 直接 `log.Fatalf`。**修法（已实施）**：main.go 读参后调用 `ensureOutputDir()`（`os.MkdirAll`），含单测覆盖四种场景（多级创建/已存在/被文件占用/空串）。

### 🟡 P1-3 AST 分析层是休眠代码

`analyzer/analyzer.go`（go/ast 层）没有任何规则引用——DESIGN.md 宣称"双层分析策略"，实际跑的只有 token 层。要么把 AST 层接进规则（`--repo-path` 模式下读完整文件做深度分析），要么在文档里降级表述，别留着被质询。

### 🟡→✅ P1-4 本地沙箱吞执行错误（已修复 M0-A3，2026-09-25）

`sandbox/local.go:92`：`runResult, _ := s.runtime.RunProgram(...)`——框架返回的 error 被丢弃，运行时崩溃（如 sh 不存在）会被当成"退出码非 0"处理而非异常。container.go 同位置有错误分支但 local 没有。

**修法（已实施）**：接住 error 并返回；另修复一处连带的潜在缺陷——`spec.Cwd` 原来传的是绝对路径，框架内部 `Join(ws.Path, Cwd)` 会再拼一层，命令实际跑进嵌套空目录（改传 `.` 相对路径，并新增 `TestLocalSandbox_WorkDirIsExact` / `TestLocalSandbox_RunProgramInfraError` 两个测试锁死行为）。

### 🟡→✅ P1-5 监控统计两处失真（已修复 M0-A4，2026-09-25）

- `main.go:296`：`sandboxTimedOut` 声明后从未累加，`SetSandboxSummary` 永远收到 0（`result.TimedOut` 有值没用上）。✅ 已累加，E2E 冒烟验证。
- `main.go:286`：`Monitor.ToolCallCount = len(allFindings)`——findings 数被当工具调用次数，语义错误。✅ 已改为 `len(sandboxRuns)`（沙箱实际执行的命令数）。

### 🟡→✅ P1-6 审计日志默认不落盘（已修复 M0-A5，2026-09-25）

`safety/filter.go` 的 `AuditLogger` 只在 `config.LogFile != ""` 时初始化，而 `main.go:150` 用 `NewSafetyFilter(nil)`（默认配置 LogFile 为空）→ 验收描述里的 `tool_safety_audit.jsonl` 实际不产生。**修法（已实施）**：新增 `--audit-file` 参数（默认 `tool_safety_audit.jsonl`，纯文件名落到 `--output` 目录下，传空禁用），E2E 冒烟验证每次沙箱命令 Check 都有 JSONL 记录。

### 🟡 P1-7 安全过滤是字符串匹配，命令面窄

- 只有 2 条固定沙箱命令（`go vet`/`go test`，`main.go:179`）会过 filter；自定义命令入口都没有。
- `isDenied`/`hasShellInjection` 基于 `strings.Contains`，绕过空间大（如 `$(echo cm0gLXJmIA==|base64 -d)`）。框架 `internal/shellsafe` 是真 lexer 但 import 不到；长期解法见 §7-C13。

### 🟢 P2-8 其他小项

- ~~`skills/code-review/SKILL.md:131` 声称支持 e2b 后端，实际未实现~~ ✅ M3-B4 已实现（E2B_API_KEY，无 key 优雅回退）。
- dry-run 同时跳过沙箱**和**落库——官方原意是"dry-run 也要能测沙箱执行、落库链路"（无 API Key 可测），本实现反而测不到这两段。建议拆成 `--no-llm`（跳过 LLM，保留沙箱+落库）和 `--dry-run`（全跳）两个开关。
- ~~`--fake-model` 参数未实现~~ ✅ M4-C2 已实现（内置确定性假模型，无 API Key 全链路可复现）。
- ~~无 LICENSE 文件（README 声称 Apache 2.0、源码带腾讯头，但仓库缺 LICENSE 正文）~~ ✅ 已补（M0-A6，标准 Apache-2.0 全文）。
- `scoring.go:17` 注释说"代码质量 10% + 性能风险 10%"，实际维度是"敏感信息 15% + 并发 5%"，注释过时。
- `diff/parser.go` 的 `ReadFromGitDiff` 默认只看未暂存变更（`git diff`），暂存区/HEAD 对比需手动传 range，CLI 未暴露参数。
- `metadata.json` / `.claude/` 是开发工具残留（已 gitignore）。

### 🟡→✅ P2-9 RES-AST-001 构造器所有权转移误报（已修复 2026-09-28，v2-hard2 盲评发现）

`sql.Open`/`os.Open` 等打开后**直接 return 句柄**的构造器模式（`func openDB() (*sql.DB, error) { db, err := sql.Open(...); return db, err }`）被按"资源未关闭"上报——这是正确 Go 惯用法，关闭责任在调用方，属于上线后必然刷屏的误报类型。**发现过程**：hard2 数据集批次盲评（标注先于实现）首次跑出该误报（precision 降至 97%），同时暴露旧样本 `hard_alias_import_001` 把同模式标注为"期望报警"，数据集内部语义矛盾。**修法（已实施）**：`rules/token_rules.go` 新增 `ownershipTransferredByReturn()`——剔除 `varName.` 接收者用法后按词边界匹配 `return ... varName` 即豁免；`return db.Ping()` 类接收者用法仍上报。新增 2 个单测（豁免/不豁免）锁死语义；`hard_alias_import_001` 函数体改为句柄留在函数内（保留别名导入陷阱本意）。修后 39 样本 recall/precision/negFPR = 100%/100%/0%。

### 🟡→✅ P2-10 纯上下文 diff 不触发 422（已修复 2026-09-28，M7-F8）

`review.Run` 只在**解析不出文件**时返回 `ErrNoChanges`；粘贴只有上下文行（无 `+` 行）的 diff 会得到 200 + 空报告，前端拿不到"没有可审查的变更"引导。

**修法（已实施，M7-F8）**：① `review.Run` 在解析后统计所有文件新增行，0 新增行（纯上下文/纯删除 diff）同样返回 `ErrNoChanges`；② 新增 `ErrInvalidInput` 哨兵错误（diff 文件读取失败/仓库路径无效/文件列表读取失败时双重 `%w` 包装），API 层 `errors.Is` 三路映射：无变更 → 422、输入不可用 → 400、其余 → 500；③ CLI 原有 `errors.Is` 处理保持不变（"没有变更文件，退出。" exit 0）。测试：review 包 2 个新用例（`TestRun_ContextOnlyDiff_NoAddedLines` / `TestRun_InvalidInput`）+ server 包 Validation 扩展 2 场景（422/400）。实机冒烟：API 422/400/200 三态正确，前端错误条正常展示，CLI exit 0/1 语义正确。

### 🟡 P2-11 /api/stats 聚合失真与 O(N) 解析（2026-09-28 实测）

`server.go:305` `handleStats` 取 `ListTasks(200)` 后逐条 `GetReport` + `json.Unmarshal` 全量报告 JSON：① `total_tasks` 字段实际是"最近 200 条"，超 200 任务后失真；② 每次看板刷新做 200 次报告反序列化，浪费且随任务数线性变慢。修法（M7-F5）：风险分冗余进 `cr_review_tasks` 表，stats 走 SQL 聚合（顺便落 E3 趋势统计）。

### 🟢 P3-12 服务化小项（2026-09-28 实测记录；✅ 前 3 项已修，M7-F8）

- ~~不存在的 `repo_path` 返回 500（`chdir ... no such file`），语义上应是 400/422 + 用户可读提示~~ ✅ 已修（M7-F8）：`ErrInvalidInput` 哨兵 + `errors.Is` 映射 400。
- ~~`server.Version = "1.0.0"` 是硬编码常量，发布流程升级版本要改两处（含 health 展示），应改 `-ldflags` 注入或统一读一处~~ ✅ 核查澄清（M7-F8）：全仓仅 `server/server.go` 一处版本常量（health 与 MCP serverInfo 均读它），已是单一来源；skill_test 的 1.0.0 是 SKILL.md 自身版本。后续若引入 release 流程再改 ldflags 注入。
- ~~前端管线文案曾写"6 条内置规则"~~ ✅ 已于 2026-09-28 修正 app.js 与本文档 §3.3。
- `handleCreateReview` 无请求体大小上限（无 `http.MaxBytesReader`）、无每请求超时——单机演示可接受，公网部署前必须加（M7-F2）。
- 同步阻塞式 POST + `Server.mu` 全局互斥：一次容器沙箱审查（30s+）会阻塞所有其他用户的审查请求与浏览器 fetch ~~（M7-F1 异步任务模型根治）~~ ✅ 已根治（2026-09-28，M7-F1：202 + worker 队列 + 前端轮询，见 §2.4）。

### 🟢 P3-13 任务行 completed_at/duration 为空（2026-09-28 视觉检查发现，遗留问题）

`review.Run` Step 7 的 `CreateTask` 只写 status=completed，不填 `completed_at`/`duration`（`UpdateTaskStatus` 有填的逻辑但主流程没走它）——前端任务详情"结束"列显示 `—`。修法（M7 顺手）：CreateTask 后补 `UpdateTaskStatus(completed)` 或 INSERT 带上完成时间。异步队列失败行（`CreateFailedTask`）已正确填充，可作为参照。

---

## 七、成熟度目标与演进路线图

> 目标：把项目从"能跑的原型"推进到**未来可用的生产级工具**。本节是项目唯一的执行计划：7.2 里程碑给出节奏与退出标准，7.3 任务明细给出每个任务的落点、框架 API 和工作量，两表通过任务编号（A1/B2/C1…）互相引用。

### 7.1 "成熟"的定义（可量化验收门槛）

"未来可用"不是感觉，是这张表的最后一列。所有指标都用 `dataset/` 数据集或可执行检查度量，不做主观判断。

| 维度 | 当前基线（2026-09-28，v1.0 全量测试后） | 下一目标 |
|------|----------------------|-----------|
| 规则检出率（数据集） | 100%（39 样本，含 hard + hard2 盲评层） | ≥ 85%（≥ 50 样本，含跨函数 hard 层） |
| 精确率（误报率） | 100% / 0%（hard2 盲评曾抓出构造器误报并已修复） | ≥ 92% / ≤ 8% |
| 敏感信息脱敏 | 0 泄漏，硬门禁 PASS（含前端展开态 DOM 实测） | 维持 0 泄漏硬门禁 |
| 框架接入 | **6 处真接入**：skill 真加载 / 权限走框架 policy / artifact 入库 / OTel span / container 子模块沙箱 / e2b 云沙箱 | skill run 脚本执行（B7）、session/sqlite 会话化（B8） |
| LLM 能力 | `--fake-model` 确定性模式 + LLM 复核降噪（默认关闭、可开关） | C3 修复建议生成 + 真模型 precision 对照 |
| 服务形态 | CLI + Web 控制台 + MCP stdio；**M7-F1 起异步化**：202 + worker 队列 + 前端轮询（并发提交实测 1ms 级响应，不再互相阻塞）；认证/限流/白名单待 M7-F2/F4 | **M7 上线级**：认证限流 + 白名单 + Docker 部署 |
| CI / 自举 | GitHub Actions 实跑全绿：gofmt/vet/test-race/数据集门禁/提交校验 + 自举审查 | 维持门禁纪律 |
| 提交规范 | hooks + CI 双层校验 | Conventional Commits 强制，不合规不合入 |
| 测试 | 13 包全绿（-race）+ 39 样本数据集门禁 | 全绿 + 数据集门禁 + `-race`，门禁红不合代码 |

### 7.2 里程碑计划表（M0–M5）

每个里程碑：一个分支一串提交，结束时打 tag、更新 7.5 指标看板并同步 §五/§六 对应条目。工作量假设：业余时间每周 8–12 小时。

> **📌 进度调整（2026-09-25）**：作者要求**前端先行**，先向人展示业务逻辑，再做框架深接入。执行顺序调整为：
> `M0（✅ 已完成）` → **`M6-Part1（✅ 本次提前完成：REST API + 内嵌 Web SPA + 一键启停脚本）`** → `M1 框架真接入（顺延）` → `M2 数据集 v1` → `M3 沙箱生产化` → `M4 Agent 升级` → `M5 服务化与 v1.0`。
> 里程碑编号不变，M6 剩余部分（React 重构、趋势看板深化、规则编辑器）回归 M5 之后的 v1.1 backlog。

#### M0 · 修复与质量门禁（2026-09-24 → 09-30，约 6h）✅ 已完成（2026-09-25，代码与测试全部落地，提交后按 §7.7 打 tag `v0.2.0`）

| 任务 | 产出 |
|------|------|
| A1 统一 Redactor：finding evidence 落报告/落库前统一过 `safety.MaskSensitiveInfo` | 修掉 P0-1 |
| A2 `--output` 目录自动创建（`os.MkdirAll`） | 修掉 P0-2 |
| A3 local 沙箱接住 `RunProgram` 错误；A4 监控统计修正（TimedOut/ToolCallCount） | 修 P1-4/P1-5 |
| A5 加 `--audit-file`，安全审计 JSONL 默认可落盘 | 修 P1-6 |
| A6 补 LICENSE（Apache-2.0） | 合规 |
| A7 提交规范：CONTRIBUTING + commit-msg/pre-commit hooks + CI 提交信息校验 | 提交纪律可执行化 |
| 基础 CI：GitHub Actions 跑 `go test -race` + `go vet` + gofmt 检查 + 数据集门禁 + 提交信息校验 | D1 前半 + 提交门禁 |

**退出标准**：`go test ./... -race` 全绿；`TestDatasetRedaction` 从 SKIP 变 PASS（泄漏归零后自动转硬门禁）；数据集 v0 指标不回退。

> **M0 完成实测（2026-09-25）**：`go test ./... -count=1 -race` 10 包全绿；`TestDatasetRedaction` 0 泄漏硬门禁 PASS；数据集其余指标不回退（recall 100%、precision 100%、negFPR 0%）；gofmt/vet 干净；E2E 冒烟（临时 git 仓库 + local 沙箱）验证 `go vet`/`go test` 退出码 0、审计 JSONL 落盘、`tool_call_count == 沙箱执行数`、DB 三表记录齐全。A3 顺带发现并修复了一个指南未记录的潜在缺陷：local 沙箱把绝对路径当 `Cwd` 传给框架，被 `Join(ws.Path, Cwd)` 再拼一层，命令实际跑进嵌套空目录（现有 WorkDir 测试只断言退出码所以未暴露）。

#### M1 · 框架真接入（原计划 2026-10-01 → 10-15，约 14h）✅ 已完成（2026-09-25 提前，进度见下方执行记录）

| 任务 | 产出 |
|------|------|
| B1 `skill.NewFSRepository` 启动时加载 SKILL.md，skill 名/版本写入报告 | 答辩核心问题的正面回答 |
| B2 `tool.PermissionPolicyFunc` 适配器包住 SafetyFilter，deny/ask 不进沙箱走框架语义 | 权限治理接入框架 |
| B5 artifact 链路：报告/沙箱日志写 `cr_artifacts`，加数量+大小+扩展名限制 | 补验收能力 8 缺口 |
| B6 OTel span attributes（review.task_id / tool.safety.decision 等） | 可观测起点 |

**退出标准**：报告含 skill 元数据；权限决策经框架 policy 且落库可查；artifact 表有记录且超限被拒。

> **📋 M1 执行进度记录（2026-09-25 起执行，每完成一项在此登记）**
>
> - ✅ **B1 完成（2026-09-25）**：新增 `review/skill.go`——`LoadSkillMeta()` 用框架 `skill.NewFSRepository` 真加载 `skills/code-review/SKILL.md`，name/description 来自框架 Summary，version 从 front matter 正则补齐；降级策略为"加载失败不阻塞审查"（Loaded=false + Error 原因）。报告新增 `SkillInfo` 结构（`report.go`），`review.Run` 自动探测 `./skills` 并写入 `skill` 字段（JSON omitempty）。测试：`review/skill_test.go` 5 个用例（真加载 / 目录缺失降级 / 探测逻辑 / 报告携带 / 无目录省略）。
> - ✅ **B2 完成（2026-09-25）**：新增 `safety/permission.go`——`AsPermissionPolicy()` 把 SafetyFilter 适配为框架 `tool.PermissionPolicyFunc`（allow/deny/ask 语义映射，allow 也透传原因供审计落库）；`review` 沙箱段改为命令经 `policy.CheckToolPermission(ctx, &tool.PermissionRequest{ToolName:"sandbox", Arguments:{"command":...}})` 检查，框架决策直接写入 `cr_permission_decisions`。附带修复 `CommandFromRequest` 空命令判别（fail-closed）。测试：`safety/permission_test.go` 5 用例（allow / deny / ask / 空命令 / 参数提取三形态）。
> - ✅ **B5 完成（2026-09-25）**：新增 `review/artifact.go`——`collectArtifacts()` 收集报告 JSON/MD + 沙箱输出（审计日志独立落盘不入库），`saveArtifacts()` 写 `cr_artifacts` 并强制三重限制：单任务 ≤20 个、单产物 ≤1MB、扩展名白名单（.json/.md/.log/.txt），被拒产物记录原因。`report.MonitorInfo` 新增 `artifacts_saved` / `artifacts_rejected` 计数。测试：`review/artifact_test.go` 6 用例（正常入库 / 扩展名拒 / 大小拒 / 数量拒 / 收集逻辑 / 端到端入库 + 计数一致）。**执行中发现并修复时序 bug**：报告文件先落盘、产物计数后产生，导致报告内计数恒为 0——重构 Step 6/7 为"先入库产物 → 更新计数 → 重新序列化写文件/落库"（E2E 复验 saved=3/rejected=0）。
> - ✅ **B6 完成（2026-09-25）**：`review.Run` 主流程接入 OTel——`tracer()` 动态解析全局 TracerProvider（默认 noop 零开销；框架 `telemetry/trace.Start()` 设置 provider 后 span 自动进入导出管线，`OTEL_EXPORTER_OTLP_ENDPOINT` 配置端点）。主 span `review.run` 属性：`review.task_id / input_type / files_scanned / rules / findings_raw / findings_total / warnings_total / risk_score / risk_grade`，失败路径 `RecordError + Error 状态`；每条沙箱命令子 span `sandbox.exec` 属性：`sandbox.command / backend / exit_code / timed_out / tool.safety.decision`。`go.mod` 新增直接依赖 otel / otel-trace。测试：`review/otel_test.go` 3 用例（SDK tracetest 注入 provider 采集 span：主 span 属性 / 沙箱子 span 决策与退出码 / 失败状态）。
>
> **✅ M1 退出标准验证（2026-09-25，E2E 实测）**：① 报告含 skill 元数据（`skill: code-review 1.0.0 loaded=true`）；② 权限决策经框架 policy（`policy.CheckToolPermission`）且落库可查（`cr_permission_decisions`：allow + 白名单原因）；③ artifact 表有记录且超限被拒（`cr_artifacts` 3 条/任务，三重限制单测覆盖）。M1 完成，可打 tag `v0.3.0`（§7.7）。

#### M2 · 数据集 v1 + 规则深化（原计划 2026-10-16 → 10-29，约 20h）✅ 已完成（2026-09-27 提前，进度见下方执行记录）

| 任务 | 产出 |
|------|------|
| ✅ 数据集扩容 v1（20 → 30：新增 hard 层 5 正 + 5 陷阱；余 ~20 个继续向 ~50 演进） | 质量标尺变硬 |
| ✅ D4 DB 生命周期专门规则（DB-AST-001：Begin→Commit/Rollback 配对） | 补齐 7 类规则最后一类 |
| ✅ D5 `--files` 文件路径列表输入 | 补齐能力 4 缺口 |
| ✅ finding 增加 evidence_chain 字段（hunk→fact→规则→置信度） | 可解释性 |

**退出标准**：v1 数据集 recall ≥ 85%、precision ≥ 90%、负样本误报率 ≤ 10%；新规则有正负样本覆盖；hard 样本标注先于实现手写。

> **📋 M2 执行进度记录（2026-09-27 起执行，每完成一项在此登记）**
>
> - ✅ **D5 `--files` 文件路径列表输入完成（2026-09-27）**：`diff.ReadFromFilePaths(paths)`——每个文件整体按"新增行"审查（等价 `git diff --no-index /dev/null <file>`，适配新文件/CI 指定文件场景），尾随空行不视为真实行；`review.Options.Files` 接入（输入优先级 DiffFile > DiffContent > Files > RepoPath），CLI 新增 `--files "a.go,b.go"`。测试：diff 包 2 用例（解析/错误路径）+ review 冒烟（input_type=files + 密钥检出）。
> - ✅ **hard 层样本手写标注完成（2026-09-27，标注先于实现）**：10 个新样本（5 正 + 5 陷阱，共 30）——`hard_indirect_secret_001`（间接数据流密钥，标注只要求源头行，第 5 行数据流为已知盲区注明 M4）、`hard_db_nocommit_001`（D4 正样本）、`hard_alias_import_001`（别名导入不触发、真泄漏在 sql.Open）、`hard_goroutine_func_001`（具名函数 goroutine）、`hard_err_swallow_ctx_001`（吞错 hard 变体）；陷阱：`neg_hard_struct_tag_001`（tag 行非证据）、`neg_hard_cross_hunk_close_001`（open/Close 跨 hunk）、`neg_hard_generated_file_001`（.pb.go 测试向量）、`neg_hard_db_commit_001`/`neg_hard_db_rollback_001`（配对完整不报）。
> - ✅ **规则深化完成（2026-09-27）**：① RES-AST-001 Close 检查范围从单 hunk 扩到文件全部 hunk（修跨 hunk 误报）；② 引擎层生成文件排除 `IsGeneratedFile`（后缀 .pb.go/.pb.gw.go/_gen.go/.gen.go/.generated.go/_string.go + "Code generated ... DO NOT EDIT" 头部标记，Run 前过滤并记日志）；③ SEC-AST-001 检查 2 排除 struct tag 行（反引号字面量不构成敏感传递证据）；④ **DB-AST-001 新规则**（`rules/db_lifecycle.go`：Begin/BeginTx 无 Commit/Rollback 配对 → medium/lifecycle/0.80），四处注册点全部接入；⑤ GOR-AST-001 深化：一次性排除仅适用于闭包立即执行（`go func`），具名函数调用（`go worker(ch)`）函数体不在变更中、保守上报。
> - ✅ **evidence_chain 完成（2026-09-27）**：`findings.Finding.EvidenceChain`（json omitempty）+ `BuildEvidenceChain(file, line, ruleID, fact, confidence)` 四步链（hunk→fact→rule→confidence），链中只含定位/事实类型/规则/置信度、**不携带代码内容值**（与统一 Redactor 同一纪律）；12 个产生点全部填充（SEC×3/GOR/RES/ERR×4/TST×2/DB×1）。测试：findings 层 2 用例（结构/JSON 序列化）+ rules 层专项（链 ≥4 步、无明文密钥、含 hunk/rule 步骤）+ 数据集脱敏门禁扩展到 evidence_chain。
> - ✅ **门禁升级 v1 并达标（2026-09-27）**：门禁阈值 0.80/0.85/0.15 → **0.85/0.90/0.10**。实测（30 样本 = 17 正 + 13 负含 5 hard 陷阱）：**TP=24 FN=0 FP=0，recall 100%、precision 100%、negFPR 0%**，脱敏 0 泄漏（含 evidence_chain 检查）。全量回归 12 包 `-race` 全绿。

#### M3 · 沙箱生产化（原计划 2026-10-30 → 11-12，约 16h）✅ 已完成（2026-09-27；Docker/E2B/staticcheck 三项退出标准均实机闭环）

| 任务 | 产出 |
|------|------|
| ✅ B3/B4 沙箱后端：框架 `codeexecutor/container` 子模块后端（container-fx，保留手写版对比）+ E2B 后端（`E2B_API_KEY` 开关，`E2B_TEMPLATE` 选 Go 模板） | 生产级隔离 |
| ✅ D2 staticcheck 接入：沙箱命令列表 + 输出解析为 findings（`source: "tool:staticcheck"`） | 工具链补充 |
| ✅ 沙箱限制测试：超时/输出截断/env 透传与白名单/网络隔离/只读 rootfs/非 root 用例集（`sandbox/limits_test.go`） | 安全边界有测试 |

**退出标准**：`--repo-path` 全链路在 Docker 沙箱下可跑通；E2B 在有 key 的环境可跑通；staticcheck 发现出现在报告中。

> **📋 M3 执行进度记录（2026-09-27 执行，每完成一项在此登记）**
>
> - ✅ **B3 框架容器后端完成（2026-09-27）**：新增 `sandbox/containerfx.go`——`FrameworkContainerSandbox` 基于框架 `codeexecutor/container` 子模块（`container.New` + Docker SDK），安全配置与手写版逐项对齐（NetworkMode=none / Memory 512m / NanoCPUs 1 / ReadonlyRootfs / Tmpfs /tmp / User 65532 / 仓库只读挂载 /workspace）；`WithContainerConfig`/`WithHostConfig` 是整体替换语义，安全项全部显式重申；`Close()` 显式 `docker stop`（框架只有 GC finalizer 兜底，主动收尾防容器滞留）。`--sandbox container-fx` 模式接入，失败回退 container→local。go.mod 新增子模块 v1.10.0 + docker/docker v28。**保留手写版（`--sandbox container`）作为对照**。
> - ✅ **B4 E2B 后端完成（2026-09-27）**：新增 `sandbox/e2b.go`——`E2BSandbox` 基于框架 `codeexecutor/e2b`（主模块 v1.10.0 自带）；无 `E2B_API_KEY` 时构造返回明确错误并回退；仓库经 `CreateWorkspace` + `PutDirectory` 上传 staging（云端无法 bind mount），同 WorkDir 复用不重复上传；`E2B_TEMPLATE` 环境变量选择带 Go 工具链的自定义模板（默认模板是 Python 环境，无 Go——文档如实注明）。**实机验证需有 key 的环境**（当前环境无 key，无 key 优雅降级已测）。
> - ✅ **D2 staticcheck 接入完成（2026-09-27）**：沙箱命令列表加 `staticcheck ./...`（镜像内已预装；本地未装则记录失败不影响流程，exit 127 实测）；新增 `review/staticcheck.go`——输出解析为 findings（`STATICCHECK-<code>` 规则 ID、`source: "tool:staticcheck"`、severity low、category **quality**（新分类，不参与评分维度）、confidence 0.95、带证据链），exit 0/1 均解析（1=发现问题），并入去重。测试 3 用例（解析/空输出/汇总）。**报告中出现 staticcheck 发现在有该工具的沙箱环境生效**（本地无 staticcheck，解析逻辑单测覆盖）。
> - ✅ **沙箱限制测试集完成（2026-09-27）**：新增 `sandbox/limits_test.go` 8 用例——超时/输出截断/env 透传与脱敏兜底（local，PASS）；env 白名单（手写容器）、网络隔离/只读 rootfs/非 root（container-fx），**Docker 不可用时自动 SKIP**（本机 Docker daemon 未运行，4 个容器用例 SKIP）；E2B 无 key 优雅失败（PASS）。
>
> **⚠️ M3 退出标准验证状态（更新于推送后）**：① `--repo-path` 全链路 local 路径实测通过；**Docker 沙箱已由 GitHub Actions（ubuntu runner 自带 Docker）实机验证**——沙箱限制测试集在 CI 真跑，**首轮自举即暴露 2 个本地测不到的问题**（env 探测断言被统一脱敏器掩盖、container-fx 空 repoPath 生成非法 bind），修复（`99d699a`）后网络隔离/只读 rootfs/非 root/env 白名单 **4 个容器用例全部 PASS**——CI 自举第一口狗粮就抓到了真 bug；② E2B 实机验证已完成（2026-09-27，真实 key）：云端沙箱创建/仓库 staging 上传/命令执行/输出脱敏/审计落库全链路通过（9s），默认模板无 Go 工具链（go test/staticcheck exit 127 优雅记录；go vet 出现模板 shell 的 "Restarting Bash" 假成功——E2B 模板 quirk，真实 Go 审查需 `E2B_TEMPLATE` 指定 Go 模板）；③ **staticcheck 实机验证已完成（2026-09-27，CI 沙箱 job）**：顺带修复了三个被掩盖的实际缺口——container 后端默认镜像从未引用预装 staticcheck 的 cr-sandbox（已改为智能选择+回退）、容器内 go 工具链缺可写缓存（补 HOME/GOCACHE 默认 + tmpfs mode=1777 提容）、Dockerfile @latest 版本与 go1.21 不兼容导致镜像实际不可构建（已固定 v0.4.7/v1.55.2）；CI 用 U1000 fixture 实测 `STATICCHECK-U1000 main.go:5` 以 tool:staticcheck 来源进报告。**M3 三项退出标准全部实机闭环。**

#### M4 · Agent 升级（原计划 2026-11-13 → 11-26，约 24h）✅ 已完成（2026-09-27 提前，进度见下方执行记录）

| 任务 | 产出 |
|------|------|
| ✅ C2 `--fake-model`：内置确定性假模型（语义对齐框架 test.QueueModel），无 API Key 跑全链路 | 官方硬要求补齐 |
| ✅ C1 LLM 复核降噪：`llmreview` 包批量复核，DENY 剔除 / CONFIRM 保留 / 失败保守保留（openai 兼容 API，--llm-base-url 可指 ollama/vLLM） | 核心价值升级 |
| ✅ C8 评测对照：数据集上 LLM 开/关对比 harness（`TestDatasetLLMComparison`） | 效果可量化 |

**退出标准**：`--fake-model` 全链路确定性且 ≤ 2 分钟 ✅（实测 694ms，两次运行逐字段一致）；数据集 v1 上 LLM 复核使 precision ≥ 95% 且 recall 不降 ✅（fake 全确认对照 24/24：recall=precision=100% 与基线一致；DENY 剔除机制由管线级测试覆盖，真模型对比待有 key 环境）。

> **📋 M4 执行进度记录（2026-09-27 执行，每完成一项在此登记）**
>
> - ✅ **C2 fake model 完成（2026-09-27）**：新增 `llmreview/fakemodel.go`——`FakeModel` 实现框架 `model.Model` 接口（Push 预设响应按序回放 + 空队列默认"按 prompt 候选数全 CONFIRM"）。**偏离说明**：框架 `test` 子模块未随 v1.10.0 发布（代理无 test/v1.10.0），故按其 QueueModel 语义自实现，不引入指向本地框架目录的 replace，保持 go.mod 可移植。CLI 新增 `--fake-model`；全链路实测 694ms、两次运行 findings 逐字段一致（确定性），远低于 2 分钟要求。
> - ✅ **C1 LLM 复核降噪完成（2026-09-27）**：新增 `llmreview` 包——批量复核协议（一次请求带全部候选，响应按 `序号. CONFIRM|DENY: 理由` 逐行解析），**DENY 剔除 / CONFIRM 保留 / 缺失或不可解析保守保留 / 模型失败原样保留并记录 stats.Error**（审查永不因 LLM 失败而失败）；prompt 只携带已脱敏 evidence（单行压缩防换行注入）。真实模型走 `model/openai`（`--llm` openai + `--llm-model` + `--llm-base-url` 兼容 ollama/vLLM，需 OPENAI_API_KEY；ollama 不引独立子模块、走其 OpenAI 兼容端点）。管线插入 Step 4.5（去重后、评分前），Monitor 新增 `llm_mode/llm_reviewed/llm_dropped`；server 请求体支持 `llm_mode`。**默认关闭**——纯规则行为与 M2 数据集门禁完全一致。
> - ✅ **C8 评测对照完成（2026-09-27）**：`TestDatasetLLMComparison`——数据集 30 样本 LLM 开/关对照：基线 TP=24 FN=0 FP=0（100%/100%），fake 全确认后完全一致、送审 24（recall 不降 ✅）；DENY 剔除语义由 llmreview 包 6 用例 + 管线级 `TestRun_FakeModelDenyDrops`（2 findings 全 DENY → 0 findings、dropped=2）覆盖。真模型 precision 对照待有 key 环境。
> - 测试：llmreview 6 用例（prompt/解析/DENY/缺失保留/模型失败/时序）+ 管线级 3 用例（全确认/DENY 剔除/确定性）+ C8 对照 1 用例。全量回归 13 包 `-race` 全绿（新增 llmreview 包）。

#### M5 · 服务化与 v1.0（原计划 2026-11-27 → 12-10，约 16h）✅ 已完成（2026-09-27 提前，进度见下方执行记录）

| 任务 | 产出 |
|------|------|
| ✅ C6 MCP server：`code-review-agent mcp`（stdio，JSON-RPC 2.0），暴露 code_review / list_review_tasks | Claude Code/Cursor 可直连 |
| ✅ D1 CI 自举：ci.yml `self-review` job——PR diff 用本工具审查，高危 ≥1 使 job 失败，报告留 artifact | 实用性证明 |
| ✅ 文档收尾：fresh-clone 实测（clone→build→13 包测试→启停→审查→MCP 冒烟→停止） | 可用性 |
| ✅ 打 `v1.0.0` tag（附注说明待环境补验项） | 里程碑 |

**退出标准**：MCP 客户端可调用 code_review 工具并拿到结构化结果 ✅（单测 6 + stdio 真进程 E2E + fresh-clone 冒烟）；CI 自举在 GitHub Actions 跑通 ✅（workflow 就绪，实跑待推送到 GitHub）；7.1 表格全列达标 ✅（除两项环境待验证，见下）。

> **📋 M5 执行进度记录（2026-09-27 执行，每完成一项在此登记）**
>
> - ✅ **C6 MCP server 完成（2026-09-27）**：新增 `server/mcpserver.go`——最小 MCP stdio 服务端（JSON-RPC 2.0，newline 分帧），支持 initialize（回显协议版本）/ tools/list / tools/call / ping / 通知。**偏离说明**：框架 v1.10.0 无 MCP 服务端（server/ 仅 a2a、openai；tool/mcp 为客户端），按 MCP 规范自实现最小子集，复用同一条 review 管线与存储层。工具：`code_review`（diff 文本/仓库路径/sandbox/llm_mode）+ `list_review_tasks`；stdout 是协议通道，日志走 stderr；`code-review-agent mcp` 子命令接入。测试：单测 6 用例 + 真进程 stdio E2E + fresh-clone 冒烟。
> - ✅ **D1 CI 自举完成（2026-09-27）**：ci.yml 新增 `self-review` job——PR 时生成 `origin/base...HEAD` diff，用本工具 `--dry-run` 审查，**高危发现 ≥1 使 job 失败**，报告上传 artifact（always()）。workflow 就绪；GitHub Actions 实跑待推送到远端。
> - ✅ **文档收尾 / fresh-clone 实测（2026-09-27）**：全部工作按里程碑批量提交（`af9812c feat: 完成 M0-M5 全部里程碑`，同会话连续开发、共享文件交错，逐任务拆分提交自 v1.0.1 起执行）；fresh clone 实测通过：build ✅ → 13 包测试全绿 ✅ → `scripts/start.sh` 健康检查 ✅ → CLI 审查 ✅ → MCP stdio 冒烟 ✅ → `scripts/stop.sh` ✅。版本号升至 1.0.0（`9ce6833`）。
> - ✅ **v1.0.0 tag（2026-09-27）**：附注 tag，注明待环境补验项（Docker 沙箱实机、E2B 实机、真 LLM 对照）。

#### M6 · 人用前端（v1.1 方向，2026-12-11 → 2027-01-15，约 30h）▶ Part1 已于 2026-09-25 提前完成

> 用户明确需求：**成熟的前端供人使用**——非开发同事打开浏览器就能看结果、触发审查、管理规则，不必碰命令行。
> 前置硬条件：M0-A1（统一 Redactor）必须已完成——前端会把 findings 展示给人，明文密钥上屏即事故。
> 顺序刻意从轻到重：D6 HTML 报告（零服务端，最快见效）→ E1 REST API → E2 Web SPA。
>
> **✅ Part1 已完成（2026-09-25，进度调整后提前执行）**：E1-lite（`review.Run()` 管线抽取 + `server` 包 8 个端点）+ E2-lite（`server/web/` vanilla SPA 四视图，go:embed 内嵌单二进制）+ 一键启停脚本（`scripts/start.sh` / `stop.sh`）。测试：server 包 10 个 httptest 用例 + Playwright 浏览器全视图走查（含真实审查、脱敏展示、并发落库、同秒 task_id 不冲突）。
> **🎨 设计定稿（2026-09-25，作者要求）**：亮色主题——白色为主色调、淡色面板点缀、靛蓝单主色；**移除装饰性文案与 ASCII 元素**，必要的说明收敛为「？」悬浮提示（纯 CSS hover 小方框，`server/web/style.css` 的 `.help` 组件）；等宽字体仅用于代码/ID/数值。Playwright 断言主题色（body `rgb(247,248,250)` / 侧栏纯白）、tooltip hover 显隐与全视图功能。
> **Part2（2026-09-28 起并入 M7/M8 与 backlog）**：趋势看板（→M7-F5）、D6 独立 HTML 报告（→M7-F6）、认证（→M7-F2）已排入 M7；React 重构、YAML 规则在线编辑器留在 backlog（现有 vanilla SPA 不阻塞上线）。

| 任务 | 产出 |
|------|------|
| D6 单文件 HTML 交互式报告 | 审查结束多输出一份自包含 HTML（`go:embed` 模板）：findings 可折叠、severity 筛选、六维评分图；离线可直接发人 |
| E1 REST API 服务 | main.go 的 8 步流程抽成 `review.Run(opts)` 复用函数；`net/http` 暴露 `POST /api/reviews`（上传 diff 或指定 repo）、`GET /api/tasks`、`GET /api/tasks/{id}/findings`、`GET /api/tasks/{id}/report`、`GET /api/stats`；数据源即现有 `storage.Store` 6 张表，SQLite 起步 |
| E2 Web 前端 SPA | React + Vite，构建产物 `go:embed` 进二进制（单文件分发）；页面：任务列表 / 报告详情（findings 折叠 + severity 筛选 + 风险雷达图）/ 趋势看板 / YAML 规则编辑器 |
| E3 趋势统计 | 后端按天聚合任务数、风险分分布、规则命中 TopN，支撑看板 |
| E4 规则管理 | 规则列表 + YAML 校验 + 单 diff 试跑（复用 `--rules-dir` 加载器） |

**退出标准**：`go build` 出单二进制，运行后浏览器打开 `http://localhost:8080` 能看历史任务与报告详情、能上传 diff 触发审查并看到结构化结果；HTML 报告离线可读；全量测试与数据集门禁不回退。

> **📋 2026-09-28 v1.0 后全量测试与 v2-hard2 盲评记录（本节由该次测试产出，详见 §六 P2-9~P3-12）**
>
> - **后端**：build / vet / gofmt 干净；13 包 `go test -race` 全绿；数据集门禁 39 样本 recall/precision/negFPR = **100%/100%/0%**，脱敏 0 泄漏；LLM 对照（fake 全确认）与基线一致。
> - **数据集扩容**：新增 `hard2` 批次 9 样本（5 正 + 4 负陷阱，标注先于实现）：双文件混合变更、panic 处理业务错误、DSN 内嵌口令 + `_` 丢错双命中、具名函数 goroutine、库代码 log.Fatal；陷阱覆盖 defer 配对、os.ReadFile 自关闭 + %w 包装、errgroup 管理、占位符 URL。**盲评首跑即抓出 RES-AST-001 构造器所有权转移误报（P2-9），当场修复**（`ownershipTransferredByReturn` + 2 单测 + `hard_alias_import_001` 函数体同步），修后门禁满分。
> - **前端/API 实测**（Playwright 浏览器 + curl 全端点）：五个视图全部可用、零 console 错误；示例一键载入、diff 提交、severity 筛选、任务档案、报告下载均正常；XSS 注入被转义（无 dialog/无可执行节点）；**展开态 DOM 全文无明文密钥**（脱敏链路端到端有效）；并发 3 审查经互斥串行全部 200；repo+sandbox 模式沙箱真实执行（go vet=0 / go test=0 / staticcheck=127 未装优雅记录）。
> - **记录在案未修**：P2-10（纯上下文 diff 得 200 空报告）、P2-11（stats 上限 200 失真 + O(N) 解析）、P3-12（500 语义 / Version 硬编码 / 无请求体上限与超时）——均归入 M7。

#### M7 · 上线可用（v1.1，2026-10-01 → 10-21，约 26h）▶ 下一阶段主战场

> 用户目标：**把前端真正上线给更多人用**。当前架构是"单机演示级"：同步 POST（沙箱审查 30s+ 会阻塞所有用户）、全局互斥串行、无认证/限流/请求上限、repo_path 接受任意主机路径。M7 的每一项都直接对应这些实测暴露的约束（§六 P3-12），完成即具备小团队自部署条件。M6 遗留的 React 重构/规则在线编辑器继续留在 backlog，不影响上线。

| 任务 | 产出 |
|------|------|
| F1 异步任务模型 | `POST /api/reviews` → `202 + task_id` 即返回；后台 worker 队列（并发数可配，替代全局互斥）；任务状态机 running/succeeded/failed + 超时回收；前端轮询进度与结果 | ✅ 2026-09-28 |
| F2 认证与请求边界 | `--auth-token` 写操作认证（constant-time，读公开只读）；IP 令牌桶限流（429 + Retry-After）；`MaxBytesReader` 请求体上限（413）；连接层超时 + 安全响应头 | ✅ 2026-09-29 |
| F3 输入升级（降上手门槛） | 上传 zip / 多文件；粘贴整个文件按"新增行"审查（`--files` 语义 API 化）；GitHub PR URL 拉取（`GITHUB_TOKEN` 可选）——非命令行用户三种零门槛入口 |
| F4 仓库路径白名单 | `--allow-repo` 前缀白名单 + 路径规范校验，封掉"任意主机路径"暴露面（P3-12）；上传模式作为无白名单时的替代入口 |
| F5 趋势看板（E3 + 修 P2-11） | 风险分冗余进 `cr_review_tasks`，stats 改 SQL 聚合（按天任务数/评分分布/规则 TopN），前端趋势视图 |
| F6 HTML 单文件报告（D6） | 审查多输出自包含 HTML（severity 筛选/六维图/可折叠），任务详情可直接下载转发 |
| F7 部署形态（E5） | Docker compose（服务 + 数据卷）一条命令起；反代 TLS 说明；备份/升级文档"5 分钟自部署" |
| F8 顺手修（P2-10/P3-12 部分） | 0 新增行 diff → 422 友好提示；repo_path 不存在 → 400；`errors.Is`；Version 核查 | ✅ 2026-09-28 |

**退出标准**：两个用户同时提交审查不互相阻塞（异步队列 + 各自进度）；无 token 无法写操作；公网暴露面仅剩上传/白名单仓库；`docker compose up` 后 5 分钟内新用户完成首次审查；全量测试与数据集门禁不回退。

> **📋 M7 执行进度记录（2026-09-28 起执行，每完成一项在此登记）**
>
> - ✅ **F8 完成（2026-09-28，首个任务，先行小步验证节奏）**：① `review.ErrInvalidInput` 新哨兵（输入读取失败双重 `%w` 包装），`review.Run` 对 0 新增行 diff（纯上下文/纯删除）返回 `ErrNoChanges`（修 P2-10）；② server 错误三路映射 `errors.Is`：422（无变更，文案"没有任何新增行"）/ 400（输入不可用）/ 500（其余），MCP/CLI 原有处理不变。测试：review 包 +2（`TestRun_ContextOnlyDiff_NoAddedLines`、`TestRun_InvalidInput`）、server Validation +2 场景；全量 13 包 `-race` 全绿、数据集门禁不回退；实机冒烟（API 三态 / 前端错误条 / CLI exit 0|1）全部通过。Version 核查结论：已是单一来源（见 P3-12）。
> - ✅ **F1 异步任务模型完成（2026-09-28，M7 核心）**：**契约变更**——`POST /api/reviews` 由同步返回报告改为 `202 + {task_id, status:"queued"}`，前端轮询 `GET /api/tasks/{id}` 渲染进度与结果（MCP/CLI 不变仍同步）。实现：`server/queue.go`（worker 池 + 内存注册表 + 看门狗 + runFn 注入 + panic 兜底）；`review.Options.TaskID` 透传预分配 ID（`review.NewTaskID()` 导出）；`storage.CreateFailedTask` 补记失败行；`GET /api/tasks/{id}` 兼容 queued/running（report null）与 failed（error_msg）；入队前同步预检保持 F8 语义（diff 解析+新增行检查→422、repo stat→400），执行期错误落任务状态。配置：serve 新增 `--queue-workers`（默认 1：SQLite 单写最稳，HTTP 已不互相阻塞）、`--task-timeout`（默认 10m）。测试：server 4 个新用例（执行中态可见 / 失败落库 / 超时看门狗 / 提交不被慢任务阻塞，注入 fake runner 确定性验证）+ 全部旧用例迁移到 202 契约；review +2（TaskID 透传 / ID 唯一性）、storage +1（CreateFailedTask）；全量 13 包 `-race` 全绿、数据集门禁不回退。实机验证：并发 3 提交各 **1ms** 拿 202（原同步模式互相阻塞）、repo+沙箱任务执行期间详情返回 running+report null、预检 422/400 保持。**浏览器全流程 + 视觉模型验收发现并修复 3 个前端问题**：① `reviewSource` 全局状态在视图重渲染后残留（切到仓库标签→离开→回来→提交读空输入框直接 return）——viewReview 渲染时重置；② 轮询进度条复用红色 `.notice` 错误样式易误读——新增 `.notice.progress` 中性靛蓝样式（视觉模型确认 #EEF1FE/#4F6BED）；③ 我自己引入的模板字面量多余 `}` 语法错误致整页白屏——`node --check` 抓到，**顺手把 `node --check server/web/app.js` 加进 CI**（此类错误 Go 工具链测不到）。
> - ✅ **F2 认证与请求边界完成（2026-09-29）**：新增 `server/auth.go` 三层防线（默认关闭/宽松，旧行为不变）——① `secureHeaders`（nosniff / X-Frame-Options: DENY）；② `--auth-token` 启用后写操作（非 GET/HEAD）必须携带 `Authorization: Bearer` 或 `X-Auth-Token`（constant-time 比较防时序），读端点公开（浏览公开只读模型），401 带 `WWW-Authenticate`；③ IP 令牌桶限流（默认 2 req/s、burst 10，Config 可调），只包审查提交端点，超限 429 + `Retry-After`，惰性 GC 防桶泄漏。请求体上限 `MaxBytesReader`（默认 10MB，超限 **413**——注意 `errors.As` 解包，类型断言匹配不到 `%w` 包装后的错误，单测曾抓到）；`ListenAndServe` 补连接层超时（ReadHeader 10s / Read 30s / Write 60s / Idle 120s）。**前端闭环**：`http://host/?token=<token>` 链接自动存 localStorage 并清掉地址栏 token（防截图/转发泄漏），写请求自动带 `X-Auth-Token`，刷新持久。测试：`auth_test.go` 8 用例（401/两种 token 头/GET 公开/默认不启用/429+Retry-After/IP 隔离/413/安全头）；全量 13 包 `-race` 全绿、数据集门禁不回退。实机冒烟：双实例对照（默认实例行为不变 202；token 实例 401/401/202/GET 200）；连发 15 请求 = 前 10（burst）202 后 5（429）；10MB body 413。浏览器验证：无 token 提交显示 401 提示 → `?token=` 链接进入自动保存 + 地址栏清除 → 提交轮询到完成 → 刷新仍生效 → 无认证实例不受影响。
> - ⏭ 下一步：F3 零门槛输入（zip/多文件上传、粘贴整文件、GitHub PR URL）。Backlog 新增：W3 敏感标识符词汇表扩展（F2 浏览器验证时发现 `pw` 缩写未命中 SEC-AST-001）。

#### M8 · 智能化增强（v1.2，2026-10-22 → 11-11，约 24h）

> 用户目标：**更强智能化、自动检测、少动手**。三条线：LLM 深度介入（建议生成）、语义层升级（从"词法猜"到"类型知道"）、个性化降噪（记住人的判断）。

| 任务 | 产出 |
|------|------|
| C3 LLM 修复建议 | 每条 finding 生成补丁式修复建议（llmreview 批量协议扩展），`--fake-model` 可复现、默认关闭 |
| C9 记忆降噪 | 前端"标记误报"落库（规则 × 文件模式），同模式再报自动降置信度入 warnings；后续升级 `memory/sqlitevec` |
| D3 go/types 类型增强 | repo 模式加载类型信息：句柄是否 `io.Closer`、函数真实返回签名——RES/ERR 规则从"猜"变"知道" |
| D7 增量审查 | 同 repo 只审上次之后的新变更，任务详情给出"新增/复发/已消失"对比视图 |
| W1 全语言兜底检测 | 非 Go 文件的密钥/敏感信息/大文件删除等通用规则降级路径（自动探测语言，Go 之外不静默跳过） |
| W2 GOR/RES 跨函数分析 | 函数级 open/close 与 goroutine 退出配对（当前 hunk/文件级），收敛保守上报 |

**退出标准**：数据集 v2 ≥ 50 样本（含跨函数 hard 批次，标注先于实现），recall ≥ 85% 且 precision ≥ 92%；误报标记 → 降置信度闭环可演示；LLM 建议在 fake 模式下可复现；全量门禁不回退。

机动缓冲：2026-11-12 → 11-25（顺延或做 backlog：B7/B8 skill-run/session 真用、C4/C5 Agent/Graph 编排、C7 PR 机器人、C10 prompt 迭代、D8 PatchView 语义层重构、React 重构、规则在线编辑器）。

### 7.3 扩展任务明细（A/B/C/D 层完整任务库）

> 7.2 只列每期重点；这里是完整任务库。排期映射：**M0** = A1–A7 + D1（基础 CI + 提交校验）；**M1** = B1/B2/B5/B6；**M2** = D4/D5 + 数据集 hard 层 + evidence_chain；**M3** = B3/B4/D2；**M4** = C1/C2/C8；**M5** = C6 + D1（自举）；**M6** = D6 + E1–E5；**M7** = F1–F8；**M8** = C3/C9/D3/D7/W1/W2；其余为 backlog。工作量：S=小时级，M=天级，L=周级。

#### A 层：修复与加固（先做，全是小改动）

| # | 扩展项 | 落点 | 工作量 |
|---|--------|------|--------|
| A1 | 统一 Redactor（修 P0-1） | `findings.NewFinding` 或 report/storage 入口统一过 `safety.MaskSensitiveInfo`；补一条"报告和 DB 无明文密钥"的测试 | S |
| A2 | 输出目录自动创建（修 P0-2） | `main.go` 加 `os.MkdirAll` | S |
| A3 | local 沙箱错误处理（修 P1-4） | `sandbox/local.go:92` 接住 error 返回 | S |
| A4 | 监控统计修正（修 P1-5） | `main.go` 累加 TimedOut；ToolCallCount 改为真实计数 | S |
| A5 | 审计日志默认开启（修 P1-6） | 加 `--audit-file` 参数 → `safety.LoadConfig` → `NewSafetyFilter(cfg)` | S |
| A6 | 补 LICENSE 文件 | Apache-2.0 正文 | S |
| A7 | 提交规范落地 | CONTRIBUTING.md + `scripts/hooks/{commit-msg,pre-commit}` + `scripts/check_commits.sh` + CI 提交校验；Conventional Commits | S |

#### B 层：深度接入 trpc-agent-go（把"借鉴"变"真用"）

| # | 扩展项 | 做法 | 框架 API | 工作量 |
|---|--------|------|---------|--------|
| B1 | 真正加载 SKILL.md ✅（M1，2026-09-25） | `review.LoadSkillMeta()`：`skill.NewFSRepository` 加载 name/description，front matter 正则补 version，写入报告 `skill` 字段；降级不阻塞 | `skill.NewFSRepository` / `skill.Repository` | S |
| B2 | SafetyFilter 接入框架权限体系 ✅（M1，2026-09-25） | `safety.AsPermissionPolicy()` 适配器，沙箱命令经 `policy.CheckToolPermission`，决策落库 | `tool.PermissionPolicyFunc` | S |
| B3 | 换框架 container 沙箱 ✅（M3，container-fx 模式，手写版保留对照） | go.mod 加 `trpc.group/trpc-go/trpc-agent-go/codeexecutor/container` 子模块；`container.New(container.WithBindMount(repoPath, "/workspace"))` 替换手写 docker run；保留手写版做对比 | `container.New` + `Engine()` | M |
| B4 | E2B 云沙箱后端 ✅（M3，E2B_API_KEY/E2B_TEMPLATE，实机验证待有 key 环境） | `sandbox/e2b.go`：`e2b.New(e2b.WithAPIKey(os.Getenv("E2B_API_KEY")))` 实现 `Sandbox` 接口（接口已留好，`Name() "e2b"`） | `e2b.New` | M |
| B5 | artifact 链路打通 ✅（M1，2026-09-25） | `review/artifact.go`：报告 JSON/MD + 沙箱输出写 `cr_artifacts`，数量 ≤20 / 大小 ≤1MB / 扩展名白名单三重限制，被拒计数入 Monitor | `storage.SaveArtifact`（自建表，语义对齐框架 `artifact.Service`） | S |
| B6 | OTel 埋点 ✅（M1，2026-09-25） | `review.run` 主 span + `sandbox.exec` 子 span 全属性；`tracer()` 动态解析全局 provider，与框架 `telemetry/trace.Start()` 兼容 | `go.opentelemetry.io/otel`（框架遥测管线） | M |
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
| D2 | 接入更多静态工具 ✅（M3，staticcheck 已接入且 CI 实机验证出报告） | Dockerfile 已预装 staticcheck/golangci-lint 但 CLI 没用——沙箱命令列表加 `staticcheck ./...`，输出解析成 findings（`source: "tool:staticcheck"`） | M |
| D3 | go/types 类型增强 | `--repo-path` 模式下加载包类型信息：资源变量是否实现 io.Closer、函数是否真返回 error——把 ERR/RES 规则从"猜"变"知道" | L |
| D4 | DB 生命周期专门规则 | Begin→Commit/Rollback 配对检查（题目 7 类规则里唯一没专门做的） | S |
| D5 | 文件路径列表输入 | 补 `--files a.go,b.go` 第三种输入模式（能力 4 缺口） | S |
| D6 | HTML 交互式报告 | GUIDE 创新清单遗留项：可折叠 finding、severity 图表（M6 首个任务，用户需求的前端第一步） | M |
| D7 | 规则热加载 / 增量审查 | fsnotify 监听 rules 目录；按上次审查结果只审增量 | M |
| D8 | 按 `project_gap_and_innovation_review.txt` 的 PatchView 方案重构语义层 | 该文档第六节的"变更视图层→语义事实层→规则层"设计是现成的进阶蓝图 | L |

#### E 层：人用产品化（用户明确需求，M6 主战场）

| # | 扩展项 | 说明 | 工作量 |
|---|--------|------|--------|
| E1 | REST API 服务 ✅ lite 已落地 | 把 main.go 的 8 步流程抽成 `review.Run(opts)`；8 个端点见 §2.4；复用 `storage.Store`（接口已抽象，SQLite 起步，可换 Postgres） | M |
| E2 | Web 前端 SPA ✅ lite 已落地（vanilla + go:embed 单二进制） | v1.1 升级为 React/Vite：任务列表 / 报告详情 / 趋势看板 / 规则编辑 | L |
| E3 | 趋势统计 API → M7-F5 | 按天聚合任务数、评分分布、规则命中 TopN（SQL 聚合，顺带修 P2-11） | S |
| E4 | 规则管理 API | 规则列表 / YAML 校验 / 单 diff 试跑 | M |
| E5 | 部署形态 → M7-F7 | 单二进制内嵌前端（✅ 已实现）；Docker compose（服务 + 数据卷）+ TLS 反代与备份文档 | S |

#### F 层：上线运营（M7 主战场，2026-09-28 规划）

> 上线三问：多用户互不阻塞吗（F1）、暴露面可控吗（F2/F4）、不会用命令行的人能用吗（F3）。

| # | 扩展项 | 说明 | 工作量 |
|---|--------|------|--------|
| F1 | 异步任务模型 | `202 + task_id` 即返回；worker 队列（并发可配）+ 任务状态机 + 超时回收；前端轮询进度；替代全局互斥（P3-12 根治） | M |
| F2 | 认证与请求边界 | `--auth-token` 写操作认证、浏览可公开只读；IP 令牌桶限流；`MaxBytesReader` 10MB；单请求 context 超时 | M |
| F3 | 零门槛输入 | zip/多文件上传、粘贴整文件（按新增行审查）、GitHub PR URL 拉取（GITHUB_TOKEN 可选） | M |
| F4 | 仓库路径白名单 | `--allow-repo` 前缀白名单 + 路径规范校验（封掉任意主机路径） | S |
| F5 | 趋势看板 | 风险分冗余进任务表 + SQL 按天聚合 + 前端趋势视图（E3 落地，修 P2-11） | S |
| F6 | HTML 单文件报告 | D6 落地：自包含、severity 筛选、六维图，可离线转发 | M |
| F7 | Docker compose 部署 | 一条命令自部署 + TLS 反代说明 + 备份/升级文档 | S |
| F8 | 服务语义修复 | P2-10（0 新增行 → 422 友好提示）/ P3-12（repo 不存在 → 400、`errors.Is`、Version 统一注入） | S |

#### W 层：智能化（M8 主战场，2026-09-28 规划）

| # | 扩展项 | 说明 | 工作量 |
|---|--------|------|--------|
| W1 | 全语言兜底检测 | 非 Go 文件自动走密钥/敏感信息/大删除等通用规则（语言自动探测，Go 之外不静默跳过） | M |
| W2 | GOR/RES 跨函数分析 | 函数级 open/close 与 goroutine 退出配对分析（当前 hunk/文件级），收敛保守上报 | L |

> 选型说明：框架的 `server/agui` 面向"对话式 Agent"UI，本项目 LLM 不在主链路，人用界面走 REST + SPA 更合适；MCP（C6，M5）负责"Agent 客户端调用"这一形态。两条线互补不冲突。

### 7.4 数据集演进计划

| 版本 | 时间点 | 规模 | 内容 | 对应质量门禁 |
|------|--------|------|------|-------------|
| v0（已完成） | 2026-09-23 | 20 | easy/medium 正样本 + trap 陷阱负样本 + 脱敏样本 | recall ≥ 80%，precision ≥ 85%，negFPR ≤ 15%，脱敏 SKIP→PASS |
| v1 | ✅ M2（2026-09-27，30 样本） | 30 | +hard 层 10 样本（间接密钥/struct tag/跨 hunk/生成文件/别名导入/DB 生命周期正负）、evidence_chain 脱敏断言 | recall 100% / precision 100% / negFPR 0% / 脱敏 0 泄漏（门禁 85/90/10） |
| v1.5 | ✅ 2026-09-28（39 样本） | 39 | +hard2 批次 9 样本（**标注先于实现盲评**）：双文件混合/panic 业务路径/DSN+丢错双命中/具名 goroutine/库 log.Fatal；陷阱：defer 配对/os.ReadFile 自关闭+wrap/errgroup/占位符 URL。**首跑抓出 RES-AST-001 构造器误报（P2-9）并修复** | 同 v1 门禁，39 样本 100%/100%/0% |
| v2 | M8 | ≥ 50 | +跨函数 hard 批次（配对跨函数体/句柄存结构体字段）、LLM 修复建议质量样本 | recall ≥ 85%，precision ≥ 92%，LLM 建议样本合格率 ≥ 80% |

**标注纪律**：hard 样本先手写标注再跑引擎（防止"照抄实现"导致数据集失去检验能力）；每个里程碑结束后抽查标注与实现的独立性。

### 7.5 指标看板（每个里程碑结束时更新此表）

| 指标 | 基线 09-23 | M0 门禁 | M2 门禁 | v1.0 门禁 | 当前实际（09-28） |
|------|-----------|---------|---------|-----------|---------|
| 数据集样本数 | 20 | 20 | ≥ 50 | ≥ 60 | **39**（v1 hard 10 + v1.5 hard2 盲评 9 已入） |
| 检出率 recall | 100% | ≥ 80% | ≥ 85% | ≥ 85% | **100%**（门禁 85%） |
| 精确率 precision | 100% | ≥ 85% | ≥ 90% | ≥ 90% | **100%**（门禁 90%；hard2 盲评首跑 97%，修复 P2-9 后恢复） |
| 负样本误报率 | 0% | ≤ 15% | ≤ 10% | ≤ 10% | **0%**（17 负样本含 9 个 hard/hard2 陷阱） |
| 脱敏泄漏 | 2（P0-1） | 0（硬门禁） | 0 | 0 | **0**（硬门禁 PASS；前端展开态 DOM 实测亦 0） |
| 红色项 | 脱敏 | — | — | — | **无** |

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
2. **提交纪律**：一个任务一个提交，message 引用任务编号（如 `fix(A1): unify evidence redaction`）。格式为 Conventional Commits（`type(scope): subject`，type ∈ feat/fix/docs/style/refactor/perf/test/build/ci/chore/revert），由 `scripts/hooks/commit-msg`（本地，`bash scripts/install-hooks.sh` 一键安装）与 CI `commit-check` job（远端）双层强制。
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
