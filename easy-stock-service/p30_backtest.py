#!/usr/bin/env python3
"""P30 回测:weak_rebound(情绪消耗)判定后 10 小时的方向分布。
point-in-time:每步只用该时点之前的已收盘 bar(历史 bar 全已收盘,无 forming 问题)。
结果落盘 logs/p30_backtest_<UTC日期>.json。只读,不写账本不交易。
跑: python3 p30_backtest.py
"""
import datetime
import json
import os
import sys

SVC = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, SVC)
import bars_crypto  # noqa: E402
import cycle_structure  # noqa: E402

COINS = ["BTC", "ETH", "SOL"]
N_BARS = 720
START = 120   # 预热
STEP = 6      # 步长(降重叠)
FWD = 10      # 前瞻小时数


def fwd_ret(bars, t, n):
    c0 = bars[t]["close"]
    return (bars[t + n]["close"] - c0) / c0 if c0 else 0.0


def dist(xs):
    xs = list(xs)
    if not xs:
        return {"n": 0}
    dn = sum(1 for x in xs if x < -0.005)
    fl = sum(1 for x in xs if -0.005 <= x <= 0.005)
    up = sum(1 for x in xs if x > 0.005)
    n = len(xs)
    return {"n": n, "mean": round(sum(xs) / n, 5),
            "p_down": round(dn / n, 3), "p_flat": round(fl / n, 3), "p_up": round(up / n, 3)}


def main():
    out = {"date": datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%d"),
           "strategy_version": cycle_structure.load_cfg().get("_strategy_version", "?"),
           "params": {"n_bars": N_BARS, "step": STEP, "fwd_h": FWD},
           "coins": {}}
    for code in COINS:
        try:
            bars = bars_crypto.intraday_hourly(code, n=N_BARS)
        except Exception as e:
            print(code, "bars fail", str(e)[:60])
            continue
        bars = bars[:-1]  # 剔除 forming 中最后一根,剩余全为已收盘历史 bar
        weak_fwds, base_fwds, rs_hits = [], [], []
        weak_meta = []
        for t in range(START, len(bars) - FWD, STEP):
            a = cycle_structure.analyze(bars[:t + 1])
            f = fwd_ret(bars, t, FWD)
            base_fwds.append(f)
            j = a["judgments"]
            if j.get("weak_rebound"):
                weak_fwds.append(f)
                weak_meta.append({"t": bars[t]["time"], "fwd10": round(f, 5),
                                  "time_ratio": a["time_ratio"],
                                  "retrace_ratio": a["retrace_ratio"]})
            if j.get("regime_switch"):
                rs_hits.append({"t": bars[t]["time"], "fwd10": round(f, 5)})
        out["coins"][code] = {
            "bars": len(bars),
            "weak_rebound_fwd10": dist(weak_fwds),
            "base_fwd10": dist(base_fwds),
            "regime_switch_hits": rs_hits[:20],
            "regime_switch_n": len(rs_hits),
            "weak_samples": weak_meta[:30],
        }
        print("%s bars=%d weak_n=%d weak_mean=%s base_mean=%s rs_n=%d" % (
            code, len(bars), len(weak_fwds),
            dist(weak_fwds).get("mean"), dist(base_fwds).get("mean"), len(rs_hits)))
    p = os.path.join(SVC, "logs", "p30_backtest_%s.json" % out["date"])
    json.dump(out, open(p, "w"), ensure_ascii=False, indent=1)
    print("saved", p)


if __name__ == "__main__":
    main()
