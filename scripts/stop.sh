#!/bin/sh
# 一键关闭 Code Review Agent 服务。
#
# 用法:
#   scripts/stop.sh          # 关闭默认端口 8080 的服务
#   PORT=9090 scripts/stop.sh
#
# 优先按 PID 文件停止；PID 文件丢失时按端口兜底清理。

root="$(cd "$(dirname "$0")/.." && pwd)"
PORT="${PORT:-8080}"
pid_file="$root/.run/server-$PORT.pid"

stopped=0

# 1) 按 PID 文件停止
if [ -f "$pid_file" ]; then
  pid=$(cat "$pid_file")
  if kill -0 "$pid" 2>/dev/null; then
    echo "▶ 停止服务 (PID $pid) …"
    kill "$pid" 2>/dev/null
    i=0
    while kill -0 "$pid" 2>/dev/null && [ $i -lt 50 ]; do
      i=$((i + 1))
      sleep 0.1
    done
    if kill -0 "$pid" 2>/dev/null; then
      echo "  进程未响应，强制终止 …"
      kill -9 "$pid" 2>/dev/null
    fi
    echo "✓ 已停止 (http://localhost:$PORT)"
  else
    echo "· PID $pid 已不存在（清理 PID 文件）"
  fi
  rm -f "$pid_file"
  stopped=1
fi

# 2) 兜底：按端口清理残留
if lsof -ti ":$PORT" >/dev/null 2>&1; then
  echo "▶ 端口 $PORT 仍有监听进程，按端口清理 …"
  lsof -ti ":$PORT" | xargs kill 2>/dev/null || true
  sleep 0.5
  if lsof -ti ":$PORT" >/dev/null 2>&1; then
    lsof -ti ":$PORT" | xargs kill -9 2>/dev/null || true
  fi
  if lsof -ti ":$PORT" >/dev/null 2>&1; then
    echo "✗ 端口 $PORT 仍被占用，请手动检查: lsof -i :$PORT"
    exit 1
  fi
  echo "✓ 端口 $PORT 已释放"
  stopped=1
fi

if [ "$stopped" -eq 0 ]; then
  echo "· 没有发现运行中的服务 (端口 $PORT)"
fi
