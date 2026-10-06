#!/usr/bin/env python3
"""情绪感知引擎:盘前市场情绪 + 早盘诱多/诱空识别。

数据源:
- 盘前: 55188 sec.php?action=overview (涨跌分布/涨跌停/连板梯队/热点/主力/成交额)
- 早盘: 5分钟K (9:30-10:00 开盘行为)

用法:
  python3 sentiment.py --premarket [--dry-run]   # 08:35 盘前情绪
  python3 sentiment.py --morning [--dry-run]     # 10:05 早盘诱多诱空 (需 symbols 文件或关注池)
  python3 sentiment.py --morning --symbols 600000,000001 [--dry-run]

输出: logs/sentiment_<date>_<session>.json
"""
import market_cache
import json
import os
import sys
import urllib.request
from datetime import datetime
from zoneinfo import ZoneInfo

BJ = ZoneInfo("Asia/Shanghai")
SVC = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, SVC)
UA = {"User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36",
      "Referer": "https://www.55188.com/sec.php"}


def get_overview():
    req = urllib.request.Request("https://www.55188.com/sec.php?action=overview", headers=UA)
    with market_cache.urlopen(req, timeout=20) as r:
        return json.loads(r.read().decode("utf-8", "ignore"))


def water_level():
    """P2 蓄水池水位:从历史盘前情绪文件累积成交额,看5日均值趋势"""
    import glob
    files = sorted(glob.glob(os.path.join(SVC, "logs", "sentiment_*_premarket.json")))[-6:]
    amts = []
    for f in files:
        try:
            m = json.load(open(f)).get("market", {})
            a = m.get("amount")
            if a:
                amts.append(a)
        except Exception:
            pass
    if len(amts) < 3:
        return {"direction": "未知", "days_declining": 0, "n": len(amts)}
    decl = 0
    for i in range(len(amts) - 1, 0, -1):
        if amts[i] < amts[i - 1]:
            decl += 1
        else:
            break
    direction = "下降" if decl >= 2 else ("上升" if amts[-1] > amts[0] * 1.05 else "持平")
    return {"direction": direction, "days_declining": decl,
            "latest": amts[-1], "avg5": round(sum(amts[-5:]) / min(len(amts), 5))}


def sector_rotation(hot_now):
    """P1 调仓换股:对比昨日 hot_sectors,输出新进/掉出板块"""
    import glob
    files = sorted(glob.glob(os.path.join(SVC, "logs", "sentiment_*_premarket.json")))
    prev = []
    if files:
        try:
            prev = [h["name"] for h in
                    json.load(open(files[-1])).get("market", {}).get("hot_sectors", [])]
        except Exception:
            pass
    now = [h["name"] for h in hot_now]
    return {"new_in": [x for x in now if x not in prev],
            "dropped_out": [x for x in prev if x not in now]}


def risk_appetite(max_board, limit_up):
    """P3 新股风向标:首板数(异动集涨停池)+最高连板 -> 风险偏好 + 趋势(对比昨日)"""
    import glob
    first_board = None
    cands = sorted(glob.glob(os.path.join(SVC, "logs", "yidong_*.json")), reverse=True)
    if cands:
        try:
            d = json.load(open(cands[0]))
            zt = [s for s in d["stocks"] if "涨停" in str(s.get("sources"))]
            first_board = sum(1 for s in zt if not s.get("m_days_n_boards"))
        except Exception:
            pass
    if first_board is None:
        return {"level": "未知", "first_board": None, "max_board": max_board}
    if first_board >= 20 and max_board >= 4:
        level = "高"
    elif first_board < 8 or max_board <= 2:
        level = "低"
    else:
        level = "中"
    # 趋势:对比昨日偏好
    trend = "持平"
    try:
        pfiles = sorted(glob.glob(os.path.join(SVC, "logs", "sentiment_*_premarket.json")))
        if pfiles:
            prev = json.load(open(pfiles[-1])).get("market", {}).get("risk_appetite", {}).get("level")
            order = {"低": 0, "中": 1, "高": 2}
            if prev in order and level in order:
                trend = "升温" if order[level] > order[prev] else ("降温" if order[level] < order[prev] else "持平")
    except Exception:
        pass
    return {"level": level, "trend": trend, "first_board": first_board, "max_board": max_board}


