#!/bin/bash
# easy-stock Web 工作台（持久版）
# - 目录: ~/workspace/easy-stock-web（对 easy-stock-analysis/frontend 的部署副本，
#   含两处本地适配：vite.config.ts 加 /api 代理 -> 后端；backend.ts 默认用 window.location.origin）
# - 后端: http://127.0.0.1:20081（独立服务，见 ~/workspace/easy-stock-service）
# - 本服务: http://127.0.0.1:20073（仅 loopback；公网访问请走 cloudflare quick tunnel）
# - token 从 ~/workspace/easy-stock-service/.env 的 A_STOCK_TOKEN 读取，不落盘、不打印
set -e
WEB_DIR="$HOME/workspace/easy-stock-web"
SERVICE_DIR="$HOME/workspace/easy-stock-service"
LOG_DIR="$WEB_DIR/logs"
mkdir -p "$LOG_DIR"

if curl -sf -m 3 http://127.0.0.1:20073/ >/dev/null 2>&1; then
  echo "web already running on 127.0.0.1:20073"
  exit 0
fi

if ! curl -sf -m 5 http://127.0.0.1:20081/api/health >/dev/null 2>&1; then
  echo "backend not healthy, starting it first..."
  bash "$SERVICE_DIR/start.sh"
  sleep 3
fi

set -a
# shellcheck disable=SC1091
source "$SERVICE_DIR/.env"   # 提供 A_STOCK_TOKEN
set +a
export VITE_A_STOCK_TOKEN="$A_STOCK_TOKEN"
unset VITE_A_STOCK_BACKEND_URL   # 为空 -> 前端使用 window.location.origin，经 vite /api 代理访问后端

cd "$WEB_DIR"
nohup npx vite --host 127.0.0.1 --port 20073 > "$LOG_DIR/vite.log" 2>&1 &
echo "web started, pid $!"
