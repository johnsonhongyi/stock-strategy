#!/usr/bin/env python3
"""盘前结构扫描:对重点股先跑通道结构,再跑策略命中。先有结构,才有策略。

逻辑(与用户 ATS v2 / pyQuant3 SSOT 对齐):
- 通道三轨/支撑线: channel_structure.py (tdx_channel_factory 纯 numpy 移植)
- 四套策略: 通道经典双共振 / 极致共振 / 小连阳启动 / 严密双共振
- 高位过滤: 60日涨幅>=80% 标高位风险,不包装成买点

用法: python3 structure_scan.py [--dry-run]
cron: 每个交易日 08:30 跑一次(周末脚本内静默)。
推送: 企业微信(@在路上)+息知,走 push.send。
"""
import json
import os
import sys
from datetime import datetime
from zoneinfo import ZoneInfo

BJ = ZoneInfo("Asia/Shanghai")
SVC = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, SVC)
import daily_review as dr  # noqa: E402
from push import send as push_send  # noqa: E402
from channel_structure import diagnose  # noqa: E402

# 用户重点股(常驻): code -> {"cost": 成本(可选)}
KEY_STOCKS = {
    "600127": {"name": "金健米业"},
    "600733": {"name": "北汽蓝谷", "cost": 5.12},
    "301218": {"name": "华是科技"},
}

HIGH_60D = 80.0  # 60日涨幅>=80% 标高位风险
YIDONG_TOPN = 40  # 异动集只取优先级前40进结构扫描
STRAT_SHORT = {
    "通道经典双共振_主升加速型": "经典双共振",
    "极致共振_黄金低吸伏击型": "极致共振",
    "小连阳加速启动": "小连阳启动",
    "严密双共振_量价齐升型": "严密双共振",
}


def to_api(sym):
    s = sym.strip()
    return ("sh" if s[0] in "69" else "sz") + s


def load_pool():
    cands = sorted((f for f in os.listdir(os.path.join(SVC, "reviews"))
                    if f.endswith(".json")), reverse=True)
    for f in cands:
        try:
            d = json.load(open(os.path.join(SVC, "reviews", f)))
            # 2026-09-30修:盘后落盘字段名不统一(triggered/tracking/candidates),
            # 曾导致跟踪通道断流(江淮在candidates里但没被捞进关注池)。三字段合并去重。
            seen, pool = set(), []
            for p in (d.get("triggered") or []) + (d.get("tracking") or []) \
                    + (d.get("candidates") or []):
                c = (p.get("symbol") or p.get("code") or "").strip()
                if c and c not in seen:
                    seen.add(c)
                    pool.append(p)
            if pool:
                return d.get("trade_date", f[:-5]), pool
        except Exception:
            continue
    return None, []


def fetch_daily(sym, limit=250):
    try:
        d = dr.api("/api/v1/quotes/kline?symbol=%s&period=day&limit=%d"
                   % (to_api(sym), limit), timeout=25)["data"]
    except Exception:
        return None
    if not d or len(d) < 60:
        return None
    return d


def analyze(sym, name, cost=None):
    kl = fetch_daily(sym)
    if not kl:
        return None
    import numpy as np
    H = np.array([k["high"] for k in kl], dtype=float)
    L = np.array([k["low"] for k in kl], dtype=float)
    C = np.array([k["close"] for k in kl], dtype=float)
    last = kl[-1]
    r = diagnose(H, L, C, lookback=200)
    f = r["features"]
    pct60 = (C[-1] / C[-61] - 1) * 100 if len(C) > 61 else 0.0
    chg = last.get("change_percent")
    return {
        "symbol": sym, "name": name, "cost": cost,
        "price": last["close"], "change_percent": chg,
        "f": f, "hits": r["hits"], "pct60": pct60,
    }


def load_yidong(top_n=YIDONG_TOPN):
    """加载最新异动集;不是今天的就现场重拉一份。返回 [(code,name,info)]"""
    import glob
    today = datetime.now(BJ).strftime("%Y-%m-%d")
    cands = sorted(glob.glob(os.path.join(SVC, "logs", "yidong_*.json")), reverse=True)
    data = None
    for p in cands:
        try:
            d = json.load(open(p))
            if d.get("date") == today and d.get("stocks"):
                data = d
                break
        except Exception:
            continue
    if data is None:
        try:
            import yidong as yd
            data = yd.build()
            json.dump(data, open(os.path.join(SVC, "logs", "yidong_%s.json" % today), "w"),
                      ensure_ascii=False, indent=1)
        except Exception as e:
            print("yidong build fail:", e)
            return []
    out = []
    for s in (data.get("stocks") or [])[:top_n]:
        out.append((s["code"], s.get("name", ""), s))
    return out


