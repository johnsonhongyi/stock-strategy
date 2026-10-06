#!/usr/bin/env python3
"""美股关注池:与 A 股 watchpool 同一套 enrichment + 同一个 score() 打分函数。
数据源:us_watchlist.json + bars_us 本地日K(不碰上游)。
输出 logs/us_pool_<date>.json,结构与 watchpool_*.json 对齐
({pool:[{symbol,name,price,change_percent,f,hits,score,score_why,...}]}),
供 paper_trade --market US 统一信号链路使用。
"""
import json
import os
import sys

SVC = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, SVC)
import numpy as np  # noqa: E402
import bars_us  # noqa: E402
from channel_structure import diagnose  # noqa: E402
from watchpool import score as pool_score  # noqa: E402,同一套打分

NAMES = {
    "AAPL": "苹果", "NVDA": "英伟达", "MSFT": "微软", "TSLA": "特斯拉",
    "META": "Meta", "AMZN": "亚马逊", "GOOGL": "谷歌",
}


def enrich(sym, heat=None):
    bars = bars_us.get_bars(sym)
    if not bars or len(bars) < 65:
        return None
    H = np.array([b["high"] for b in bars], dtype=float)
    L = np.array([b["low"] for b in bars], dtype=float)
    C = np.array([b["close"] for b in bars], dtype=float)
    V = np.array([b["volume"] for b in bars], dtype=float)  # 美股单位:股
    A = np.array([b["amount"] or 0 for b in bars], dtype=float)  # 单位:美元
    last = bars[-1]
    r = diagnose(H, L, C, lookback=200)
    f = r["features"]
    pct60 = (C[-1] / C[-61] - 1) * 100 if len(C) > 61 else 0.0
    base = V[-6:-1].mean()
    vol_ratio = float(V[-1] / base) if base > 0 else 0.0
    vwap1 = float(A[-1] / V[-1]) if V[-1] > 0 else 0.0  # 美元/股
    dist_vwap = (last["close"] / vwap1 - 1) * 100 if vwap1 > 0 else 0.0
    prev = last.get("prev_close") or (C[-2] if len(C) > 1 else 0)
    chg = (last["close"] / prev - 1) * 100 if prev else 0.0
    a = {
        "symbol": sym, "name": (heat.get("name") if heat else None) or NAMES.get(sym, sym),
        "price": last["close"], "change_percent": round(chg, 2),
        "f": f, "hits": r["hits"], "pct60": round(pct60, 1),
        "vol_ratio": round(vol_ratio, 2), "vwap1": round(vwap1, 2),
        "dist_vwap": round(dist_vwap, 2),
        # 热度异动分:复用 watchpool.score 同一套(连板概念美股无,走 hot_rank/多源加成)
        "yidong": ({"hot_rank": heat["heat_rank"],
                    "sources": ["异动"] + heat["sources"],
                    "heat_score": heat["heat_score"]} if heat else None),
        "plate": "", "src": "us_heat" if heat else "us_watchlist",
    }
    pool_score(a)  # 与 A 股同一套打分
    return a


def build(date_s=None):
    from trading_calendar import today_str
    date_s = date_s or today_str("US")
    syms = json.load(open(os.path.join(SVC, "us_watchlist.json")))["symbols"]
    # 热度迭代:us_heat 当日 Top5(不在观察池里的)进入候选宇宙,新票自动打底一次
    import us_heat
    heat = us_heat.load(date_s)
    heat_map = {h["symbol"]: h for h in heat.get("symbols", [])}
    extra = [h["symbol"] for h in heat.get("symbols", [])[:5]
             if h["symbol"] not in syms]
    for s in extra:
        try:
            n = bars_us.backfill_one(s)
            if n:
                print("%s heat新标的打底 bars=%d" % (s, n))
        except Exception as e:
            print("%s backfill FAIL %s" % (s, str(e)[:60]))
    pool = []
    for s in syms + extra:
        try:
            a = enrich(s, heat_map.get(s))
        except Exception as e:
            print("%s enrich FAIL %s" % (s, str(e)[:80]))
            continue
        if a:
            pool.append(a)
    pool.sort(key=lambda x: x["score"], reverse=True)
    out = {"date": date_s, "market": "US", "pool": pool,
           "heat_extra": extra}
    p = os.path.join(SVC, "logs", "us_pool_%s.json" % date_s)
    json.dump(out, open(p, "w"), ensure_ascii=False, indent=1, default=str)
    for a in pool:
        print("%-6s %-6s %8.2f %+.2f%% score=%.1f %s %s" % (
            a["symbol"], a["name"][:6], a["price"], a["change_percent"],
            a["score"], ",".join(a["score_why"]), a["src"]))
    return out


if __name__ == "__main__":
    build()
