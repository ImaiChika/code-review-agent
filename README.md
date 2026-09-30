# Code Review Agent

基于 tRPC-Agent-Go 框架的自动代码审查系统。

## 功能特性

- **Token 感知的规则引擎**：基于 `go/scanner` 词法分析，不是简单正则匹配
- **10 条内置规则**：硬编码密钥、敏感信息泄漏（含 GitHub/GitLab/Google/npm/SendGrid/Bearer 前缀）、goroutine 泄漏、context 取消泄漏、资源泄漏、SQL 拼接注入、命令注入、错误处理、测试缺失、DB 事务生命周期
- **YAML 规则 DSL**：用户可用 YAML 自定义规则，不需要写 Go 代码
- **风险评分系统**：0-100 分量化评分，带多维度 breakdown
- **SQLite 存储**：审查结果持久化，支持按任务查询
- **安全过滤器**：命令执行前的安全检查 + 审计日志
- **沙箱执行**：基于 trpc-agent-go 的 codeexecutor/local
- **敏感信息脱敏**：自动检测并脱敏 10 种敏感信息类型

## 快速开始

> 📖 面向所有人的白话版使用说明（不需要编程背景）见 [PROJECT_GUIDE.md 第零节](PROJECT_GUIDE.md#零写给所有人的使用说明书白话版)。

### Web 控制台（推荐体验）

```bash
scripts/start.sh             # 一键启动，默认 http://localhost:8080
scripts/stop.sh              # 一键关闭
PORT=9090 scripts/start.sh   # 自定义端口
```

浏览器打开 <http://localhost:8080>：**总览看板**（统计/风险分布/评分权重/业务管线）、**新建审查**（粘贴 diff 或一键载入内置示例，实时出结果）、**任务记录**（历史档案，含 findings 证据、沙箱执行、权限决策）、**规则引擎**（内置规则 + YAML DSL 展示）。

服务与 CLI 共用同一套审查管线（`review` 包），看到的即真实业务逻辑；前端内嵌在二进制里（go:embed），单文件部署、无外部依赖。

### 安装与 CLI

```bash
go build -o code-review-agent .
```

### 使用

```bash
# 审查 diff 文件
./code-review-agent --diff-file changes.diff

# 审查文件路径列表（M2-D5：整体按新增行审查，适合新文件/CI 指定文件）
./code-review-agent --files "a.go,b.go"

# 审查 git 仓库变更
./code-review-agent --repo-path /path/to/repo

# 启动 HTTP 服务（Web 前端 + REST API，等价于 scripts/start.sh）
./code-review-agent serve --port 8080

# dry-run 模式（不写数据库）
./code-review-agent --diff-file changes.diff --dry-run

# 详细输出
./code-review-agent --diff-file changes.diff --verbose

# 使用自定义 YAML 规则
./code-review-agent --diff-file changes.diff --rules-dir ./rules/custom

# 指定输出目录
./code-review-agent --diff-file changes.diff --output ./reports
```

### 命令行参数

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `--diff-file` | - | diff 文件路径 |
| `--repo-path` | - | git 仓库路径 |
| `--rules-dir` | - | 自定义 YAML 规则目录 |
| `--db` | `review.db` | SQLite 数据库路径 |
| `--output` | `.` | 报告输出目录（不存在会自动创建） |
| `--sandbox` | `container` | 沙箱模式：`container`（手写 docker run）/ `container-fx`（框架 Docker SDK）/ `e2b`（云沙箱，需 `E2B_API_KEY`）/ `local`（仅 `--repo-path` 且非 dry-run 时执行；失败自动回退） |
| `--audit-file` | `tool_safety_audit.jsonl` | 安全审计日志 JSONL 路径（纯文件名落在 `--output` 目录下，传空禁用） |
| `--fake-model` | false | 内置确定性假模型 LLM 复核（M4-C2：无 API Key 全链路可复现） |
| `--llm` | - | LLM 复核降噪（M4-C1）：`openai`（兼容 API；配 `--llm-model`、`--llm-base-url` 可指向 ollama/vLLM，需 `OPENAI_API_KEY`；DENY 剔除候选降误报） |
| `--dry-run` | false | 不写数据库 |
| `--verbose` | false | 详细输出 |

### Docker 部署（5 分钟自部署，M7-F7）

```bash
docker compose up -d      # 首次自动构建镜像，随后启动
# 浏览器打开 http://localhost:8080
docker compose logs -f    # 看日志；docker compose down 停止（数据保留）
```

- 数据（SQLite + 报告 + 审计日志）全部落在卷 `cra-data`，升级镜像不丢数据；备份即 `docker run --rm -v cra-data:/data -v $PWD:/b alpine cp /data/review.db /b/`。
- 可选配置写入 `.env`：`AUTH_TOKEN`（写操作认证，设置后分发 `http://host:8080/?token=xxx` 链接）、`GITHUB_TOKEN`（提升 PR 拉取限额）。
- 粘贴 diff / 粘贴代码 / 上传文件 / GitHub PR 四种入口无需任何额外挂载；如需在容器内审查本机仓库（repo 模式），把仓库目录只读挂进容器并在 `.env` 设 `ALLOW_REPOS=/repos`（容器内路径），见 `docker-compose.yml` 内注释。
- 反向代理 TLS（nginx/Caddy）按常规 HTTP 服务转发 8080 即可；对外强烈建议同时设置 `AUTH_TOKEN`。

## 项目结构

```
code-review-agent/
├── main.go              # CLI 入口 + serve 子命令（薄壳）
├── review/              # 审查管线（8 步流程，CLI 与 API 共用）
├── server/              # HTTP 服务：REST API + 内嵌 Web 前端（web/）
├── scripts/             # start.sh / stop.sh 一键启停、提交规范脚本
├── analyzer/            # Go AST + Token 分析器
│   ├── analyzer.go      # go/ast 完整文件分析
│   └── token.go         # go/scanner 逐行分析
├── diff/                # unified diff 解析器
│   └── parser.go        # diff 解析 + Go 包名提取
├── findings/            # Finding 结构体 + 去重
│   ├── finding.go       # Finding 定义
│   └── dedup.go         # 去重和分组
├── report/              # 报告生成
│   ├── report.go        # JSON/Markdown 报告
│   └── markdown.go      # Markdown 格式化
├── rules/               # 规则引擎
│   ├── rule.go          # Rule 接口
│   ├── engine.go        # 规则引擎
│   ├── token_rules.go   # Token 感知规则（SEC/GOR/RES/ERR/TST/DB）
│   ├── context_leak.go  # CTX-AST-001（R1）
│   ├── sql_injection.go / command_injection.go  # SEC-AST-003/004（R1）
│   ├── dsl.go           # YAML 规则 DSL 加载器
│   └── custom/          # 自定义 YAML 规则示例
├── safety/              # 安全过滤器 + 脱敏
│   ├── filter.go        # 命令安全检查
│   └── mask.go          # 敏感信息脱敏
├── sandbox/             # 沙箱执行
│   ├── sandbox.go       # Sandbox 接口
│   └── local.go         # 本地执行（基于 trpc-agent-go）
├── scoring/             # 风险评分系统
│   └── scoring.go       # 0-100 分 + 维度 breakdown
├── storage/             # SQLite 存储
│   └── storage.go       # 5 张表 + CRUD
└── testdata/            # 8 条测试样例
```

## 内置规则

| 规则 ID | 名称 | 检测内容 |
|---------|------|---------|
| SEC-AST-001 | Token 感知的密钥检测 | 硬编码密码、API Key |
| SEC-AST-002 | Token 感知的敏感信息泄漏 | AWS Key、GitHub Token、私钥、JWT |
| GOR-AST-001 | Token 感知的 goroutine 泄漏 | 无退出机制的 goroutine |
| RES-AST-001 | Token 感知的资源泄漏 | 未关闭的文件、连接、HTTP 响应 |
| ERR-AST-001 | Token 感知的错误处理 | 忽略 error、panic、log.Fatal |
| TST-AST-001 | Token 感知的测试缺失 | 新增导出函数无测试 |
| DB-AST-001 | DB 事务生命周期（M2-D4） | Begin/BeginTx 后无 Commit/Rollback 配对 |
| CTX-AST-001 | context 取消泄漏（R1） | WithCancel/WithTimeout/WithDeadline 的 cancel 未调用 |
| SEC-AST-003 | SQL 拼接注入（R1） | SQL 语句与变量 + 拼接 / fmt.Sprintf 格式化，参数化（?/$1）豁免 |
| SEC-AST-004 | 命令注入（R1） | exec.Command(Context) 可执行文件或 shell -c 参数来自变量 |

## YAML 自定义规则

在 `rules/custom/` 目录下创建 `.yaml` 文件：

```yaml
rules:
  - id: MY-001
    name: "检测硬编码端口"
    severity: medium
    category: security
    match:
      token_facts:
        - kind: identifier
          value_contains: ["port"]
        - kind: string_literal
    exclude:
      line_contains: ["localhost"]
    message: "疑似硬编码端口号"
    recommendation: "使用配置文件管理端口"
```

## 风险评分

评分维度（0-100 分）：

| 维度 | 权重 | 说明 |
|------|------|------|
| 安全问题 | 30% | 密钥泄漏、注入风险 |
| 敏感信息 | 15% | AWS Key、Token 泄漏 |
| 资源泄漏 | 20% | goroutine、文件、连接 |
| 错误处理 | 15% | 忽略 error、panic |
| 测试覆盖 | 15% | 缺少测试 |
| 并发问题 | 5% | 数据竞争 |

等级划分：A（0-20）→ B（20-40）→ C（40-60）→ D（60-80）→ F（80-100）

## 技术栈

| 组件 | 技术 |
|------|------|
| 代码分析 | go/ast + go/scanner（标准库） |
| 规则引擎 | 自定义 Token 感知引擎 |
| 规则 DSL | YAML（gopkg.in/yaml.v3） |
| 存储 | SQLite（trpc-agent-go session/sqlite） |
| 沙箱 | trpc-agent-go codeexecutor/local |
| 安全过滤 | 自定义安全策略 + 审计日志 |

## 测试

```bash
# 运行所有测试
go test ./...

# 运行特定包测试
go test ./rules/ -v
go test ./analyzer/ -v

# 运行验收测试
go test ./... -count=1 -timeout 60s
```

## 开发

提交规范（Conventional Commits）、本地 Git 钩子与 CI 门禁说明见 [CONTRIBUTING.md](CONTRIBUTING.md)。克隆后执行一次 `bash scripts/install-hooks.sh` 安装本地钩子。

## License

Apache License 2.0
