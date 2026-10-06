#!/usr/bin/env python3
"""美股热度(55188异动集的美股版):Yahoo trending + day_gainers 双源合并去重。
Reddit/Stocktwits 在本机直连不可用(匿名403/PullPush反爬429),source 槽位已预留,通了即插。
输出 logs/us_heat_<date>.json {date, symbols:[{symbol,name,heat_score,heat_rank,sources,day_chg}]}。
us_pool.build() 读它做"异动分"(复用 watchpool.score 同一套),标的随热度迭代。
"""
import market_cache
import json
import os
import re
import sys
import urllib.request

SVC = os.path.dirname(os.path.abspath(__file__))
UA = {"User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64)"}
SYM_RE = re.compile(r"^[A-Z]{1,5}$")  # 只要美股普通股:过滤 crypto(-USD)/境外(.AS)/权证


def _get(url, timeout=15):
    req = urllib.request.Request(url, headers=UA)
    return json.load(market_cache.urlopen(req, timeout=timeout))


def fetch_trending():
    """Yahoo 热榜: [(symbol, rank)]。"""
    d = _get("https://query1.finance.yahoo.com/v1/finance/trending/US")
    quotes = d["finance"]["result"][0]["quotes"]
    return [(q["symbol"].upper(), i + 1) for i, q in enumerate(quotes)
            if SYM_RE.match(q["symbol"].upper())]


def fetch_gainers(count=25):
    """Yahoo 当日涨幅榜: [(symbol, name, chg%)]。"""
    d = _get("https://query1.finance.yahoo.com/v1/finance/screener/predefined/saved"
             "?scrIds=day_gainers&count=%d" % count)
    out = []
    for i, q in enumerate(d["finance"]["result"][0]["quotes"]):
        s = (q.get("symbol") or "").upper()
        if not SYM_RE.match(s):
            continue
        out.append((s, q.get("shortName") or s,
                    float(q.get("regularMarketChangePercent") or 0), i + 1))
    return out


def build(date_s=None):
    """合并双源 -> heat_score,落盘。"""
    if date_s is None:
        sys.path.insert(0, SVC)
        from trading_calendar import today_str
        date_s = today_str("US")
    agg = {}
    try:
        for sym, rank in fetch_trending():
            a = agg.setdefault(sym, {"sources": [], "name": sym, "day_chg": 0.0})
            a["sources"].append("yahoo_trending")
            a["trend_pts"] = 21 - rank
    except Exception as e:
        print("trending FAIL %s" % str(e)[:80])
    try:
        for sym, name, chg, rank in fetch_gainers():
            a = agg.setdefault(sym, {"sources": [], "name": name, "day_chg": 0.0})
            a["sources"].append("day_gainers")
            a["name"] = name
            a["day_chg"] = round(chg, 2)
            a["gain_pts"] = (26 - rank) * 0.8 + (8 if chg >= 20 else 5 if chg >= 10 else 0)
    except Exception as e:
        print("gainers FAIL %s" % str(e)[:80])
    syms = []
    for sym, a in agg.items():
        sc = a.get("trend_pts", 0) + a.get("gain_pts", 0)
        if len(a["sources"]) >= 2:
            sc += 3  # 多源共振
        syms.append({"symbol": sym, "name": a["name"],
                     "heat_score": round(sc, 1), "sources": a["sources"],
                     "day_chg": a["day_chg"]})
    syms.sort(key=lambda x: x["heat_score"], reverse=True)
    for i, s in enumerate(syms):
        s["heat_rank"] = i + 1
    # trending 源无涨跌:给前12补一次日线涨跌(便宜,1票1请求)
    try:
        sys.path.insert(0, SVC)
        import bars_us
        for s in syms[:12]:
            if s["day_chg"] == 0.0:
                d = bars_us.yahoo_daily2(s["symbol"])
                if len(d) >= 2 and d[-2]["close"]:
                    s["day_chg"] = round((d[-1]["close"] / d[-2]["close"] - 1) * 100, 2)
    except Exception as e:
        print("heat chg enrich FAIL %s" % str(e)[:60])
    out = {"date": date_s, "symbols": syms[:20],
           "note": "reddit/stocktwits 直连不可用,槽位预留"}
    p = os.path.join(SVC, "logs", "us_heat_%s.json" % date_s)
    json.dump(out, open(p, "w"), ensure_ascii=False, indent=1)
    for s in syms[:12]:
        print("#%-2d %-6s %6.1f %s %+.1f%% %s" % (
            s["heat_rank"], s["symbol"], s["heat_score"], s["name"][:18],
            s["day_chg"], ",".join(s["sources"])))
    return out


def load(date_s=None):
    if date_s is None:
        sys.path.insert(0, SVC)
        from trading_calendar import today_str
        date_s = today_str("US")
    p = os.path.join(SVC, "logs", "us_heat_%s.json" % date_s)
    if os.path.exists(p):
        return json.load(open(p))
    return build(date_s)


if __name__ == "__main__":
    build()
