#!/bin/bash
# restore.sh — easy-stock 灾难恢复一键脚本
# 场景: VM 的 home 被清空后,从备份重建整套系统
# 用法: 把 easy-stock-release-*.tar.gz、easy-stock-keys-*.tar.gz、restore.sh 三个文件放在同一目录,执行:
#       bash restore.sh
# 脚本会: 解包 -> 灌入密钥(600权限) -> 重建前端依赖 -> 补 Python 依赖 -> 拉起后端/前端 -> 跑 recover.sh 验收
set -u
HERE="$(cd "$(dirname "$0")" && pwd)"
WS="$HOME/workspace"
SVC="$WS/easy-stock-service"
WEB="$WS/easy-stock-web"

REL_TGZ="$(ls "$HERE"/easy-stock-release-*.tar.gz 2>/dev/null | head -1)"
KEY_TGZ="$(ls "$HERE"/easy-stock-keys-*.tar.gz 2>/dev/null | head -1)"
[ -n "${REL_TGZ:-}" ] || { echo "FAIL: 同目录下找不到 easy-stock-release-*.tar.gz"; exit 1; }
[ -n "${KEY_TGZ:-}" ] || { echo "FAIL: 同目录下找不到 easy-stock-keys-*.tar.gz"; exit 1; }

echo "== 1/6 解恢复包 -> $WS"
mkdir -p "$WS"
tar -xzf "$REL_TGZ" -C "$WS" --strip-components=1
[ -f "$SVC/strategy.yaml" ] || { echo "FAIL: 解包异常,找不到 strategy.yaml"; exit 1; }
echo "OK 解包完成"

echo "== 2/6 灌入密钥(.env / push_config.json,权限600)"
tar -xzf "$KEY_TGZ" -C "$SVC"
chmod 600 "$SVC/.env" "$SVC/push_config.json"
echo "OK 密钥就位"

echo "== 3/6 前端依赖"
if [ ! -d "$WEB/node_modules" ]; then
  if command -v npm >/dev/null 2>&1; then
    (cd "$WEB" && npm ci) && echo "OK npm ci 完成" || echo "WARN npm ci 失败,前端稍后手动处理"
  else
    echo "WARN 无 npm,跳过前端依赖安装"
  fi
else
  echo "OK node_modules 已存在,跳过"
fi

echo "== 4/6 Python 依赖"
if python3 -c "import numpy, requests, yaml, lxml.etree" 2>/dev/null; then
  echo "OK 依赖齐全,跳过"
else
  mkdir -p "$HOME/.local/lib/python3.12/site-packages"
  pip install --target="$HOME/.local/lib/python3.12/site-packages" --break-system-packages --quiet \
    "numpy==1.26.4" requests pyyaml pandas lxml matplotlib \
    && echo "OK 依赖已补齐" || echo "WARN pip 安装失败,请手动处理"
fi

echo "== 5/6 拉起服务"
bash "$SVC/start.sh"; sleep 3
bash "$WEB/start.sh"; sleep 5

echo "== 6/6 验收"
bash "$SVC/recover.sh"
