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

mkdir -p "$work/fixture"
printf 'module fixture\n\ngo 1.21\n' > "$work/fixture/go.mod"
cat > "$work/fixture/main.go" <<'FIXTURE'
package main

func main() {}

func unusedHelper() {} // U1000: func unusedHelper is unused
FIXTURE

cd "$work/fixture"
git init -q .
git config user.email t@t
git config user.name t
git add . && git commit -qm "chore: baseline"
printf '\n// trigger non-empty diff\n' >> main.go

out="$work/out"
mkdir -p "$out"

cd "$root"
echo "▶ 自举审查（--repo-path + --sandbox container，镜像应选 cr-sandbox）…"
./code-review-agent --repo-path "$work/fixture" --sandbox container --db "$work/review.db" --output "$out"

echo "▶ 沙箱命令执行情况（含输出首行，用于诊断）："
sqlite3 "$work/review.db" "SELECT command || ' => exit ' || exit_code || ' | ' || substr(replace(output, char(10), ' / '), 1, 200) FROM cr_sandbox_runs;"

mkdir -p .run/staticcheck-live
cp "$out/review_report.json" .run/staticcheck-live/

python3 - "$out/review_report.json" <<'PYASSERT'
import json, sys
r = json.load(open(sys.argv[1]))
allf = (r.get("findings") or []) + (r.get("warnings") or [])  # Go nil slice → JSON null
sc = [f for f in allf if f["rule_id"].startswith("STATICCHECK-")]
print("STATICCHECK 发现:", [(f["rule_id"], f["file"], f["line"], f["source"]) for f in sc])
assert sc, "报告未出现 STATICCHECK-* 发现（D2 实机验证失败）"
PYASSERT

echo "✓ staticcheck 实机验证通过（报告见 .run/staticcheck-live/review_report.json）"
