#!/usr/bin/env python3
"""高烈度涨停股回踩爆发力回测(事件研究)。

事件:某股某日涨停且前10个交易日无涨停(首板日),烈度=随后连板数。
口径:
- 回踩:D+1..D+10内首日 low<=MA×1.02(MA用当日及之前收盘价)
- 企稳:触及日后3个交易日收盘均值 > 触及日low×0.98
- 爆发:企稳确认日(触及+3)收盘为基准,后10个交易日最高价涨幅
- 日期均按个股自身K线序列(停牌顺延)
输出: heat_backtest_report.md + heat_backtest_events.csv
"""
import market_cache
import json, os, sys, time, random, urllib.request, urllib.parse
from collections import defaultdict
from statistics import mean, median

SVC = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, SVC)
from trading_calendar import next_trading_day
LOGS = os.path.join(SVC, "logs")

def token():
    for line in open(os.path.join(SVC, ".env")):
        if line.startswith("A_STOCK_TOKEN"):
            return line.strip().split("=", 1)[1]
    raise RuntimeError("no token")

def sym(code):
    return ("sh" if code[0] == "6" else "sz") + code

def resp_key(code):
    return code + (".SH" if code[0] == "6" else ".SZ")

def load_pools():
    """{date: {code: rec}}"""
    pools = {}
    import glob
    for p in sorted(glob.glob(os.path.join(LOGS, "heat_hist_*.json"))):
        d = json.load(open(p))
        pools[d["date"]] = {s["code"]: s for s in d["stocks"]}
    return pools

def trading_days(pools):
    return sorted(pools.keys())

def build_events(pools):
    """事件=(code,D首板日);streak=从D起连续在池天数"""
    days = trading_days(pools)
    day_idx = {d: i for i, d in enumerate(days)}
    events = []
    for D in days:
        if D < "2026-08-03":   # 07-20..07-31仅用于去重叠,不产出事件
            continue
        for code, rec in pools[D].items():
            i = day_idx[D]
            prior = days[max(0, i - 10):i]
            if any(code in pools[p] for p in prior):
                continue
            # streak: D起连续在池
            streak = 1
            for j in range(i + 1, min(i + 6, len(days))):
                if code in pools[days[j]]:
                    streak += 1
                else:
                    break
            events.append({"code": code, "name": rec.get("name", ""),
                           "D": D, "board_D": rec.get("board", 0),
                           "streak": streak,
                           "limit_up_type": rec.get("limit_up_type", ""),
                           "suc_rate": rec.get("suc_rate"),
                           "turnover_rate": rec.get("turnover_rate")})
    return events

def tier_of(ev, high_board=2, mid_suc=0.7):
    if ev["streak"] >= high_board:
        return "高"
    sr = ev["suc_rate"]
    if (ev["limit_up_type"] == "换手板") or (sr is not None and sr > mid_suc):
        return "中"
    return "低"

