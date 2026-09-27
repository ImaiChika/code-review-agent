# CONTRIBUTING.md

感谢参与 Code Review Agent！本文档说明如何构建、测试和提交代码。

## 环境要求

| 依赖 | 要求 |
|------|------|
| Go | ≥ 1.21 |
| CGO | 需要（`mattn/go-sqlite3`）；macOS 自带 clang，Linux 需 gcc |
| Docker | 可选，container 沙箱模式（不可用自动回退 local） |

## 常用命令

```bash
go build -o code-review-agent .        # 构建
go test ./... -count=1                 # 全量测试（含数据集质量门禁）
go test ./... -count=1 -race           # 竞态检测
go test -run TestDataset -v .          # 数据集质量指标（检出率/精确率/误报/脱敏）
go vet ./...                           # 静态检查
gofmt -l .                             # 格式检查（无输出 = 通过）
```

## 提交规范（Conventional Commits）

提交信息格式：`type(scope): subject`

| type | 用途 |
|------|------|
| `feat` | 新功能 |
| `fix` | 缺陷修复 |
| `docs` | 文档 |
| `style` | 格式（不影响代码含义） |
| `refactor` | 重构（既非新增也非修复） |
| `perf` | 性能优化 |
| `test` | 测试 |
| `build` | 构建/依赖变更 |
| `ci` | CI 配置 |
| `chore` | 杂项 |

约定：

- **scope 引用任务编号**（PROJECT_GUIDE §7.3 的 A/B/C/D/E 编号），如 `fix(A1): unify evidence redaction`。
- subject 用英文小写祈使句，结尾不加句号。
- **一个任务一个提交**，不把多个任务混进一个提交。

示例：

```
feat: add --audit-file flag for safety audit log
fix(A3): surface local sandbox infra errors instead of swallowing them
docs: mark M0 done in PROJECT_GUIDE
```

## 本地 Git 钩子

钩子不随克隆分发，克隆后执行一次：

```bash
bash scripts/install-hooks.sh
```

- `commit-msg`：校验提交信息符合 Conventional Commits（Merge/Revert 放行）。
- `pre-commit`：暂存的 Go 文件必须已 `gofmt`。

钩子只做轻量检查；`go vet`、全量测试、数据集门禁、提交信息校验由 CI 强制。

## 门禁纪律（红不合入）

CI（`.github/workflows/ci.yml`）在 PR 上运行：

1. `gofmt` 检查
2. `go vet ./...`
3. `go test ./... -race`（**含数据集质量门禁**：检出率、精确率、负样本误报率、脱敏 0 泄漏）
4. 提交信息 Conventional Commits 校验（`scripts/check_commits.sh`）

新增/修改规则必须带数据集正负样本（标注先于实现手写，见 `dataset/README.md`）；
数据集门禁红了不合代码。
