#!/bin/bash
# recover.sh — VM 更换 / 服务异常后的一键自检与自愈
# 用法: bash ~/workspace/easy-stock-service/recover.sh
# 只做: 检查 -> 拉起缺失的进程 -> 复检并报告。不删数据、不改配置。
set -u
SVC="$HOME/workspace/easy-stock-service"
WEB="$HOME/workspace/easy-stock-web"
ok=0; fail=0

say()  { printf '%s\n' "$1"; }
pass() { say "OK   $1"; ok=$((ok+1)); }
miss() { say "FAIL $1"; fail=$((fail+1)); }

say "== 1/3 进程与依赖检查 =="
curl -sf -m 10 http://127.0.0.1:20081/api/health >/dev/null 2>&1 && pass "后端 :20081" || miss "后端 :20081"
curl -sf -m 10 http://127.0.0.1:20073/ >/dev/null 2>&1 && pass "前端 :20073" || miss "前端 :20073"
python3 -c "import numpy, requests, yaml, lxml.etree" 2>/dev/null && pass "Python 依赖(numpy/requests/yaml/lxml)" || miss "Python 依赖"
python3 -c "import yaml; yaml.safe_load(open('$SVC/strategy.yaml'))" 2>/dev/null && pass "strategy.yaml 可解析" || miss "strategy.yaml"

say "== 2/3 数据与账本存在性 =="
for f in data/bars.db logs/paper_ledger.json logs/paper_ledger_us.json logs/paper_ledger_crypto.json .env push_config.json; do
  [ -f "$SVC/$f" ] && pass "$f" || miss "$f"
done

say "== 3/3 自愈拉起 =="
if ! curl -sf -m 5 http://127.0.0.1:20081/api/health >/dev/null 2>&1; then
  say ".. 后端未响应,执行 start.sh"; bash "$SVC/start.sh"; sleep 5
  curl -sf -m 5 http://127.0.0.1:20081/api/health >/dev/null 2>&1 && pass "后端已拉起" || miss "后端拉起失败,看 $SVC/logs/stdout.log"
fi
if ! curl -sf -m 5 http://127.0.0.1:20073/ >/dev/null 2>&1; then
  say ".. 前端未响应,执行 web start.sh"; bash "$WEB/start.sh"; sleep 8
  curl -sf -m 5 http://127.0.0.1:20073/ >/dev/null 2>&1 && pass "前端已拉起" || miss "前端拉起失败,看 $WEB/logs/"
fi

say "== 结果: 通过 $ok / 失败 $fail =="
[ "$fail" -eq 0 ]
