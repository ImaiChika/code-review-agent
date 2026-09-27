#!/bin/sh
# 把 scripts/hooks/ 下的钩子安装到本仓库 .git/hooks/（M0-A7）。
# git 钩子不随克隆分发，每个克隆环境执行一次即可。

set -e

root="$(git rev-parse --show-toplevel)"

for hook in commit-msg pre-commit; do
  if [ ! -f "$root/scripts/hooks/$hook" ]; then
    echo "✗ 找不到 $root/scripts/hooks/$hook" >&2
    exit 1
  fi
  cp "$root/scripts/hooks/$hook" "$root/.git/hooks/$hook"
  chmod +x "$root/.git/hooks/$hook"
  echo "✓ 已安装 $hook"
done

echo ""
echo "钩子只做轻量本地检查（提交信息格式 + gofmt）；"
echo "vet / 全量测试 / 数据集门禁 / 提交信息校验由 GitHub Actions 强制。"