def fmt_stock(a):
    f = a["f"]
    chg = a["change_percent"]
    if chg is None:
        head = "⚪"
    elif chg >= 0:
        head = "🔴"
    else:
        head = "🟢"
    chg_s = ("+%0.2f%%" % chg) if chg and chg >= 0 else ("%0.2f%%" % chg if chg else "--")
    lines = []
    lines.append("%s %s %s %s %.2f (%s)" % (head, a["symbol"], a["name"], "", a["price"], chg_s))
    yi = a.get("yidong")
    if yi:
        # 异动标签:几天几板/人气排名/异动描述
        tags = []
        if yi.get("high_days"):
            tags.append(yi["high_days"])
        if yi.get("hot_rank"):
            tags.append("人气%d" % yi["hot_rank"])
        if yi.get("surge_desc"):
            tags.append(yi["surge_desc"][:30])
        if tags:
            lines.append("  📌 异动: %s" % " | ".join(tags))
    if a["cost"]:
        pnl = (a["price"] / a["cost"] - 1) * 100
        lines.append("  成本 %.2f, 浮亏 %+.1f%%" % (a["cost"], pnl))
    lines.append("  三轨: 上 %.2f | 中 %.2f | 下 %.2f | ch_pos %.1f%%"
                 % (f["ch_upper"], f["ch_mid"], f["ch_lower"], f["ch_pos"]))
    supp_state = "跌破" if f["ch_supp_broken"] else "站稳"
    lines.append("  支撑: %.2f (%+.1f°) %s | 斜率 %+.1f° 方向 %s"
                 % (f["ch_supp_price"], f["ch_supp_slope_deg"], supp_state,
                    f["ch_slope_deg"], {1: "向上", -1: "向下", 0: "走平"}[f["ch_dir"]]))
    high_risk = a["pct60"] >= HIGH_60D
    if high_risk:
        lines.append("  🟡 高位风险: 60日 %+.0f%%, 结构信号作废,不接盘" % a["pct60"])
    hits = [STRAT_SHORT[k] for k, v in a["hits"].items() if v]
    if high_risk:
        pass  # 高位否决,不展示策略命中,避免包装成买点
    elif hits:
        lines.append("  策略命中: ✅ " + " / ".join(hits))
    else:
        lines.append("  策略命中: 无")
    return "\n".join(lines)


def main():
    dry = "--dry-run" in sys.argv
    now = datetime.now(BJ)
    if now.weekday() >= 5 and not dry:
        print("weekend, silent")
        return
    trade_date, pool = load_pool()
    symbols = []  # (code, name, cost, yidong_info)
    seen = set()
    for code, info in KEY_STOCKS.items():
        symbols.append((code, info["name"], info.get("cost"), None))
        seen.add(code)
    for p in pool:
        code = p.get("symbol", "").strip()
        if code and code not in seen:
            symbols.append((code, p.get("name", ""), None, None))
            seen.add(code)
    for code, name, yi in load_yidong():
        if code and code not in seen:
            symbols.append((code, name, None, yi))
            seen.add(code)

    results = []
    for code, name, cost, yi in symbols:
        try:
            a = analyze(code, name, cost)
        except Exception as e:
            print("analyze fail %s: %s" % (code, e))
            continue
        if a:
            a["yidong"] = yi
            results.append(a)

    if not results:
        print("no data")
        return

    date_s = now.strftime("%m-%d")
    title = "盘前结构扫描 %s (%d只)" % (date_s, len(results))
    parts = [title, "先结构,后策略 (ATS v2 通道口径)", ""]
    for a in results:
        parts.append(fmt_stock(a))
        parts.append("")
    content = "\n".join(parts).strip()

    logp = os.path.join(SVC, "logs", "structure_%s.md" % now.strftime("%Y-%m-%d"))
    os.makedirs(os.path.dirname(logp), exist_ok=True)
    open(logp, "w").write(content)

    if dry:
        print(content)
        return
    push_send(title, content)


if __name__ == "__main__":
    main()
