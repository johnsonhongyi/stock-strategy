#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""逆势带量启动扫描:全A股找"长期盘整+放量突破120日新高"的结构。
把"通道结构突破+带量"的盘口语言翻译成量化规则,每天收盘后跑一遍,
帮用户在启动初期(而不是17块的山顶)发现这类票。

规则(以T日收盘为准):
  A 突破:收盘 >= 过去120日最高收盘*0.98 (创120日新高,允许2%误差)
  B 底部起来:60天前收盘 <= 现价*0.80 (从深水区爬起,不是高位横盘再突破)
  C 带量:近5日均量 >= 2.0 * 前60日均量(剔除近5日)
  D 不过热:突破120日新高后涨幅 <= 30% (只要启动初期,不要已经连板到山顶的)
  E 箱体:过去120日(剔除近5日)最高/最低振幅 >= 30% (之前有过像样的底部箱体)
  F 周期:过去120日里,收盘价低于箱顶*0.85 的天数 >= 40 天 (底部磨了很久,
     不是刚上市/刚V起来;这就是"有周期")
  过滤:ST/*ST/退市股剔除;上市不足130个交易日剔除

用法:
  python3 breakout_scan.py [--asof YYYY-MM-DD] [--top 15] [--push] [--universe FILE]
  --asof: 回测日期(默认今天);K线拉260根后按日期切片
  --push: 推送 top 结果到企业微信+息知(默认只打印)
"""
import market_cache
import sys, os, json, math, urllib.request, urllib.parse
from datetime import datetime

SVC = os.path.dirname(os.path.abspath(__file__))
BASE = "http://127.0.0.1:20081"


def load_token():
    try:
        for line in open(os.path.join(SVC, ".env")):
            if line.strip().startswith("A_STOCK_TOKEN="):
                return line.strip().split("=", 1)[1]
    except OSError:
        pass
    return ""


TOKEN = load_token()


def api(path, timeout=45, force_refresh=False):
    req = urllib.request.Request(BASE + path,
                                 headers={"X-A-Stock-Token": TOKEN})
    with market_cache.urlopen(req, timeout=timeout, force_refresh=force_refresh) as r:
        return json.load(r)


def to_api(code):
    code = code.strip()
    if code.endswith(".SH"):
        return "sh" + code[:-3]
    if code.endswith(".SZ"):
        return "sz" + code[:-3]
    if code.startswith(("sh", "sz")):
        return code
    return "sh" + code if code.startswith("6") else "sz" + code


def norm_sym(sym):
    s = sym.lower().replace("sh", "").replace("sz", "")
    return s.split(".")[0]


def get_universe():
    d = api("/api/v1/stocks/directory", timeout=30)["data"]
    out = []
    for s in d.get("stocks", []):
        code = s.get("code", "")
        name = s.get("name", "")
        if not code or not name:
            continue
        if "ST" in name.upper() or "退" in name:
            continue
        if code.startswith("9"):  # B股
            continue
        out.append((code, name))
    return out


def fetch_bars(codes, limit):
    """批量拉日K,返回 {code: bars}。带重试,进度走stderr。"""
    import time
    res = {}
    api_syms = [to_api(c) for c, _ in codes]
    idx = {to_api(c): c for c, _ in codes}
    total = (len(api_syms) + 14) // 15
    done = 0
    for i in range(0, len(api_syms), 15):
        chunk = api_syms[i:i + 15]
        data = None
        for attempt in range(3):
            try:
                data = api("/api/v1/quotes/kline/batch?symbols=%s&period=day"
                           "&limit=%d" % (",".join(chunk), limit),
                           timeout=60)["data"] or {}
                break
            except Exception as e:
                if attempt == 2:
                    print("batch kl fail x3: %s" % e, flush=True,
                          file=sys.stderr)
                time.sleep(2 * (attempt + 1))
        done += 1
        if done % 20 == 0:
            print("fetch %d/%d" % (done, total), flush=True, file=sys.stderr)
        if not data:
            continue
        for sym, bars in data.items():
            c = idx.get(sym) or norm_sym(sym)
            if bars and len(bars) >= 130:
                res[c] = bars
    return res


def evaluate(bars):
    """对一组日K(按T日收盘)打分,返回 None 或 dict"""
    n = len(bars)
    if n < 130:
        return None
    closes = [b["close"] for b in bars]
    vols = [b.get("volume") or 0 for b in bars]
    if not closes[-1] or not closes[-61]:
        return None
    base = closes[-130:-5]
    H = max(base)
    L = min(base)
    if not H or not L:
        return None
    price = closes[-1]
    # A 突破
    brk = price / H
    if brk < 0.98:
        return None
    # B 底部起来
    if closes[-61] > price * 0.80:
        return None
    # C 带量
    v5 = sum(vols[-5:]) / 5
    v60 = sum(vols[-70:-5]) / 60
    if v60 <= 0 or v5 < 2.0 * v60:
        return None
    # D 不过热:刚突破新高,涨幅<=30%
    if brk > 1.30:
        return None
    # E 箱体
    if (H - L) / L < 0.30:
        return None
    # F 周期:底部磨底天数
    grind = sum(1 for c in base if c < H * 0.85)
    if grind < 40:
        return None
    chg60 = (price / closes[-61] - 1) * 100
    score = (v5 / v60) * brk
    return {"price": price, "brk_pct": (brk - 1) * 100,
            "chg60": chg60, "vol_ratio": v5 / v60,
            "base_low": L, "base_high": H, "grind_days": grind,
            "score": score}


def scan(asof=None, top=15):
    uni = get_universe()
    print("universe: %d" % len(uni), flush=True, file=sys.stderr)
    limit = 240 if asof else 140  # 批量接口上限240
    bars_map = fetch_bars(uni, limit)
    print("bars ok: %d" % len(bars_map), flush=True, file=sys.stderr)
    names = {c: n for c, n in uni}
    hits = []
    for code, bars in bars_map.items():
        if asof:
            cut = -1
            for i, b in enumerate(bars):
                t = (b.get("time") or "")[:10]
                if t and t <= asof:
                    cut = i
            if cut < 129:
                continue
            bars = bars[:cut + 1]
        r = evaluate(bars)
        if r:
            r["code"] = code
            r["name"] = names.get(code, "")
            hits.append(r)
    hits.sort(key=lambda r: -r["score"])
    return hits[:top], len(bars_map)


def draw_breakout(code, name, bars, info, outdir):
    """画逆势启动结构图:130日K线+箱体上下沿+突破点标记(用户的通道语言)。"""
    import matplotlib
    matplotlib.use("Agg")
    import matplotlib.pyplot as plt
    from matplotlib.patches import Rectangle
    plt.rcParams["font.family"] = "Noto Sans CJK JP"
    plt.rcParams["axes.unicode_minus"] = False
    os.makedirs(outdir, exist_ok=True)
    closes = [b["close"] for b in bars]
    opens = [b["open"] for b in bars]
    highs = [b["high"] for b in bars]
    lows = [b["low"] for b in bars]
    vols = [b.get("volume") or 0 for b in bars]
    dates = [(b.get("time") or "")[5:10].replace("-", "/") for b in bars]
    H, L = info["base_high"], info["base_low"]

    fig, (ax1, ax2) = plt.subplots(
        2, 1, figsize=(11, 7), gridspec_kw={"height_ratios": [3, 1]},
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
        ax1.plot([x[i], x[i]], [lows[i], highs[i]], color=color, lw=0.8)
        ax1.add_patch(Rectangle(
            (x[i] - 0.35, min(opens[i], closes[i])), 0.7,
            max(abs(closes[i] - opens[i]), (highs[i] - lows[i]) * 0.02 or 0.01),
            facecolor=color, edgecolor=color))
    # 箱体:用户通道结构的上轨/下轨
    ax1.axhline(H, color="#ffeb3b", ls="--", lw=1.2, label="箱顶%.2f" % H)
    ax1.axhline(L, color="#9e9e9e", ls="--", lw=1.0, label="箱底%.2f" % L)
    ax1.axhspan(L, H, color="#ffeb3b", alpha=0.05)
    # 突破点
    ax1.scatter([x[-1]], [closes[-1]], s=120, color="#f23645", zorder=5,
                marker="*", label="突破")
    ax1.legend(facecolor="#1a1d23", edgecolor="#3c4043", labelcolor="white",
               fontsize=8, loc="upper left")
    ax1.set_title(
        "%s %s  收%.2f  120日新高%+.1f%%  60日%+.1f%%  量比%.1fx  磨底%d天" % (
            name, code, closes[-1], info["brk_pct"], info["chg60"],
            info["vol_ratio"], info["grind_days"]),
        color="white", fontsize=12, pad=10)
    ax1.grid(True, alpha=0.15)
    vmax = max(vols) or 1
    for i in range(len(bars)):
        color = "#f23645" if closes[i] >= opens[i] else "#089981"
        ax2.bar(x[i], vols[i] / 1e4, color=color, alpha=0.7, width=0.7)
    ax2.set_ylabel("量(万手)", color="#9aa0a6", fontsize=8)
    ax2.set_xticks(x[::15])
    ax2.set_xticklabels([dates[i] for i in x[::15]])
    ax2.grid(True, alpha=0.15)
    path = os.path.join(outdir, "%s.png" % code)
    fig.tight_layout()
    fig.savefig(path, dpi=110, facecolor=fig.get_facecolor())
    plt.close(fig)
    return path


def main():
    asof = None
    top = 15
    push = False
    args = sys.argv[1:]
    i = 0
    while i < len(args):
        if args[i] == "--asof" and i + 1 < len(args):
            asof = args[i + 1]; i += 2
        elif args[i] == "--top" and i + 1 < len(args):
            top = int(args[i + 1]); i += 2
        elif args[i] == "--push":
            push = True; i += 1
        else:
            i += 1
    hits, n = scan(asof=asof, top=top)
    sys.stdout.write(json.dumps(
        {"asof": asof or "today", "scanned": n, "hits": len(hits),
         "top": [{"code": h["code"], "name": h["name"],
                  "price": round(h["price"], 2),
                  "brk": round(h["brk_pct"], 1),
                  "chg60": round(h["chg60"], 1),
                  "volx": round(h["vol_ratio"], 1)}
                 for h in hits]},
        ensure_ascii=False, indent=1))
    sys.stdout.write("\n")
    if push and hits:
        from push import send as push_send
        lines = []
        for h in hits:
            lines.append("- 🔴 **%s %s** %.2f:120日新高%+.1f%%,60日%+.1f%%,"
                         "5日均量%.1fx" % (
                             h["name"], h["code"], h["price"], h["brk_pct"],
                             h["chg60"], h["vol_ratio"]))
        title = "逆势带量启动 %s|%d只" % (asof or "今日", len(hits))
        body = "> 长期盘整+放量突破120日新高\n\n" + "\n\n".join(lines)
        print(push_send(title, body))


if __name__ == "__main__":
    main()
