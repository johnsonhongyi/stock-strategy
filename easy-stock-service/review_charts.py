#!/usr/bin/env python3
"""给候选池画 K线+1/3/5/10日VWAP 叠加图,并输出结构拆解数据。"""
import market_cache
import json
import os
import sys
import urllib.request
from datetime import datetime
from zoneinfo import ZoneInfo

import matplotlib
matplotlib.use("Agg")
import matplotlib.pyplot as plt
from matplotlib.patches import Rectangle

SVC = os.path.dirname(os.path.abspath(__file__))
BJ = ZoneInfo("Asia/Shanghai")
BASE = "http://127.0.0.1:20081"
plt.rcParams["font.family"] = "Noto Sans CJK JP"
plt.rcParams["axes.unicode_minus"] = False


def load_token():
    with open(os.path.join(SVC, ".env")) as f:
        for line in f:
            if line.startswith("A_STOCK_TOKEN="):
                return line.strip().split("=", 1)[1]
    return ""


def api(path, timeout=30, force_refresh=False):
    req = urllib.request.Request(
        BASE + path, headers={"X-A-Stock-Token": load_token()})
    with market_cache.urlopen(req, timeout=timeout, force_refresh=force_refresh) as r:
        return json.load(r)


def bar_amount(b):
    """成交额:真实值优先,缺失时用典型价×量估算(东财K线偶发amount=0时兜底)。"""
    amt = b.get("amount") or 0
    if amt > 0:
        return amt, False
    vol = b.get("volume") or 0
    tp = (b["high"] + b["low"] + b["close"]) / 3.0
    return tp * vol * 100.0, True


def vwap_series(bars, n):
    """每日收盘的 N 日锚定 VWAP 序列。"""
    out = []
    estimated = False
    for i in range(len(bars)):
        win = bars[max(0, i - n + 1):i + 1]
        amt = 0.0
        for b in win:
            a, est = bar_amount(b)
            amt += a
            estimated = estimated or est
        vol = sum((b.get("volume") or 0) for b in win)
        out.append(amt / (vol * 100.0) if vol > 0 else None)
    return out, estimated


def slope(cur, prev, th=0.003):
    if cur is None or prev is None or prev == 0:
        return "flat"
    r = (cur - prev) / prev
    return "up" if r >= th else ("down" if r <= -th else "flat")


