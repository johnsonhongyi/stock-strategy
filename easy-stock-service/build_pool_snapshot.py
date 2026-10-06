#!/usr/bin/env python3
"""用收盘后最终日K重算回踩企稳跟踪池,落盘 reviews/<date>.json。
给 intraday_scan.py 提供盘中跟踪的 watchlist。不写 .md 报告,不推送。
用法: python3 build_pool_snapshot.py [SYM1,SYM2,...] [--date YYYY-MM-DD]
"""
import json
import os
import sys
from datetime import datetime
from zoneinfo import ZoneInfo

SVC = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, SVC)
import daily_review as dr  # noqa: E402

BJ = ZoneInfo("Asia/Shanghai")

# 2026-09-28 晚 dry-run 的实测池(13只),默认用它重算
DEFAULT_POOL = ("000980,600733,002119,001201,000592,002413,603773,"
                "600206,688163,600172,300300,002614,600127")


def to_api(sym):
    s = dr.norm_sym(sym)
    return ("sh" if s[0] in "69" else "sz") + s


def main():
    argv = sys.argv[1:]
    date_arg = None
    rest = []
    i = 0
    while i < len(argv):
        a = argv[i]
        if a == "--date" and i + 1 < len(argv):
            date_arg = argv[i + 1]
            i += 2
        elif a.startswith("--date="):
            date_arg = a.split("=", 1)[1]
            i += 1
        else:
            rest.append(a)
            i += 1
    today = date_arg or datetime.now(BJ).strftime("%Y-%m-%d")
    symbols = dr.norm_sym(rest[0]).split(",") if rest else DEFAULT_POOL.split(",")
    symbols = [s for s in (dr.norm_sym(x) for x in symbols) if s]
    api_syms = [to_api(s) for s in symbols]

    klines = {}
    for i in range(0, len(api_syms), 15):
        batch = api_syms[i:i + 15]
        try:
            data = dr.api("/api/v1/quotes/kline/batch?symbols=%s&period=day&limit=25"
                          % ",".join(batch), timeout=60)["data"]
            for sym, bars in (data or {}).items():
                code = dr.norm_sym(sym)
                if code and bars:
                    klines[code] = bars
        except Exception as e:
            print("batch fail: %s" % e, flush=True)

    # 名称补全
    name_map = {}
    try:
        for s in dr.api("/api/v1/stocks/directory")["data"]["stocks"]:
            name_map[dr.norm_sym(s.get("symbol"))] = s.get("name", "")
    except Exception:
        pass

    triggered, tracking = [], []
    for code in symbols:
        bars = klines.get(code)
        if not bars:
            print("no kline: %s" % code, flush=True)
            continue
        st = dr.analyze_daily(bars)
        if not st:
            continue
        pb = dr.analyze_pullback(bars, st)
        if pb and not dr.is_st(name_map.get(code, "")):
            item = {"symbol": code, "name": name_map.get(code, ""), **pb}
            (triggered if pb["stage"] == "triggered" else tracking).append(item)

    trig_rank = {"刚站上1日线": 0, "回踩1日线": 1, "整理末端": 2}
    triggered.sort(key=lambda p: (min(trig_rank.get(t, 9)
                                      for t in p["trigger"].split("、")),
                                   abs(p.get("dist_vwap1") or 99)))
    tracking.sort(key=lambda p: abs(p.get("dist_vwap1") or 99))

    out = {"trade_date": today, "triggered": triggered, "tracking": tracking,
           "triggered_count": len(triggered), "tracking_count": len(tracking)}
    path = os.path.join(SVC, "reviews", "%s.json" % today)
    with open(path, "w") as f:
        json.dump(out, f, ensure_ascii=False)
    print("wrote %s: triggered=%d tracking=%d" %
          (path, len(triggered), len(tracking)))
    for p in triggered:
        print("  TRIGGERED %s %s %s" % (p["symbol"], p["name"], p["trigger"]))
    for p in tracking[:5]:
        print("  tracking %s %s" % (p["symbol"], p["name"]))


if __name__ == "__main__":
    main()