def fetch_klines(codes, limit=100):
    """{code: [(date,o,h,l,c),...]} 按日期升序;缓存增量"""
    cache_p = os.path.join(LOGS, "heat_klines.json")
    cache = json.load(open(cache_p)) if os.path.exists(cache_p) else {}
    tk = token()
    todo = [c for c in codes if c not in cache]
    for i in range(0, len(todo), 30):
        batch = todo[i:i + 30]
        url = ("http://127.0.0.1:20081/api/v1/quotes/kline/batch?symbols=%s&period=day&limit=%d&token=%s"
               % (",".join(sym(c) for c in batch), limit, tk))
        try:
            d = json.load(market_cache.urlopen(url, timeout=120))
        except Exception as e:
            print("batch fail:", str(e)[:80], flush=True)
            d = {"data": {}}
        data = d.get("data", {})
        retry_codes = []
        for c in batch:
            rows = data.get(resp_key(c), []) or []
            bars = []
            for r in rows:
                try:
                    dt = r["time"][:10]
                    bars.append((dt, r["open"], r["high"], r["low"], r["close"]))
                except Exception:
                    continue
            bars.sort()
            if len(bars) < 25:
                retry_codes.append(c)  # 空/过短:稍后单抓重试,不进缓存
            else:
                cache[c] = bars
        for c in retry_codes:  # 单只重试一次
            try:
                u2 = ("http://127.0.0.1:20081/api/v1/quotes/kline?symbol=%s&period=day&limit=%d&token=%s"
                      % (sym(c), limit, tk))
                d2 = json.load(market_cache.urlopen(u2, timeout=60))
                dd = d2.get("data", [])
                rows2 = dd.get("klines", []) if isinstance(dd, dict) else dd
                bars2 = []
                for r in (rows2 if isinstance(rows2, list) else []):
                    try:
                        bars2.append((r["time"][:10], r["open"], r["high"], r["low"], r["close"]))
                    except Exception:
                        continue
                bars2.sort()
                if len(bars2) >= 25:
                    cache[c] = bars2
                    print("  retry ok", c, len(bars2), flush=True)
                else:
                    print("  retry short", c, len(bars2), flush=True)
            except Exception as e:
                print("  retry fail", c, str(e)[:60], flush=True)
            time.sleep(0.3)
        time.sleep(0.2)
        print("klines %d/%d" % (min(i + 30, len(todo)), len(todo)), flush=True)
    json.dump(cache, open(cache_p, "w"))
    return cache

def ma(closes, n):
    return sum(closes[-n:]) / n if len(closes) >= n else None

def analyze_event(ev, bars, ma_n=20, tol=1.02):
    """返回 dict: pullback/touch_day/stabilized/burst/win/notes"""
    dates = [b[0] for b in bars]
    try:
        di = dates.index(ev["D"])
    except ValueError:
        return {"skip": "D无K线"}
    closes = [b[4] for b in bars]
    out = {"skip": None}
    # 回踩: D后1..10根bar首日 low<=MA×1.02
    touch = None
    for k in range(1, 11):
        j = di + k
        if j >= len(bars):
            break
        m = ma(closes[:j + 1], ma_n)
        if m is None:
            continue
        if bars[j][3] <= m * tol:
            touch = j
            out["touch_day"] = bars[j][0]
            out["touch_low"] = bars[j][3]
            out["ma_val"] = round(m, 3)
            break
    out["pullback"] = touch is not None
    if touch is None:
        return out
    # 企稳: 后3根收盘均值 > touch_low×0.98
    if touch + 3 >= len(bars):
        out["stabilized"] = None
        return out
    avg3 = mean(b[4] for b in bars[touch + 1:touch + 4])
    out["stabilized"] = avg3 > bars[touch][3] * 0.98
    out["confirm_day"] = bars[touch + 3][0]
    out["confirm_close"] = bars[touch + 3][4]
    # 爆发: 确认日后10根最高high相对确认收盘
    fwd = bars[touch + 4:touch + 14]
    if len(fwd) < 10:
        out["burst"] = None
        return out
    mx = max(b[2] for b in fwd)
    out["burst"] = round(mx / bars[touch + 3][4] - 1, 4)
    out["win"] = out["burst"] > 0.10
    return out

def summarize(rows):
    n = len(rows)
    pb = [r for r in rows if r.get("pullback")]
    st = [r for r in rows if r.get("stabilized") is True]
    bu = [r["burst"] for r in rows if r.get("burst") is not None]
    return {
        "n": n,
        "pullback_n": len(pb),
        "pullback_rate": round(len(pb) / n, 4) if n else None,
        "stabilize_n": len(st),
        "stabilize_rate": round(len(st) / len(pb), 4) if pb else None,
        "burst_n": len(bu),
        "burst_mean": round(mean(bu), 4) if bu else None,
        "burst_median": round(median(bu), 4) if bu else None,
        "win_rate": round(sum(1 for b in bu if b > 0.10) / len(bu), 4) if bu else None,
    }

