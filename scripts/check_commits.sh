#!/bin/sh
# 校验一个 commit 区间内所有提交信息符合 Conventional Commits（M0-A7）。
# CI 的 commit-check job 调用；也可本地手动运行。
#
# 用法:
#   scripts/check_commits.sh <git-range>
#   例: scripts/check_commits.sh origin/main..HEAD
#
# 规则与 scripts/hooks/commit-msg 保持一致：
#   type(scope): subject
#   type ∈ feat fix docs style refactor perf test build ci chore revert
#   Merge / Revert 开头的 git 生成信息放行。

set -e

range="${1:?用法: $0 <git-range>，例如 origin/main..HEAD}"

# 确认区间合法（无提交时 git rev-list 返回空，直接通过）
commits=$(git rev-list "$range")

status=0
pattern='^(feat|fix|docs|style|refactor|perf|test|build|ci|chore|revert)(\([A-Za-z0-9._/-]+\))?!?: .+'

for sha in $commits; do
  subject=$(git log -1 --format=%s "$sha")
  case "$subject" in
    Merge\ *|Revert\ *) continue ;;
  esac
  if ! printf '%s\n' "$subject" | grep -qE "$pattern"; then
    echo "✗ 提交信息不符合 Conventional Commits: ${sha}  \"$subject\"" >&2
    status=1
  fi
done

if [ "$status" -eq 0 ]; then
  echo "✓ 提交信息检查通过"
fi
exit $status
