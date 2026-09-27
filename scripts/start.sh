#!/bin/sh
# 一键启动 Code Review Agent 服务（REST API + 内嵌 Web 前端）。
#
# 用法:
#   scripts/start.sh                 # 默认端口 8080
#   PORT=9090 scripts/start.sh      # 指定端口
#
# 运行时文件（日志/PID/二进制）在 .run/ 目录（已 gitignore）。
# 对应关闭脚本: scripts/stop.sh

set -e

root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"

PORT="${PORT:-8080}"
run_dir="$root/.run"
mkdir -p "$run_dir"
log_file="$run_dir/server-$PORT.log"
pid_file="$run_dir/server-$PORT.pid"

# 0) 已在运行？
if [ -f "$pid_file" ] && kill -0 "$(cat "$pid_file")" 2>/dev/null; then
  echo "✗ 服务已在运行 (PID $(cat "$pid_file"), http://localhost:$PORT)。"
  echo "  如需重启: scripts/stop.sh && scripts/start.sh"
  exit 1
fi

# 1) 端口被占？
if lsof -i ":$PORT" -sTCP:LISTEN >/dev/null 2>&1; then
  echo "✗ 端口 $PORT 已被占用："
  lsof -i ":$PORT" -sTCP:LISTEN | tail -1
  exit 1
fi

# 2) 按需构建（Go 源码或内嵌 web 资源比缓存二进制新时重建）
bin="$run_dir/code-review-agent"
src_newer() {
  find "$root" -type f \( -name '*.go' -o -name '*.js' -o -name '*.css' -o -name '*.html' \) \
    -not -path "$run_dir/*" -not -path "$root/.git/*" -newer "$bin" 2>/dev/null | head -1
}
needs_build=0
if [ ! -x "$bin" ]; then
  needs_build=1
elif [ -n "$(src_newer)" ]; then
  needs_build=1
fi
if [ "$needs_build" = "1" ]; then
  echo "▶ 构建二进制 …"
  go build -o "$bin" . || { echo "✗ 构建失败"; exit 1; }
fi

# 3) 后台启动
echo "▶ 启动服务 (端口 $PORT) …"
nohup "$bin" serve --port "$PORT" >> "$log_file" 2>&1 &
echo $! > "$pid_file"

# 4) 健康检查（最长 6 秒）
i=0
while [ $i -lt 30 ]; do
  if curl -sf "http://localhost:$PORT/api/health" >/dev/null 2>&1; then
    echo ""
    echo "✓ 服务已启动: http://localhost:$PORT"
    echo "  PID: $(cat "$pid_file")   日志: $log_file"
    echo "  停止: scripts/stop.sh"
    exit 0
  fi
  if ! kill -0 "$(cat "$pid_file")" 2>/dev/null; then
    echo "✗ 服务进程退出，日志尾部："
    tail -5 "$log_file" || true
    rm -f "$pid_file"
    exit 1
  fi
  i=$((i + 1))
  sleep 0.2
done

echo "✗ 健康检查超时（6s），日志尾部："
tail -5 "$log_file" || true
exit 1