def market_sentiment(ov):
    """盘前市场情绪分 -100~+100"""
    o = ov.get("overview", {})
    up, down = o.get("up_count", 0), o.get("down_count", 0)
    lu, ld = o.get("limit_up_count", 0), o.get("limit_down_count", 0)
    broken = o.get("broken_count", 0)
    prev_ret = o.get("previous_limit_up_return") or 0  # 昨日涨停今日赚钱效应
    amt_chg = o.get("amount_change") or 0
    ladder = ov.get("limit_ladder", [])
    max_board = max([b.get("board", 0) for b in ladder], default=0)

    s = 0.0
    parts = {}
    if up + down > 0:
        parts["breadth"] = round((up - down) / (up + down) * 40, 1)
        s += parts["breadth"]
    parts["limit_diff"] = round(max(min((lu - ld) * 2, 20), -20), 1)
    s += parts["limit_diff"]
    parts["board"] = round(max(min((max_board - 3) * 5, 15), -15), 1)
    s += parts["board"]
    parts["money_effect"] = round(max(min(prev_ret * 3, 15), -15), 1)
    s += parts["money_effect"]
    parts["amount_mom"] = 10 if amt_chg > 0 else (-10 if amt_chg < 0 else 0)
    s += parts["amount_mom"]
    parts["broken"] = -min(broken * 0.5, 10)
    s += parts["broken"]

    s = round(max(min(s, 100), -100), 1)
    # P5 大跌装死日:down>4000 直接冰点
    dead_day = down > 4000
    if dead_day:
        s = min(s, -40)
    if s >= 40:
        label = "亢奋"
    elif s >= 15:
        label = "积极"
    elif s > -15:
        label = "中性"
    elif s > -40:
        label = "谨慎"
    else:
        label = "冰点"
    hot = [{"name": h.get("name"), "limit_up": h.get("limit_up_count"),
            "reason": (h.get("reason") or "")[:40]}
           for h in ov.get("hot_sectors", [])[:5]]
    return {
        "score": s, "label": label, "parts": parts,
        "up": up, "down": down, "limit_up": lu, "limit_down": ld,
        "max_board": max_board, "prev_limitup_ret": prev_ret,
        "amount": o.get("amount"),
        "hot_sectors": hot,
        "dead_day": dead_day,  # P5
        "as_of": ov.get("as_of"), "trade_date": ov.get("trade_date"),
    }


