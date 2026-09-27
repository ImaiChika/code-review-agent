#!/bin/sh
# M3-D2 沙箱 staticcheck 实机验证：
# 构建 cr-sandbox 镜像（预装 staticcheck）→ 审查一个含已知 SA4006 问题的 fixture →
# 断言报告出现 STATICCHECK-* 发现。CI（ubuntu runner 自带 Docker）与本地均可运行。
set -e

root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"

echo "▶ 构建 agent …"
go build -o code-review-agent .

echo "▶ 构建 cr-sandbox 镜像（固定版本 staticcheck/golangci-lint）…"
docker build -q -t cr-sandbox . >/dev/null

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# fixture：已知 staticcheck 问题（SA4006: value never used）
mkdir -p "$work/fixture"
cat > "$work/fixture/go.mod" <<'EOF'
module fixture

go 1.21
