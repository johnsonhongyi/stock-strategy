#!/usr/bin/env python3
"""VWAP结构盘后复盘:交易日 15:35 运行。

属于我们的方案 = VWAP结构定义强弱 × 流动方向定义趋势 × 资金共振做验证 × T+1纪律管出手。

报告结构:
  一、水位:核心指数涨跌 + 涨停梯队(涨停数/连板高度/炸板率)
  二、轮动:行业涨跌Top/Bottom + 题材资金净流入Top(钱往哪切)
  三、回踩企稳跟踪池:重点池筛"10日大涨>=15%→回落2%~12%→缩量→企稳(回踩1日线/刚站上/整理末端)"
     →第二段买点(可操作);结构未坏(不破10日线)是底线
  四、突破跟踪池:筛"多头排列+四线同上+站上1日线"→只看不追,提供热度参照
  五、北汽蓝谷:当日结构复述 + 明日计划

输出:JSON(stdout,供cron worker) + Markdown存档(reviews/YYYY-MM-DD.md)
     + 外部推送(push.py,标题即结论)
非交易日静默退出(trade_date != 今日)。
"""
import market_cache
import json
import os
import sys
import urllib.request
import urllib.parse
from datetime import datetime
from zoneinfo import ZoneInfo

SVC = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, SVC)
BJ = ZoneInfo("Asia/Shanghai")
BASE = "http://127.0.0.1:20081"
REVIEW_DIR = os.path.join(SVC, "reviews")
WATCHLIST_FILE = os.path.join(SVC, "watchlist.json")
SLOPE_TH = 0.003


def load_token():
    try:
        with open(os.path.join(SVC, ".env")) as f:
            for line in f:
                if line.startswith("A_STOCK_TOKEN="):
                    return line.strip().split("=", 1)[1]
    except OSError:
        pass
    return ""


TOKEN = load_token()


def api(path, timeout=25, force_refresh=False):
    req = urllib.request.Request(BASE + path,
                                 headers={"X-A-Stock-Token": TOKEN})
    with market_cache.urlopen(req, timeout=timeout, force_refresh=force_refresh) as r:
        return json.load(r)


def out(obj):
    json.dump(obj, sys.stdout, ensure_ascii=False)
    sys.stdout.write("\n")
    sys.stdout.flush()


def norm_sym(s):
    s = (s or "").upper().replace(".SH", "").replace(".SZ", "")
    return s


def vwap_of(bars):
    amt = sum((b.get("amount") or 0) for b in bars or [])
    vol = sum((b.get("volume") or 0) for b in bars or [])
    if vol <= 0:
        return None
    if amt > 0:
        return amt / (vol * 100.0)
    # 成交额缺失(如新浪K线源)时用典型价估算,误差通常<0.5%
    est = sum(((b.get("high") or 0) + (b.get("low") or 0)
               + (b.get("close") or 0)) / 3.0 * (b.get("volume") or 0)
              for b in bars or [])
    return est / vol if vol > 0 else None


def slope(cur, prev):
    if cur is None or prev is None or prev == 0:
        return "flat"
    r = (cur - prev) / prev
    if r >= SLOPE_TH:
        return "up"
    if r <= -SLOPE_TH:
        return "down"
    return "flat"