def main():
    pools = load_pools()
    print("pool days:", len(pools))
    events = build_events(pools)
    print("events:", len(events))
    codes = sorted({e["code"] for e in events})
    kl = fetch_klines(codes)

    # 主口径: 高=连板≥2, 中=首板&(换手板|suc>0.7), 回踩MA20×1.02
    rows = []
    for ev in events:
        bars = kl.get(ev["code"]) or []
        if len(bars) < 25:
            continue
        r = analyze_event(ev, bars)
        r.update({"code": ev["code"], "name": ev["name"], "D": ev["D"],
                  "streak": ev["streak"], "tier": tier_of(ev),
                  "limit_up_type": ev["limit_up_type"], "suc_rate": ev["suc_rate"]})
        rows.append(r)
    print("analyzed:", len(rows))

    tiers = defaultdict(list)
    for r in rows:
        tiers[r["tier"]].append(r)
    for t in ["高", "中", "低"]:
        print(t, summarize(tiers[t]))

    # 对照组: 200只同期无涨停随机股, 伪D从事件日期分布抽样
    import glob as _g
    dresp = json.load(market_cache.urlopen(
        "http://127.0.0.1:20081/api/v1/stocks/directory?token=" + token(), timeout=60))
    allc = [s["code"] for s in dresp["data"]["stocks"]
            if len(s["code"]) == 6 and s["code"][0] in "6030"]
    pool_codes = set()
    for mp in pools.values():
        pool_codes.update(mp.keys())
    cand = [c for c in allc if c not in pool_codes]
    random.seed(42)
    ctrl_codes = random.sample(cand, 200)
    kl2 = fetch_klines(ctrl_codes)
    ev_dates = [e["D"] for e in events]
    ctrl_rows = []
    for c in ctrl_codes:
        bars = kl2.get(c) or []
        if len(bars) < 30:
            continue
        D = random.choice(ev_dates)
        dates = [b[0] for b in bars]
        if D not in dates:
            continue
        r = analyze_event({"code": c, "D": D}, bars)
        r.update({"code": c, "D": D, "tier": "对照"})
        ctrl_rows.append(r)
    print("对照", summarize(ctrl_rows))

    # 共振参数扫描
    scan = []
    for hb in [2, 3]:
        for ma_n in [20, 10]:
            for ms in [0.7, 0.8]:
                rr = []
                for ev in events:
                    if tier_of(ev, high_board=hb, mid_suc=ms) != "高":
                        continue
                    bars = kl.get(ev["code"]) or []
                    if len(bars) < 25:
                        continue
                    r = analyze_event(ev, bars, ma_n=ma_n)
                    rr.append(r)
                s = summarize(rr)
                s.update({"high_board": hb, "ma": ma_n, "mid_suc": ms})
                scan.append(s)
                print(s)
    scan.sort(key=lambda s: (s["burst_mean"] is not None, s["burst_mean"] or -9),
              reverse=True)

    # 落盘CSV
    import csv
    cp = os.path.join(SVC, "heat_backtest_events.csv")
    cols = ["code", "name", "D", "streak", "tier", "limit_up_type", "suc_rate",
            "pullback", "touch_day", "touch_low", "ma_val", "stabilized",
            "confirm_day", "confirm_close", "burst", "win", "skip"]
    with open(cp, "w", newline="") as f:
        w = csv.DictWriter(f, fieldnames=cols, extrasaction="ignore")
        w.writeheader()
        for r in rows + ctrl_rows:
            w.writerow(r)
    json.dump({"tiers": {t: summarize(tiers[t]) for t in tiers},
               "control": summarize(ctrl_rows), "scan": scan,
               "n_events": len(events), "n_rows": len(rows)},
              open(os.path.join(SVC, "heat_backtest_stats.json"), "w"),
              ensure_ascii=False, indent=1)
    print("saved csv+stats")

if __name__ == "__main__":
    main()