def draw(symbol, name, outdir, exact=None):
    data = api("/api/v1/quotes/kline/batch?symbols=%s&period=day&limit=40"
               % symbol)["data"]
    key = next(k for k in data if symbol in k)
    bars = data[key][-30:]
    dates = [(b.get("time") or "")[5:10].replace("-", "/") for b in bars]
    opens = [b["open"] for b in bars]
    highs = [b["high"] for b in bars]
    lows = [b["low"] for b in bars]
    closes = [b["close"] for b in bars]
    vols = [b.get("volume") or 0 for b in bars]
    # 涨跌幅:优先接口字段,缺失时用收盘价自算
    chg = bars[-1].get("change_percent")
    if chg is None and len(closes) > 1 and closes[-2]:
        chg = (closes[-1] - closes[-2]) / closes[-2] * 100

    v1, e1 = vwap_series(bars, 1)
    v3, e3 = vwap_series(bars, 3)
    v5, e5 = vwap_series(bars, 5)
    v10, e10 = vwap_series(bars, 10)
    estimated = e1 or e3 or e5 or e10

    fig, (ax1, ax2) = plt.subplots(
        2, 1, figsize=(10, 7), gridspec_kw={"height_ratios": [3, 1]},
        sharex=True)
    fig.patch.set_facecolor("#0f1115")
    for ax in (ax1, ax2):
        ax.set_facecolor("#0f1115")
        ax.tick_params(colors="#9aa0a6", labelsize=8)
        for s in ax.spines.values():
            s.set_color("#3c4043")

    x = list(range(len(bars)))
    for i in range(len(bars)):
        up = closes[i] >= opens[i]
        color = "#f23645" if up else "#089981"
        ax1.plot([x[i], x[i]], [lows[i], highs[i]], color=color, lw=1)
        ax1.add_patch(Rectangle(
            (x[i] - 0.35, min(opens[i], closes[i])), 0.7,
            max(abs(closes[i] - opens[i]), (highs[i] - lows[i]) * 0.02 or 0.01),
            facecolor=color, edgecolor=color))

    styles = [("1日", v1, "#ffeb3b", 1.6), ("3日", v3, "#4fc3f7", 1.2),
              ("5日", v5, "#ce93d8", 1.2), ("10日", v10, "#9e9e9e", 1.0)]
    for label, vs, color, lw in styles:
        ax1.plot(x, vs, color=color, lw=lw, label=f"VWAP{label}")
    ax1.legend(facecolor="#1a1d23", edgecolor="#3c4043", labelcolor="white",
               fontsize=8, loc="upper left")
    # 今日1日VWAP:优先用复盘报告的精确值(真实成交额算出),估算值仅画线用
    v1_today = exact["vwap1"] if exact and exact.get("vwap1") else v1[-1]
    chg_txt = f"{chg:+.2f}%" if chg is not None else "—"
    dev = (closes[-1] - v1_today) / v1_today * 100 if v1_today else 0.0
    ax1.set_title(
        f"{name} {symbol}  收{closes[-1]:.2f} "
        f"({chg_txt})  偏离1日VWAP{dev:+.2f}%",
        color="white", fontsize=13, pad=12)
    ax1.grid(True, alpha=0.15)

    vmax = max(vols) or 1
    for i in range(len(bars)):
        color = "#f23645" if closes[i] >= opens[i] else "#089981"
        ax2.bar(x[i], vols[i] / 1e4, color=color, alpha=0.7, width=0.7)
    ax2.set_ylabel("量(万手)", color="#9aa0a6", fontsize=8)
    ax2.set_xticks(x[::3])
    ax2.set_xticklabels([dates[i] for i in x[::3]], rotation=0)
    ax2.grid(True, alpha=0.15)

    path = os.path.join(outdir, f"{symbol}.png")
    fig.tight_layout()
    fig.savefig(path, dpi=110, facecolor=fig.get_facecolor())
    plt.close(fig)

    # 结构拆解
    price = closes[-1]
    vv = {"d1": v1[-1], "d3": v3[-1], "d5": v5[-1], "d10": v10[-1]}
    pv = {"d1": v1[-2], "d3": v3[-2], "d5": v5[-2], "d10": v10[-2]}
    sl = {k: slope(vv[k], pv[k]) for k in vv}
    vals = [price, vv["d1"], vv["d3"], vv["d5"], vv["d10"]]
    def _nz(x):
        return x if x else 0
    vals_nz = [_nz(v) for v in vals]
    align = "bull" if vals_nz[0] > vals_nz[1] > vals_nz[2] > vals_nz[3] > vals_nz[4] else (
        "bear" if vals_nz[0] < vals_nz[1] < vals_nz[2] < vals_nz[3] < vals_nz[4] else "mixed")
    flow = "up" if all(s == "up" for s in sl.values()) else (
        "down" if all(s == "down" for s in sl.values()) else "mixed")
    # 近5日成交量均值 vs 今日(成交额缺失时用成交量代替)
    vol_ratio = (vols[-1] / (sum(vols[-6:-1]) / 5)
                 if sum(vols[-6:-1]) > 0 else None)
    # 10日最高/最低位置
    hi10 = max(highs[-10:])
    pos10 = (price - min(lows[-10:])) / (hi10 - min(lows[-10:])) \
        if hi10 > min(lows[-10:]) else 0.5
    return {
        "symbol": symbol, "name": name, "price": price,
        "change_percent": chg, "high": highs[-1], "low": lows[-1],
        "vwap": {k: (round(v, 3) if v else None) for k, v in vv.items()},
        "slope": sl, "alignment": align, "flow": flow,
        "deviation": round(dev, 2),
        "vol_ratio": round(vol_ratio, 2) if vol_ratio else None,
        "pos10": round(pos10 * 100),
        "stop": round(_nz(vv["d3"]) * 0.995, 2) if _nz(vv["d3"]) else None,
        "estimated": estimated,
        "chart": path,
    }


def main():
    today = datetime.now(BJ).strftime("%Y-%m-%d")
    outdir = os.path.join(SVC, "reviews", "charts", today)
    os.makedirs(outdir, exist_ok=True)
    manifest_path = os.path.join(outdir, "manifest.json")
    targets = []
    exact = {}
    if os.path.exists(manifest_path):
        try:
            with open(manifest_path) as f:
                manifest = json.load(f)
            for m in manifest:
                targets.append((m["symbol"], m["name"]))
                if m.get("vwap1"):
                    exact[m["symbol"]] = {"vwap1": m["vwap1"]}
        except (OSError, ValueError):
            pass
    if not targets:
        # 兜底:默认4只(复盘报告精确值: price/(1+dev))
        targets = [("600707", "彩虹股份"), ("600733", "北汽蓝谷"),
                   ("605089", "味知香"), ("600802", "福建水泥")]
        exact = {
            "600707": {"vwap1": 10.38 / 1.0065},
            "600733": {"vwap1": 4.95 / 1.0072},
            "605089": {"vwap1": 22.00 / 1.0195},
            "600802": {"vwap1": 7.28 / 1.0322},
        }
    results = []
    for sym, name in targets:
        try:
            results.append(draw(sym, name, outdir, exact.get(sym)))
            print(f"ok {sym}", flush=True)
        except Exception as e:
            print(f"fail {sym}: {e}", flush=True)
    with open(os.path.join(outdir, "structures.json"), "w") as f:
        json.dump(results, f, ensure_ascii=False, indent=1)
    print("DIR:" + outdir)


if __name__ == "__main__":
    main()
