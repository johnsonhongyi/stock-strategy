#!/usr/bin/env python3
"""模拟仓样本漏斗每日计数 (2026-10-01 用户确认)。

10-08 开盘起每天收盘后跑: 入池 -> P24触发 -> 买入 -> P23退出,
累计数推企业微信;周一(复盘前)附周汇总。
用法: python3 funnel_count.py [--date YYYY-MM-DD] [--no-push]
非交易日静默退出。
"""
import json
import os
import sys
import datetime

BASE = os.path.dirname(os.path.abspath(__file__))
LOGS = os.path.join(BASE, "logs")
sys.path.insert(0, BASE)

from trading_calendar import is_trading_day  # noqa: E402
import push  # noqa: E402

START = "2026-10-08"  # 样本积累起点


def load_json(path, default=None):
    try:
        with open(path, encoding="utf-8") as f:
            return json.load(f)
    except Exception:
        return default


def daterange(a, b):
    d0 = datetime.date.fromisoformat(a)
    d1 = datetime.date.fromisoformat(b)
    d = d0
    while d <= d1:
        yield d.isoformat()
        d += datetime.timedelta(days=1)


def day_stats(date):
    """单日漏斗: 入池 / P24触发 / 买入 / P23退出。"""
    wp = load_json(os.path.join(LOGS, f"watchpool_{date}.json"), {})
    pool_n = (wp.get("pool_n") if isinstance(wp, dict) else 0) or 0
    if not pool_n and isinstance(wp, dict):
        pool_n = len(wp.get("pool", []) or [])

    dec = load_json(os.path.join(LOGS, f"decisions_{date}.json"), {})
    decs = dec.get("decisions", []) if isinstance(dec, dict) else []
    decs = [d for d in decs if isinstance(d, dict)]
    buys = [d for d in decs if d.get("action") in ("BUY", "FILL")]
    p24_buys = [
        d for d in buys
        if "P24" in str(d.get("why", "")) or "大阳" in str(d.get("why", ""))
    ]
    return {"pool": pool_n, "p24": len(p24_buys), "buys": len(buys)}


def p23_exits(date, ledger):
    closed = ledger.get("closed", []) or []
    return [
        c for c in closed
        if c.get("sell_date") == date
        and ("P23" in str(c.get("reason", "")) or "不及预期" in str(c.get("reason", "")))
    ]


def cumulative(ledger, today):
    """自 START 起累计: 买入笔数 / P23退出 / 当前持仓。"""
    cum_buys = 0
    for d in daterange(START, today):
        dec = load_json(os.path.join(LOGS, f"decisions_{d}.json"), {})
        decs = dec.get("decisions", []) if isinstance(dec, dict) else []
        cum_buys += sum(
            1 for x in decs
            if isinstance(x, dict) and x.get("action") in ("BUY", "FILL")
        )
    closed = ledger.get("closed", []) or []
    cum_p23 = sum(
        1 for c in closed
        if (c.get("sell_date") or "") >= START
        and ("P23" in str(c.get("reason", "")) or "不及预期" in str(c.get("reason", "")))
    )
    return {
        "cum_buys": cum_buys,
        "cum_p23": cum_p23,
        "open_pos": len(ledger.get("positions", []) or []),
        "equity": ledger.get("equity"),
    }


def fmt(date, day, cum, ledger, weekly=None):
    lines = [
        f"📊 模拟盘样本漏斗 {date}",
        "",
        f"入池 {day['pool']} 只 → P24触发 {day['p24']} 只 → 买入 {day['buys']} 笔 → P23退出 {day['p23']} 笔",
        "",
        f"累计(自10-08): 买入 {cum['cum_buys']} 笔 / P23退出 {cum['cum_p23']} 笔 / 持仓 {cum['open_pos']} 只",
    ]
    if cum.get("equity") is not None:
        lines.append(f"账本净值 {cum['equity']:.0f}")
    if weekly:
        w = weekly["week"]
        lines += [
            "",
            f"—— 本周汇总({weekly['span']}) ——",
            f"入池 {w['pool']} 只 / P24触发 {w['p24']} 只 / 买入 {w['buys']} 笔 / P23退出 {w['p23']} 笔",
        ]
    return "\n".join(lines)


def main():
    args = sys.argv[1:]
    date = None
    no_push = False
    for a in args:
        if a.startswith("--date="):
            date = a.split("=", 1)[1]
        elif a == "--no-push":
            no_push = True
    if not date:
        date = datetime.date.today().isoformat()
    if not is_trading_day(date):
        print(json.dumps({"skip": True, "reason": "not_trading_day", "date": date},
                         ensure_ascii=False))
        return 0

    ledger = load_json(os.path.join(LOGS, "paper_ledger.json"), {}) or {}
    day = day_stats(date)
    day["p23"] = len(p23_exits(date, ledger))
    cum = cumulative(ledger, date)

    weekly = None
    wd = datetime.date.fromisoformat(date).weekday()  # 0=周一
    if wd == 0:
        # 周一:汇总上周一以来(含今日)的漏斗,供20:00周复盘
        monday = datetime.date.fromisoformat(date) - datetime.timedelta(days=7)
        w = {"pool": 0, "p24": 0, "buys": 0, "p23": 0}
        for d in daterange(monday.isoformat(), date):
            if not is_trading_day(d):
                continue
            s = day_stats(d)
            w["pool"] += s["pool"]
            w["p24"] += s["p24"]
            w["buys"] += s["buys"]
            w["p23"] += len(p23_exits(d, ledger))
        weekly = {"span": f"{monday.isoformat()}~{date}", "week": w}

    content = fmt(date, day, cum, ledger, weekly)
    out = {"date": date, "day": day, "cumulative": cum,
           "weekly": weekly["week"] if weekly else None}
    with open(os.path.join(LOGS, f"funnel_{date}.json"), "w",
              encoding="utf-8") as f:
        json.dump(out, f, ensure_ascii=False, indent=2)

    if not no_push:
        ok, msg = push.send(f"📊 模拟盘样本漏斗 {date}", content)
        out["pushed"] = {"ok": ok, "msg": msg}
    print(json.dumps(out, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    sys.exit(main())
