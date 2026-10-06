#!/bin/bash
# Idempotent start: only launches if the backend isn't already listening.
SVC_DIR="$(cd "$(dirname "$0")" && pwd)"
set -a; source "$SVC_DIR/.env"; set +a
if curl -sf -m 5 "http://${A_STOCK_ADDR}/api/health" >/dev/null 2>&1; then
  echo "already running on $A_STOCK_ADDR"
  exit 0
fi
cd "$SVC_DIR"
nohup "$SVC_DIR/bin/easy-stock-backend" >> "$SVC_DIR/logs/stdout.log" 2>&1 &
echo "started pid $!"