def morning_action_from_bars(code, bars, today_s=None):
    """早盘行为识别(纯逻辑,市场无关):返回 {trap: 诱多/诱空/无, strength, day_chg, accel_end, ...}
    用开盘后约30分钟的5分钟K。today_s 指定"今日"日期(默认北京时间今日,美股传美东日期)。"""
    if not bars or len(bars) < 4:
        return {"code": code, "error": "no_bars"}
    # 取今日开盘后的 bars(北京时间9:30后 / 美股美东9:30后)
    today = today_s or datetime.now(BJ).strftime("%Y-%m-%d")
    tb = [b for b in bars if b["time"][:10] == today]
    if len(tb) < 3:
        # 非交易时段/数据未到
        return {"code": code, "error": "morning_bars_not_ready", "n": len(tb)}
    prev_close = bars[0]["close"] if tb[0] != bars[0] else None
    # prev_close 取昨日收盘:找 tb 之前最后一根日线级别收盘(简化:用 tb[0] 的前一根5分钟收盘近似)
    idx0 = bars.index(tb[0])
    prev_close = bars[idx0 - 1]["close"] if idx0 > 0 else tb[0]["open"]
    o = tb[0]["open"]
    hi = max(b["high"] for b in tb)
    lo = min(b["low"] for b in tb)
    cl = tb[-1]["close"]
    open_chg = (o / prev_close - 1) * 100
    trap, strength = "无", "震荡"
    # 诱多:高开低走
    if open_chg >= 2.0 and cl < o:
        trap = "诱多"
    elif open_chg >= 2.0 and (hi / cl - 1) * 100 > 3.0:
        trap = "诱多"  # 冲高回落超3%
    # 诱空:低开收复
    if open_chg <= -1.5 and cl > o * 1.01:
        trap = "诱空"
    # 真强/真弱
    if trap == "无":
        if open_chg > 0 and lo >= o * 0.995:
            strength = "真强"  # 高开不回补
        elif open_chg > 0 and cl > o:
            strength = "真强"
        elif open_chg < 0 and hi < o:
            strength = "真弱"  # 低开反抽不过开盘价
    # P4 加速末端:T+1下大涨+放量=最后加速器
    day_chg = (cl / prev_close - 1) * 100
    accel_end = day_chg > 7.0
    # P8 早盘诱多节点:9:45/10:00/10:15/10:30/11:30是日内高点节点
    # 量价齐升:近3根5分钟K 量增价涨(漂亮的首日VWAP结构);盘中VWAP
    vol_up = False
    if len(tb) >= 3:
        v3 = tb[-3:]
        vol_up = all(b["volume"] >= a["volume"] for a, b in zip(v3, v3[1:])) \
            and all(b["close"] >= a["close"] for a, b in zip(v3, v3[1:]))
    vwap5 = None
    try:
        from vwap import vwap_of
        vwap5 = vwap_of(tb)
    except Exception:
        pass
    return {
        "code": code, "trap": trap, "strength": strength,
        "open_chg": round(open_chg, 2),
        "day_chg": round(day_chg, 2),
        "accel_end": accel_end,  # P4
        "m30_high_chg": round((hi / o - 1) * 100, 2),
        "m30_low_chg": round((lo / o - 1) * 100, 2),
        "m30_close_chg": round((cl / o - 1) * 100, 2),
        "price": cl,
        "day_high": round(hi, 2),
        "vwap_5min": round(vwap5, 2) if vwap5 else None,  # P8
        "vol_up": vol_up,  # P8 量价齐升
    }


