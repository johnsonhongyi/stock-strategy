#!/usr/bin/env python3
"""第二轮:小时级 VWAP 结构 + 过滤器/择时,找扣费后真 edge(只读分析)。

背景:第一轮(mtf_vwap_validate.py)结论——小时级裸"1/3/5小时VWAP排列+MA20"
信号 5 小时前瞻≈抛硬币,扣双边 0.1% 手续费后为负。
本轮测试过滤/择时变体(多空镜像),全部 point-in-time(只用已收盘 bar),
统计扣费后净均值。

变体:
  base        信号收盘直接入场
  vol         + 量能过滤(信号小时量 > 20h 均值×1.5)
  dev         + 偏离过滤(|现价-5hVWAP|/5hVWAP < 2%,不追伸太远)
  vol+dev     两者叠加
  p8          P8式择时:信号后5小时内出现回踩5hVWAP(±0.5%)才入场,否则放弃
  p8+vol+dev  择时+双过滤
前瞻窗口:3/5/10 小时。手续费:双边各 0.1%,净收益=gross-0.002。

约束:只读 bars_crypto,不写账本、不改策略、不推送。
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
FEE_RT = 0.002          # 双边各 0.1%
MIN_HIST = 20
FWDS = [3, 5, 10]
MAX_FWD = max(FWDS)
P8_WINDOW = 5

VARIANTS = [
    ("base", {}),
    ("vol", {"vol": True}),
    ("dev", {"dev": True}),
    ("vol+dev", {"vol": True, "dev": True}),
    ("p8", {"p8": True}),
    ("p8+vol+dev", {"p8": True, "vol": True, "dev": True}),
]


def _vwap(seg):
    amt = sum(b["amount"] for b in seg)
    vol = sum(b["volume"] for b in seg)
    return amt / vol if vol else None


def load_hourly(code):
    now = time.time()
    rows = bars_crypto.kraken_ohlc(code, 60)
    out = []
    for ts, o, h, l, cl, vol in rows:
        if ts + 3600 > now:
            continue  # forming 中,未收盘
        out.append({"ts": ts, "close": cl, "volume": vol, "amount": cl * vol})
    return out


def gen_trades(bars, side, filt, fwd):
    """返回 (净收益列表, 信号数)。全部 point-in-time。"""
    trades = []
    n_sig = 0
    n = len(bars)
    for i in range(MIN_HIST - 1, n - P8_WINDOW - MAX_FWD):
        hist = bars[:i + 1]
        v1 = _vwap(hist[-1:])
        v3 = _vwap(hist[-3:])
        v5 = _vwap(hist[-5:])
        if not (v1 and v3 and v5):
            continue
        ma20 = statistics.fmean(b["close"] for b in hist[-20:])
        c0 = hist[-1]["close"]
        if side == "long":
            sig = v1 > v3 > v5 and c0 >= ma20
        else:
            sig = v1 < v3 < v5 and c0 < ma20
        if not sig:
            continue
        n_sig += 1
        if filt.get("vol"):
            vol20 = statistics.fmean(b["volume"] for b in hist[-20:])
            if hist[-1]["volume"] <= vol20 * 1.5:
                continue
        if filt.get("dev"):
            if abs(c0 - v5) / v5 >= 0.02:
                continue
        if filt.get("p8"):
            entry = None
            for j in range(1, P8_WINDOW + 1):
                hj = bars[:i + j + 1]
                v5j = _vwap(hj[-5:])
                cj = hj[-1]["close"]
                if v5j and abs(cj - v5j) / v5j <= 0.005:
                    entry = (i + j, cj)
                    break
            if entry is None:
                continue  # 5 小时内无回踩,放弃
            ei, ep = entry
        else:
            ei, ep = i, c0
        ce = bars[ei + fwd]["close"]
        gross = ce / ep - 1 if side == "long" else ep / ce - 1
        trades.append(gross - FEE_RT)
    return trades, n_sig


def stats(xs):
    if not xs:
        return {"n": 0, "win_rate": None, "net_mean": None}
    wins = sum(1 for x in xs if x > 0)
    return {"n": len(xs),
            "win_rate": round(wins / len(xs), 4),
            "net_mean": round(statistics.fmean(xs), 5)}


def main():
    today_utc = datetime.datetime.now(datetime.timezone.utc).date().isoformat()
    result = {"date": today_utc, "fee_roundtrip": FEE_RT, "fwds": FWDS,
              "note": "point-in-time,只用已收盘bar;净收益=毛收益-0.002",
              "coins": {}}
    for ci, code in enumerate(COINS):
        if ci:
            time.sleep(2)
        bars = load_hourly(code)
        coin_r = {"nbars": len(bars), "variants": {}}
        for vname, filt in VARIANTS:
            v_r = {}
            for fwd in FWDS:
                f_r = {}
                for side in ("long", "short"):
                    tr, n_sig = gen_trades(bars, side, filt, fwd)
                    s = stats(tr)
                    s["n_signals"] = n_sig
                    f_r[side] = s
                v_r["fwd%d" % fwd] = f_r
            coin_r["variants"][vname] = v_r
        result["coins"][code] = coin_r

    # 主表:前瞻 5h
    print("=" * 92)
    print("第二轮:小时级VWAP+过滤器  date=%s  前瞻=5h  净收益=毛-0.2%%手续费" % today_utc)
    print("=" * 92)
    print("%-10s %-5s %-6s %6s %8s %9s %9s" %
          ("变体", "币种", "方向", "交易数", "胜率", "净均值", "信号数"))
    print("-" * 92)
    for vname, _ in VARIANTS:
        for code in COINS:
            for side in ("long", "short"):
                s = result["coins"][code]["variants"][vname]["fwd5"][side]
                wr = ("%.1f%%" % (s["win_rate"] * 100)) if s["win_rate"] is not None else "-"
                nm = ("%.3f%%" % (s["net_mean"] * 100)) if s["net_mean"] is not None else "-"
                flag = " *" if s["n"] < 30 else ""
                print("%-10s %-5s %-6s %6d %8s %9s %9d%s" %
                      (vname, code, side, s["n"], wr, nm, s["n_signals"], flag))
    print("* = 样本<30,不可靠")
    print("=" * 92)

    # 全 fwd 最佳组合(n>=30)
    best = []
    for vname, _ in VARIANTS:
        for code in COINS:
            for fwd in FWDS:
                for side in ("long", "short"):
                    s = result["coins"][code]["variants"][vname]["fwd%d" % fwd][side]
                    if s["n"] >= 30 and s["net_mean"] is not None:
                        best.append((s["net_mean"], vname, code, fwd, side, s["n"], s["win_rate"]))
    best.sort(reverse=True)
    print("扣费后净均值 Top10 (n>=30):")
    for nm, vname, code, fwd, side, n, wr in best[:10]:
        print("  %+.3f%%  %s %s fwd%dh %s  n=%d 胜率=%.1f%%" %
              (nm * 100, vname, code, fwd, side, n, wr * 100))
    print("扣费后净均值 Bottom5 (n>=30):")
    for nm, vname, code, fwd, side, n, wr in best[-5:]:
        print("  %+.3f%%  %s %s fwd%dh %s  n=%d 胜率=%.1f%%" %
              (nm * 100, vname, code, fwd, side, n, wr * 100))
    result["top10_net"] = [
        {"net_mean": nm, "variant": v, "coin": c, "fwd": f, "side": s, "n": n, "win_rate": wr}
        for nm, v, c, f, s, n, wr in best[:10]]

    path = os.path.join(SVC, "logs", "mtf_vwap2_%s.json" % today_utc)
    json.dump(result, open(path, "w"))
    print("saved", path)
    return result


if __name__ == "__main__":
    main()
