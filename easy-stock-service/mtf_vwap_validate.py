#!/usr/bin/env python3
"""多周期 VWAP 趋势结构分形有效性验证(只读分析)。

假设:P25 式 VWAP 多头/空头排列结构在小时/日/周级别自相似(分形)。
方法:各周期同口径信号 —— 1/3/5 单位 VWAP 多头排列(v1>v3>v5)且收盘站上
该周期 MA20 为多头,镜像为空头;全部 point-in-time(信号只用信号时点
已收盘的 bar);统计信号后未来 5 个周期单位的收益率。
VWAP 定义与 vwap.py / crypto_pool._p25_ok 一致:Σamount/Σvolume,amount=close×volume。

约束:只读 bars_crypto,不写账本、不改策略、不推送、不新增上游依赖。
"""
import datetime
import json
import os
import statistics
import sys
import time

SVC = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, SVC)
import bars_crypto  # noqa: E402

COINS = ["BTC", "ETH", "SOL"]
FWD_N = 5          # 信号后向前看 5 个周期单位
MIN_HIST = 20      # 信号需要 20 根历史 bar(MA20)


def _vwap(seg):
    amt = sum(b["amount"] for b in seg)
    vol = sum(b["volume"] for b in seg)
    return amt / vol if vol else None


def load_hourly(code):
    """Kraken 60分钟K,去掉 forming 中的最后一根。返回升序 dict 列表。"""
    now = time.time()
    rows = bars_crypto.kraken_ohlc(code, 60)
    out = []
    for ts, o, h, l, cl, vol in rows:
        if ts + 3600 > now:
            continue  # forming 中,未收盘
        out.append({"ts": ts, "close": cl, "volume": vol, "amount": cl * vol})
    return out


def load_daily(code):
    rows = bars_crypto.get_bars(code)
    return [{"date": r["date"], "close": r["close"],
             "volume": r["volume"], "amount": r["amount"]} for r in rows]


def load_weekly(code):
    """日K按 ISO 自然周(UTC)聚合;只保留满 7 天的完整周(去掉首尾残周)。"""
    daily = load_daily(code)
    weeks, cur, cur_key = [], [], None
    for r in daily:
        d = datetime.date.fromisoformat(r["date"])
        key = (d.isocalendar().year, d.isocalendar().week)
        if key != cur_key:
            if cur:
                weeks.append((cur_key, cur))
            cur, cur_key = [], key
        cur.append(r)
    if cur:
        weeks.append((cur_key, cur))
    out = []
    for key, rs in weeks:
        if len(rs) != 7:
            continue  # 残周=forming 中,丢弃
        out.append({
            "date": rs[-1]["date"],
            "close": rs[-1]["close"],
            "volume": sum(r["volume"] for r in rs),
            "amount": sum(r["amount"] for r in rs),
        })
    return out


def evaluate(bars):
    """逐 bar point-in-time 信号;返回 {'long':[fwd...], 'short':[fwd...]}。
    信号在 bar i 收盘时已知;fwd 用 i+5 收盘价衡量。"""
    longs, shorts = [], []
    n = len(bars)
    for i in range(MIN_HIST - 1, n - FWD_N):
        hist = bars[:i + 1]
        v1 = _vwap(hist[-1:])
        v3 = _vwap(hist[-3:])
        v5 = _vwap(hist[-5:])
        ma20 = statistics.fmean(b["close"] for b in hist[-20:])
        c0 = hist[-1]["close"]
        c5 = bars[i + FWD_N]["close"]
        if v1 and v3 and v5:
            if v1 > v3 > v5 and c0 >= ma20:
                longs.append(c5 / c0 - 1)
            elif v1 < v3 < v5 and c0 < ma20:
                shorts.append(c0 / c5 - 1)
    return {"long": longs, "short": shorts}


def stats(xs):
    if not xs:
        return {"n": 0, "win_rate": None, "mean": None, "mean_abs": None}
    wins = sum(1 for x in xs if x > 0)
    return {"n": len(xs),
            "win_rate": round(wins / len(xs), 4),
            "mean": round(statistics.fmean(xs), 5),
            "mean_abs": round(statistics.fmean(abs(x) for x in xs), 5)}


def main():
    today_utc = datetime.datetime.now(datetime.timezone.utc).date().isoformat()
    result = {"date": today_utc, "fwd_n": FWD_N, "min_hist": MIN_HIST,
              "note": "point-in-time:信号只用信号时点已收盘bar;forming bar已剔除",
              "coins": {}}
    for ci, code in enumerate(COINS):
        if ci:
            time.sleep(2)  # Kraken 限频,温柔一点
        tf = {}
        try:
            tf["hourly"] = {"nbars": len(load_hourly(code))}
            tf["hourly"].update({k: stats(v) for k, v in
                                 evaluate(load_hourly(code)).items()})
        except Exception as e:
            tf["hourly"] = {"error": str(e)[:120]}
        try:
            db = load_daily(code)
            tf["daily"] = {"nbars": len(db)}
            tf["daily"].update({k: stats(v) for k, v in evaluate(db).items()})
        except Exception as e:
            tf["daily"] = {"error": str(e)[:120]}
        try:
            wb = load_weekly(code)
            tf["weekly"] = {"nbars": len(wb)}
            tf["weekly"].update({k: stats(v) for k, v in evaluate(wb).items()})
        except Exception as e:
            tf["weekly"] = {"error": str(e)[:120]}
        result["coins"][code] = tf

    # 打印结果表
    print("=" * 86)
    print("VWAP 趋势结构多周期分形验证  date=%s  fwd=%d周期单位  (收益率=小数)" % (today_utc, FWD_N))
    print("=" * 86)
    hdr = "%-5s %-7s %-6s %6s %8s %9s %9s" % ("币种", "周期", "方向", "信号数", "胜率", "平均收益", "平均绝对")
    print(hdr)
    print("-" * 86)
    for code in COINS:
        for tfname in ("hourly", "daily", "weekly"):
            tf = result["coins"][code][tfname]
            if "error" in tf:
                print("%-5s %-7s ERROR %s" % (code, tfname, tf["error"]))
                continue
            for side in ("long", "short"):
                s = tf[side]
                wr = ("%.1f%%" % (s["win_rate"] * 100)) if s["win_rate"] is not None else "-"
                mn = ("%.2f%%" % (s["mean"] * 100)) if s["mean"] is not None else "-"
                ma = ("%.2f%%" % (s["mean_abs"] * 100)) if s["mean_abs"] is not None else "-"
                print("%-5s %-7s %-6s %6d %8s %9s %9s  (bar数=%d)" %
                      (code, tfname, side, s["n"], wr, mn, ma, tf["nbars"]))
    print("=" * 86)

    path = os.path.join(SVC, "logs", "mtf_vwap_%s.json" % today_utc)
    json.dump(result, open(path, "w"))
    print("saved", path)
    return result


if __name__ == "__main__":
    main()