def main():
    from trading_calendar import guard_trading_day
    guard_trading_day("sentiment")
    dry = "--dry-run" in sys.argv
    now = datetime.now(BJ)
    date_s = now.strftime("%Y-%m-%d")
    out = {}
    if "--premarket" in sys.argv:
        ov = get_overview()
        ms = market_sentiment(ov)
        ms["water"] = water_level()          # P2 蓄水池水位
        ms["rotation"] = sector_rotation(ms["hot_sectors"])  # P1 调仓换股
        ms["risk_appetite"] = risk_appetite(ms["max_board"], ms["limit_up"])  # P3 新股风向标
        out = {"session": "premarket", "date": date_s, "market": ms}
        p = os.path.join(SVC, "logs", "sentiment_%s_premarket.json" % date_s)
        json.dump(out, open(p, "w"), ensure_ascii=False, indent=1)
        print("盘前情绪: %s分 [%s]%s 涨停%d/跌停%d 最高%d板 昨日涨停今%s%.2f%%" % (
            ms["score"], ms["label"], " 💀装死日" if ms["dead_day"] else "",
            ms["limit_up"], ms["limit_down"],
            ms["max_board"], "赚" if ms["prev_limitup_ret"] >= 0 else "亏",
            abs(ms["prev_limitup_ret"])))
        print("热点:", " / ".join(h["name"] for h in ms["hot_sectors"]))
        print("水位: %s(连降%d日) 风险偏好: %s" % (
            ms["water"]["direction"], ms["water"]["days_declining"],
            ms["risk_appetite"]["level"]))
        if ms["rotation"]["new_in"] or ms["rotation"]["dropped_out"]:
            print("板块轮动: 新进 %s | 掉出 %s" % (
                ms["rotation"]["new_in"], ms["rotation"]["dropped_out"]))
        if not dry:
            from push import send as push_send
            title = "🌡 盘前情绪 %s分[%s]%s" % (
                ms["score"], ms["label"], " 💀" if ms["dead_day"] else "")
            body = "%s\n涨跌 %d/%d, 涨停 %d 跌停 %d, 最高 %d 板\n昨日涨停今日 %+.2f%%, 水位%s, 风险偏好%s\n热点: %s" % (
                title, ms["up"], ms["down"], ms["limit_up"], ms["limit_down"],
                ms["max_board"], ms["prev_limitup_ret"],
                ms["water"]["direction"], ms["risk_appetite"]["level"],
                "、".join(h["name"] for h in ms["hot_sectors"][:4]))
            if ms["rotation"]["new_in"]:
                body += "\n新进板块: %s" % "、".join(ms["rotation"]["new_in"][:3])
            push_send(title, body)
    elif "--morning" in sys.argv:
        syms = []
        if "--symbols" in sys.argv:
            i = sys.argv.index("--symbols")
            syms = sys.argv[i + 1].split(",")
        else:
            # 默认用今日关注池
            import glob
            cands = sorted(glob.glob(os.path.join(SVC, "logs", "watchpool_*.json")), reverse=True)
            name_of = {}
            if cands:
                d = json.load(open(cands[0]))
                syms = [s["symbol"] for s in d.get("pool", [])]
                name_of = {s["symbol"]: s.get("name", "") for s in d.get("pool", [])}
        res = []
        # 批量一次拉取全部5分钟K(最多30只/批)
        import daily_review as dr
        syms = [c.strip() for c in syms]
        api_codes = [("sh" if c[0] in "69" else "sz") + c for c in syms]
        bars_map = {}
        for i in range(0, len(api_codes), 30):
            chunk = api_codes[i:i + 30]
            try:
                d = dr.api("/api/v1/quotes/kline/batch?symbols=%s&period=5&limit=12" % ",".join(chunk),
                           timeout=30)["data"]
                for k, v in (d or {}).items():
                    bars_map[k.replace(".", "").upper()] = v  # 600000SH
                    bars_map[k.split(".")[0]] = v              # 600000
            except Exception as e:
                print("batch kline failed: %s" % str(e)[:60])
        for c in syms:
            bars = bars_map.get(c) or []
            try:
                res.append(morning_action_from_bars(c, bars))
            except Exception as e:
                res.append({"code": c, "error": str(e)[:60]})
        out = {"session": "morning", "date": date_s, "stocks": res}
        p = os.path.join(SVC, "logs", "sentiment_%s_morning.json" % date_s)
        json.dump(out, open(p, "w"), ensure_ascii=False, indent=1, default=str)
        traps = [r for r in res if r.get("trap") not in (None, "无")]
        accel = [r for r in res if r.get("accel_end")]
        print("早盘行为: %d只, 诱多/诱空 %d只, 加速末端 %d只" % (len(res), len(traps), len(accel)))
        for r in traps[:10]:
            print("  %s %s %s 开盘%+.1f%%" % (r["code"], name_of.get(r["code"], ""), r["trap"], r.get("open_chg", 0)))
        for r in accel[:10]:
            print("  %s %s 🚀加速末端 当日%+.1f%%" % (r["code"], name_of.get(r["code"], ""), r.get("day_chg", 0)))
        if not dry and (traps or accel):
            from push import send as push_send
            title = "⚠️ 早盘识别 诱多诱空%d只 加速末端%d只" % (len(traps), len(accel))
            # 推送最多10只防刷屏: 诱多/诱空优先, 加速末端补位; 全量明细在日志文件
            trap_lines = ["%s %s %s 开盘%+.1f%% 30分钟%+.1f%%" % (
                r["code"], name_of.get(r["code"], ""), r["trap"], r.get("open_chg", 0), r.get("m30_close_chg", 0))
                for r in traps[:10]]
            accel_lines = ["%s %s 🚀加速末端 当日%+.1f%% 不追" % (r["code"], name_of.get(r["code"], ""), r.get("day_chg", 0))
                           for r in accel[:10 - len(trap_lines)]]
            push_send(title, "\n".join(trap_lines + accel_lines))
    else:
        print("usage: sentiment.py --premarket|--morning [--symbols ..] [--dry-run]")


if __name__ == "__main__":
    main()
