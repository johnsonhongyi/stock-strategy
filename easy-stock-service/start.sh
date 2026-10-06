#!/bin/bash
# Idempotent start: only launches if the backend isn't already listening.
SVC_DIR="$(cd "$(dirname "$0")" && pwd)"
if [[ -f "$SVC_DIR/.env" ]]; then
  set -a
  source "$SVC_DIR/.env"
  set +a
fi
if curl -sf -m 5 "http://${A_STOCK_ADDR}/api/health" >/dev/null 2>&1; then
  echo "already running on $A_STOCK_ADDR"
  exit 0
fi
cd "$SVC_DIR"
mkdir -p "$SVC_DIR/logs"
if ! PYTHONPATH="$SVC_DIR${PYTHONPATH:+:$PYTHONPATH}" python3 -c 'import bars; conn=bars.db(); conn.close()'; then
  echo "market data database initialization failed; backend not started" >&2
  exit 1
fi
nohup "$SVC_DIR/bin/easy-stock-backend" >> "$SVC_DIR/logs/stdout.log" 2>&1 &
echo "started pid $!"