def analyze_daily(daily_bars):
    """盘后版VWAP结构:用日K直接算,精确值。"""
    bars = daily_bars or []
    if len(bars) < 11:
        return None
    today_bar = bars[-1]
    price = today_bar.get("close")
    hist = bars[:-1]

    def anchored(n):
        past = hist[-(n - 1):] if n > 1 else []
        return vwap_of(past + [today_bar])

    def anchored_prev(n):
        return vwap_of(hist[-n:])

    vwap = {f"d{n}": anchored(n) for n in (1, 3, 5, 10)}
    vwap_prev = {f"d{n}": anchored_prev(n) for n in (1, 3, 5, 10)}
    sl = {k: slope(vwap[k], vwap_prev[k]) for k in vwap}
    pos = {k: ("above" if price >= v else "below")
           for k, v in vwap.items()
           if v is not None and price is not None}
    vals = [price, vwap["d1"], vwap["d3"], vwap["d5"], vwap["d10"]]
    alignment = "mixed"
    if all(x is not None for x in vals):
        if vals[0] > vals[1] > vals[2] > vals[3] > vals[4]:
            alignment = "bull"
        elif vals[0] < vals[1] < vals[2] < vals[3] < vals[4]:
            alignment = "bear"
    ups = sum(1 for s in sl.values() if s == "up")
    dns = sum(1 for s in sl.values() if s == "down")
    flow = "up" if ups == 4 else ("down" if dns == 4 else "mixed")
    dev = (price - vwap["d1"]) / vwap["d1"] if price and vwap["d1"] else None
    return {
        "price": price, "change_percent": today_bar.get("change_percent"),
        "high": today_bar.get("high"), "low": today_bar.get("low"),
        "amount": today_bar.get("amount"),
        "vwap": {k: round(v, 3) if v else None for k, v in vwap.items()},
        "slope": sl, "position": pos, "alignment": alignment, "flow": flow,
        "deviation": round(dev, 4) if dev is not None else None,
    }


def is_st(name):
    return "ST" in (name or "").upper()


def analyze_pullback(bars, st):
    """回踩企稳跟踪:大涨→回落整理→企稳,找第二段的买点而不是追涨。

    两档输出:
    - tracking(整理跟踪中):10日大涨>=15%,回落2%~15%,未破10日线,10日线不向下。
      每天给位置:回落多深、整理几天、量缩了没、离1日线多远。
    - triggered(企稳触发,可操作):tracking + 缩量(近3日均量<大涨阶段×0.8)
      + 企稳信号(回踩1日线±1.5%/刚站上1日线/整理末端)。
    """
    if len(bars) < 15 or not st:
        return None
    price = st["price"]
    if not price:
        return None
    win = bars[-11:]
    base = min((b.get("low") or float("inf")) for b in win)
    peak = max((b.get("high") or 0) for b in win)
    if not base or base == float("inf") or not peak:
        return None
    surge = (peak - base) / base
    limitup = any((b.get("change_percent") or 0) >= 9.5 for b in bars[-6:])
    if surge < 0.15 and not limitup:
        return None
    pullback = (peak - price) / peak
    if not (0.02 <= pullback <= 0.15):
        return None
    v = st["vwap"]
    if not v["d10"] or price < v["d10"]:
        return None
    if st["slope"].get("d10") == "down":
        return None
    # 量能:大涨阶段(涨幅最大的3根K)均量 vs 近3日均量
    gains = sorted(win, key=lambda b: ((b.get("close") or 0) - (b.get("open") or 0))
                   / (b.get("open") or 1), reverse=True)[:3]
    surge_vol = sum(b.get("volume") or 0 for b in gains) / 3 or 1
    recent_vol = sum(b.get("volume") or 0 for b in bars[-3:]) / 3
    vol_ratio = recent_vol / surge_vol
    shrunk = vol_ratio < 0.8
    # 企稳信号
    dev = st["deviation"] or 0
    triggers = []
    if abs(dev) <= 0.015:
        triggers.append("回踩1日线")
    prev = analyze_daily(bars[:-1])
    if prev and prev["position"].get("d1") == "below" \
            and st["position"].get("d1") == "above":
        triggers.append("刚站上1日线")
    c3 = [(b.get("close") or 0) for b in bars[-3:]]
    r3 = [((b.get("high") or 0) - (b.get("low") or 0)) for b in bars[-3:]]
    r6 = [((b.get("high") or 0) - (b.get("low") or 0)) for b in bars[-6:-3]]
    if c3[0] and c3[2] >= c3[0] and sum(r3) / 3 < (sum(r6) / 3 or 1):
        triggers.append("整理末端")
    peak_idx = max(range(len(win)),
                   key=lambda i: win[i].get("high") or 0)
    days = len(win) - 1 - peak_idx
    stop = round(v["d3"] * 0.995, 2) if v["d3"] else None
    base_info = {
        "price": price, "change_percent": st["change_percent"],
        "surge": round(surge * 100, 1), "peak": peak,
        "pullback": round(pullback * 100, 1), "days": days,
        "vwap1": v["d1"], "stop": stop, "target": peak,
        "vol_ratio": round(vol_ratio, 2),
        "dist_vwap1": round(dev * 100, 2),
    }
    if shrunk and triggers:
        return {**base_info, "stage": "triggered",
                "trigger": "、".join(triggers)}
    return {**base_info, "stage": "tracking", "trigger": ""}

def main():
    from trading_calendar import guard_trading_day
    guard_trading_day("daily_review")
    now = datetime.now(BJ)
    today = now.strftime("%Y-%m-%d")
    if now.weekday() >= 5:
        out({"notify": False, "reason": "weekend"})
        return
    if not TOKEN:
        out({"notify": False, "reason": "no_token"})
        return

    # 交易日判断:涨停梯队的 trade_date
    try:
        ladder = api("/api/v1/short-term/limit-up-ladder")["data"]
    except Exception as e:
        out({"notify": False, "reason": "data_error: %s" % e})
        return
    trade_date = (ladder.get("current") or {}).get("trade_date", "")
    if trade_date != today:
        out({"notify": False, "reason": "not_trading_day",
             "trade_date": trade_date})
        return

    # 一、水位:指数 + 涨停梯队
    try:
        indexes = api("/api/v1/market/indexes?scope=core")["data"]
    except Exception:
        indexes = []
    idx_map = {}
    for idx in indexes or []:
        idx_map[idx.get("id")] = idx
    cur = ladder.get("current") or {}
    water = {
        "indexes": [
            {"name": (idx_map.get(k) or {}).get("name", k),
             "price": (idx_map.get(k) or {}).get("price"),
             "change_percent": (idx_map.get(k) or {}).get("change_percent")}
            for k in ("sse", "szse", "chinext", "sse50", "star50")
            if k in idx_map
        ],
        "limit_up": cur.get("limit_up_count"),
        "board": cur.get("board_count"),
        "max_streak": cur.get("max_streak"),
        "reopened": cur.get("reopened_count"),
        "first_board": cur.get("first_board_count"),
        "max_streak_stocks": [
            {"name": s.get("name"), "symbol": norm_sym(s.get("symbol")),
             "streak": s.get("streak")}
            for lv in (cur.get("levels") or [])
            for s in (lv.get("stocks") or [])[:3]
            if lv.get("level") == cur.get("max_streak")
        ][:3],
    }

    # 二、轮动:行业 + 题材资金流
    try:
        industries = api("/api/v1/market/industries?limit=100") or []
        if isinstance(industries, dict):
            industries = industries.get("data", industries)
    except Exception:
        industries = []
    industries = [i for i in industries if i.get("name")]
    by_chg = sorted(industries, key=lambda x: x.get("change_percent") or 0,
                    reverse=True)
    rotation = {
        "top": [{"name": i["name"], "chg": i.get("change_percent"),
                 "chg5": i.get("five_day_change_percent"),
                 "leader": i.get("leader_name"),
                 "leader_chg": i.get("leader_change_percent")}
                for i in by_chg[:5]],
        "bottom": [{"name": i["name"], "chg": i.get("change_percent"),
                    "chg5": i.get("five_day_change_percent")}
                   for i in by_chg[-5:]],
    }
    try:
        flows = api("/api/v1/market/flows?dimension=theme&sort=ratio&limit=8"
                    )["data"]
    except Exception:
        flows = []
    money = [{"name": f.get("name"), "chg": round(f.get("change_percent") or 0, 2),
              "net_inflow": f.get("net_inflow"),
              "leader": f.get("leader_name")}
             for f in (flows or [])[:5]]

    # 三、重点池:热股榜Top30 + 行业领涨龙头 + 自选
    pool = {}
    try:
        hot = api("/api/v1/stocks/hot-ranks")["data"]["stocks"]
        for s in hot[:30]:
            code = norm_sym(s.get("symbol"))
            if code and not is_st(s.get("name")):
                pool[code] = s.get("name")
    except Exception:
        pass
    for i in by_chg[:15]:
        code = norm_sym(i.get("leader_symbol"))
        if code and not is_st(i.get("leader_name")):
            pool.setdefault(code, i.get("leader_name"))
    try:
        wl = json.load(open(WATCHLIST_FILE)).get("symbols", [])
    except OSError:
        wl = ["600733"]
    for code in wl:
        pool.setdefault(norm_sym(code), "")
    symbols = sorted(pool.keys())

    # 名称补全:本地代码表
    name_map = {}
    try:
        directory = api("/api/v1/stocks/directory")["data"]["stocks"]
        for s in directory:
            name_map[norm_sym(s.get("symbol"))] = s.get("name", "")
    except Exception:
        pass
    for code in symbols:
        if not pool.get(code) and name_map.get(code):
            pool[code] = name_map[code]

    # 批量日K(15只/批,取25日:回踩跟踪需要看10日大涨+整理;limit=25时后端慢,批量改小)
    klines = {}
    for i in range(0, len(symbols), 15):
        batch = symbols[i:i + 15]
        try:
            data = api("/api/v1/quotes/kline/batch?symbols=%s&period=day&limit=25"
                       % ",".join(batch), timeout=60)["data"]
            for sym, bars in (data or {}).items():
                code = norm_sym(sym)
                if code and bars:
                    klines[code] = bars
        except Exception:
            continue

    candidates = []
    pullbacks = []
    structures = {}
    for code in symbols:
        bars = klines.get(code)
        if not bars:
            continue
        st = analyze_daily(bars)
        if not st:
            continue
        name = pool.get(code) or ""
        structures[code] = {"name": name, **st}
        if st["alignment"] == "bull" and st["flow"] == "up" \
                and st["position"].get("d1") == "above":
            candidates.append({
                "symbol": code, "name": name, "price": st["price"],
                "change_percent": st["change_percent"],
                "deviation": st["deviation"],
                "amount": st["amount"],
            })
        pb = analyze_pullback(bars, st)
        if pb and not is_st(name):
            pullbacks.append({"symbol": code, "name": name, **pb})
    # 按偏离1日VWAP从小到大(越贴线越安全),成交额大的优先
    candidates.sort(key=lambda c: (abs(c["deviation"] or 1),
                                   -(c["amount"] or 0)))
    # 回踩池分档:triggered(企稳触发,可操作)在前,tracking(整理跟踪中)在后
    trig_rank = {"刚站上1日线": 0, "回踩1日线": 1, "整理末端": 2}
    triggered = [p for p in pullbacks if p["stage"] == "triggered"]
    tracking = [p for p in pullbacks if p["stage"] == "tracking"]
    triggered.sort(key=lambda p: (min(trig_rank.get(t, 9)
                                      for t in p["trigger"].split("、")),
                                  p["pullback"]))
    tracking.sort(key=lambda p: p["pullback"])

    # 四、北汽蓝谷明日计划
    bq = structures.get("600733")
    bq_plan = None
    if bq:
        v = bq["vwap"]
        stop = round(v["d3"] * 0.995, 2) if v["d3"] else None
        bq_plan = {
            "price": bq["price"], "change_percent": bq["change_percent"],
            "structure": "多头排列四线同上" if bq["alignment"] == "bull"
            and bq["flow"] == "up" else "%s/%s" % (bq["alignment"],
                                                  bq["flow"]),
            "defense": v["d1"], "stop": stop,
            "plan": "持有,防守1日VWAP%s;跌破动态止损%s走人;冲高5.04-5.05分批减仓"
                    % (v["d1"], stop),
        }

    # 存档 Markdown
    os.makedirs(REVIEW_DIR, exist_ok=True)
    weekday = "一二三四五六日"[now.weekday()]
    lines = [
        "# 盘后复盘 %s(周%s)" % (today, weekday), "",
        "## 一、水位",
    ]
    for ix in water["indexes"]:
        lines.append("- %s %s(%+.2f%%)" % (
            ix["name"], ix["price"], ix["change_percent"] or 0))
    lines.append("- 涨停%s家,连板%s家,最高%s连板(%s),炸板%s家" % (
        water["limit_up"], water["board"], water["max_streak"],
        "、".join(s["name"] for s in water["max_streak_stocks"]) or "无",
        water["reopened"]))
    lines += ["", "## 二、轮动(钱往哪切)"]
    lines.append("领涨: " + " | ".join(
        "%s%+.2f%%(5日%+.2f%%,龙头%s)" % (t["name"], t["chg"] or 0,
                                         t["chg5"] or 0, t["leader"])
        for t in rotation["top"]))
    lines.append("领跌: " + " | ".join(
        "%s%.2f%%" % (b["name"], b["chg"] or 0)
        for b in rotation["bottom"]))
    lines.append("资金净流入: " + " | ".join(
        "%s(龙头%s)" % (m["name"], m["leader"]) for m in money))
    lines += ["", "## 三、回踩企稳跟踪池(大涨→回落整理→企稳,第二段买点)"]
    lines.append("### 企稳触发(可操作)%d只" % len(triggered))
    if triggered:
        for p in triggered[:10]:
            lines.append(
                "- %s %s 现价%s(%+.2f%%),10日大涨%+.1f%%后回落%.1f%%(整理%s天),%s;回踩位%s,止损%s,目标前高%s" % (
                    p["symbol"], p["name"], p["price"],
                    p["change_percent"] or 0, p["surge"], p["pullback"],
                    p["days"], p["trigger"], p["vwap1"], p["stop"],
                    p["target"]))
    else:
        lines.append("- 无")
    lines.append("### 整理跟踪中%d只" % len(tracking))
    if tracking:
        for p in tracking[:12]:
            lines.append(
                "- %s %s 现价%s,10日大涨%+.1f%%后回落%.1f%%(整理%s天),量比%.2f,距1日线%+.2f%%;回踩位%s,止损%s" % (
                    p["symbol"], p["name"], p["price"], p["surge"],
                    p["pullback"], p["days"], p["vol_ratio"],
                    p["dist_vwap1"], p["vwap1"], p["stop"]))
    else:
        lines.append("- 无")
    lines += ["", "## 四、突破跟踪池(多头排列+四线同上+站上1日线,看,不追)%d只" % len(candidates)]
    for c in candidates[:15]:
        flag = "⚠偏离超3%不追" if abs(c["deviation"] or 0) > 0.03 else ""
        lines.append("- %s %s 现价%s(%+.2f%%),偏离1日%+.2f%%%s" % (
            c["symbol"], c["name"], c["price"], c["change_percent"] or 0,
            (c["deviation"] or 0) * 100, flag))
    if bq_plan:
        lines += ["", "## 五、北汽蓝谷明日计划",
                  "- 现价%s(%+.2f%%),结构:%s" % (
                      bq_plan["price"], bq_plan["change_percent"] or 0,
                      bq_plan["structure"]),
                  "- " + bq_plan["plan"]]
    md_path = os.path.join(REVIEW_DIR, "%s.md" % today)
    with open(md_path, "w") as f:
        f.write("\n".join(lines) + "\n")

    # 图表清单:企稳触发优先,其次整理跟踪,再补突破池,最多6只
    chart_dir = os.path.join(REVIEW_DIR, "charts", today)
    os.makedirs(chart_dir, exist_ok=True)
    chart_syms = []
    for p in (triggered + tracking)[:4]:
        chart_syms.append({"symbol": p["symbol"], "name": p["name"],
                           "vwap1": p["vwap1"], "kind": "pullback"})
    for c in candidates[:6 - len(chart_syms)]:
        if c["symbol"] not in {s["symbol"] for s in chart_syms}:
            st_c = structures.get(c["symbol"], {})
            chart_syms.append({"symbol": c["symbol"], "name": c["name"],
                               "vwap1": (st_c.get("vwap") or {}).get("d1"),
                               "kind": "breakout"})
    with open(os.path.join(chart_dir, "manifest.json"), "w") as f:
        json.dump(chart_syms, f, ensure_ascii=False, indent=1)

    # 生成K线图(企业微信图文推送用);失败不影响文字推送
    chart_imgs = []
    try:
        from review_charts import draw as draw_chart
        for c in chart_syms:
            try:
                exact = {"vwap1": c["vwap1"]} if c.get("vwap1") else None
                draw_chart(c["symbol"], c["name"], chart_dir, exact)
                chart_imgs.append(os.path.join(chart_dir, c["symbol"] + ".png"))
            except Exception as e:
                print("chart fail %s: %s" % (c["symbol"], e), flush=True)
    except Exception as e:
        print("chart module fail: %s" % e, flush=True)

    # 推送:标题即结论(息知卡片只显示标题,纯文本不支持图片;企业微信收markdown+K线图)
    sse = next((x for x in water["indexes"] if "上证" in x["name"]), {})
    sse_chg = sse.get("change_percent") or 0
    title = "%s盘后复盘%s|上证%+.2f%%涨停%s家|企稳%s只" % (
        "🔴" if sse_chg > 0 else ("🟢" if sse_chg < 0 else "🟡"),
        today[5:].replace("-", ""),
        sse_chg, water["limit_up"], len(triggered))
    body_lines = [
        "🔴领涨:%s" % "、".join(t["name"] for t in rotation["top"][:3]),
        "🔴资金:%s" % "、".join(m["name"] for m in money[:3]),
        "🔴企稳触发:%s" % "、".join("%s%s(%s)" % (p["name"], p["symbol"], p["trigger"])
                              for p in triggered[:5]) or "无",
        "🟡整理跟踪:%s" % "、".join("%s%s" % (p["name"], p["symbol"])
                              for p in tracking[:5]) or "无",
    ]
    if bq_plan:
        body_lines.append("600733:%s" % bq_plan["plan"])
    pushed = []
    no_push = "--no-push" in sys.argv or os.environ.get("REVIEW_NO_PUSH") == "1"
    try:
        from push import send as push_send
        pushed = [] if no_push else push_send(title, "\n".join(body_lines),
                                              images=chart_imgs)
    except Exception as e:
        pushed = [{"error": "%s" % e}]

    result = {
        "notify": True,
        "title": title,
        "trade_date": today,
        "water": water,
        "rotation": rotation,
        "money_flow": money,
        "candidates": candidates,
        "candidate_count": len(candidates),
        "pullbacks": pullbacks,
        "pullback_count": len(pullbacks),
        "triggered": triggered,
        "triggered_count": len(triggered),
        "tracking": tracking,
        "tracking_count": len(tracking),
        "pool_size": len(symbols),
        "beiqi_plan": bq_plan,
        "report": md_path,
        "pushed": pushed,
    }
    # 落盘一份完整 JSON,供盘中扫描 intraday_scan.py 读取跟踪池
    try:
        with open(os.path.join(REVIEW_DIR, "%s.json" % today), "w") as f:
            json.dump(result, f, ensure_ascii=False)
    except OSError as e:
        print("json dump fail: %s" % e, flush=True)
    out(result)


if __name__ == "__main__":
    main()
