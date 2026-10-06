#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""盘中大盘情绪快照:交易时段每30分钟独立推送 + 逐条落盘供自学习迭代。

数据:55188 overview(涨跌家数/涨跌停/连板梯队/热点/成交额)+指数实时(上证/深证/创业板)。
评分复用 sentiment.py 的盘前公式,口径一致,便于复盘对比"盘前预期 vs 盘中实际"。
落盘:logs/sentiment_intraday_<date>.jsonl(每行一条快照),周复盘读取整天轨迹,
     审判盘前情绪判断对错,迭代情绪模型(这是自学习的核心训练数据)。
时间门控:09:30-11:30 / 13:00-15:05,其余静默退出。
用法:python3 sentiment_intraday.py [--dry-run]
"""
import market_cache
import json
import os
import sys
import urllib.request
from datetime import datetime, timezone, timedelta

SVC = os.path.dirname(os.path.abspath(__file__))
BJ = timezone(timedelta(hours=8))
UA = {"User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64)"}


def get_overview():
    req = urllib.request.Request(
        "https://www.55188.com/sec.php?action=overview", headers=UA)
    with market_cache.urlopen(req, timeout=20) as r:
        return json.loads(r.read().decode("utf-8", "ignore"))


def get_indexes():
    """上证/深证/创业板实时涨跌幅,走后端(失败返回空,不阻塞)。"""
    try:
        from review_charts import api as bapi
        d = bapi("/api/v1/quotes/realtime?symbols=sh000001,sz399001,sz399006",
                 timeout=15).get("data", [])
        out = {}
        items = d.items() if isinstance(d, dict) else d
        for v in items:
            tail = (v.get("symbol") or "")[:6]
            nm = {"000001": "上证", "399001": "深证", "399006": "创业板"}.get(tail)
            if nm:
                out[nm] = round(v.get("change_percent") or 0, 2)
        return out
    except Exception as e:
        print("index quote fail: %s" % e, flush=True)
        return {}


def score_from_overview(ov):
    """与 sentiment.py 盘前公式同口径的情绪分。"""
    o = ov.get("overview", {})
    up, down = o.get("up_count", 0), o.get("down_count", 0)
    lu, ld = o.get("limit_up_count", 0), o.get("limit_down_count", 0)
    broken = o.get("broken_count", 0)
    prev_ret = o.get("previous_limit_up_return") or 0
    amt_chg = o.get("amount_change") or 0
    ladder = ov.get("limit_ladder", [])
    max_board = max([b.get("board", 0) for b in ladder], default=0)
    s, parts = 0.0, {}
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
    return {"score": s, "label": label, "parts": parts, "dead_day": dead_day,
            "up": up, "down": down, "limit_up": lu, "limit_down": ld,
            "max_board": max_board, "broken": broken,
            "amount": o.get("amount"), "prev_limitup_ret": prev_ret}


def load_premarket(date):
    """读盘前情绪分+大环境门禁,做预期对照。"""
    out = {}
    pf = os.path.join(SVC, "logs", "sentiment_%s_premarket.json" % date)
    if os.path.exists(pf):
        try:
            m = json.load(open(pf)).get("market", {})
            out["pre_score"] = m.get("score")
            out["pre_label"] = m.get("label")
        except Exception:
            pass
    rf = os.path.join(SVC, "logs", "market_regime_%s.json" % date)
    if os.path.exists(rf):
        try:
            r = json.load(open(rf))
            out["pre_gate"] = r.get("门禁")
            out["pre_regime_score"] = r.get("情绪")
        except Exception:
            pass
    return out


def load_prev_snapshots(date):
    path = os.path.join(SVC, "logs", "sentiment_intraday_%s.jsonl" % date)
    snaps = []
    if os.path.exists(path):
        for line in open(path):
            line = line.strip()
            if line:
                try:
                    snaps.append(json.loads(line))
                except Exception:
                    pass
    return snaps


def trend_analysis(snaps, score):
    """日内情绪轨迹感知:开盘分→现分方向 + 连续同向次数 + 阶段预警。
    解决"只跑分不感知趋势":如43→41连续两次下滑,推送必须报"走弱"而非静态"亢奋"。"""
    if not snaps:
        return None
    scores = [s["score"] for s in snaps if s.get("score") is not None]
    if not scores:
        return None
    day_open = scores[0]
    cur_delta = round(score - scores[-1], 1)
    sign = (cur_delta > 0) - (cur_delta < 0)
    deltas = [s.get("delta_30m") for s in snaps[1:]] + [cur_delta]
    streak = 1
    for d in reversed(deltas[:-1]):
        ds = ((d or 0) > 0) - ((d or 0) < 0)
        if sign == 0 or ds != sign:
            break
        streak += 1
    move = score - day_open
    if move >= 5:
        direction = "日内走强"
    elif move <= -5:
        direction = "日内走弱"
    elif abs(cur_delta) < 1 and streak < 2:
        direction = "横盘钝化"
    else:
        direction = "小幅波动"
    warn = ""
    if score >= 30 and sign < 0 and streak >= 2:
        warn = "亢奋区退潮,警惕跳水"
    elif score <= -15 and sign > 0 and streak >= 2:
        warn = "冰点修复中"
    elif score >= 30 and direction == "横盘钝化":
        warn = "高位钝化,方向待选"
    return {"day_open": round(day_open, 1), "move": round(move, 1),
            "direction": direction, "streak": streak if sign != 0 else 0,
            "streak_dir": "走强" if sign > 0 else ("走弱" if sign < 0 else ""),
            "warn": warn}


def trend_line(t):
    if not t:
        return None
    s = "轨迹:开盘%.0f→现%.0f %s" % (t["day_open"],
                                     t["day_open"] + t["move"], t["direction"])
    if t["streak"] >= 2:
        s += "(连续%d次%s)" % (t["streak"], t["streak_dir"])
    if t["warn"]:
        s += " ⚠️" + t["warn"]
    return s


def in_window(now):
    t = now.strftime("%H:%M")
    return ("09:30" <= t <= "11:30") or ("13:00" <= t <= "15:05")


def main():
    from trading_calendar import guard_trading_day
    guard_trading_day("sentiment_intraday")
    dry = "--dry-run" in sys.argv
    now = datetime.now(BJ)
    today = now.strftime("%Y-%m-%d")
    if not in_window(now) and not dry:
        print("intraday sentiment: 非交易时段,静默")
        return
    if now.weekday() >= 5 and not dry:
        print("intraday sentiment: 周末,静默")
        return

    ov = get_overview()
    sc = score_from_overview(ov)
    idx = get_indexes()
    pre = load_premarket(today)
    prev_snaps = load_prev_snapshots(today)
    prev = prev_snaps[-1] if prev_snaps else None

    snap = {
        "ts": now.strftime("%Y-%m-%d %H:%M:%S"),
        "trade_date": ov.get("trade_date") or today,
        "score": sc["score"], "label": sc["label"], "parts": sc["parts"],
        "dead_day": sc["dead_day"],
        "up": sc["up"], "down": sc["down"],
        "limit_up": sc["limit_up"], "limit_down": sc["limit_down"],
        "max_board": sc["max_board"], "broken": sc["broken"],
        "amount": sc["amount"],
        "indexes": idx,
        "hot_sectors": [{"name": h.get("name"),
                         "limit_up": h.get("limit_up_count")}
                        for h in ov.get("hot_sectors", [])[:5]],
        "vs_premarket": pre,
        "delta_30m": round(sc["score"] - prev["score"], 1) if prev else None,
        "trend": trend_analysis(prev_snaps, sc["score"]),
    }
    # 落盘:自学习训练数据(append-only)
    path = os.path.join(SVC, "logs", "sentiment_intraday_%s.jsonl" % today)
    if not dry:
        with open(path, "a") as f:
            f.write(json.dumps(snap, ensure_ascii=False) + "\n")
    print("intraday sentiment %s 分数%.1f(%s) 涨跌%d/%d 涨停%d跌停%d 最高%d板" % (
        snap["ts"][11:16], sc["score"], sc["label"],
        sc["up"], sc["down"], sc["limit_up"], sc["limit_down"], sc["max_board"]))

    if dry:
        return
    # 独立推送:紧凑版,情绪分+趋势+关键水位
    from push import send as push_send
    arrow = ""
    if snap["delta_30m"] is not None:
        arrow = " %s%.1f" % ("🔴+" if snap["delta_30m"] > 0 else "🟢",
                             snap["delta_30m"])
    emo = "🔴" if sc["score"] >= 15 else ("🟢" if sc["score"] < -15 else "🟡")
    title = "%s盘中情绪%s|%.0f分%s" % (
        emo, snap["ts"][11:16], sc["score"], arrow)
    lines = [
        "情绪:%.0f分(%s)%s" % (sc["score"], sc["label"], arrow),
    ]
    tl = trend_line(snap["trend"])
    if tl:
        lines.append(tl)
    lines += [
        "指数:" + " ".join("%s%+.2f%%" % (k, v) for k, v in idx.items()) or "指数:暂无",
        "涨跌:%d/%d 涨停%d 跌停%d 炸板%d 最高%d板" % (
            sc["up"], sc["down"], sc["limit_up"], sc["limit_down"],
            sc["broken"], sc["max_board"]),
    ]
    if pre.get("pre_score") is not None:
        d = sc["score"] - pre["pre_score"]
        lines.append("盘前预期:%.0f分 → 实际偏差%+.0f" % (pre["pre_score"], d))
    if pre.get("pre_gate"):
        lines.append("盘前门禁:%s" % pre["pre_gate"])
    hot = "、".join(h["name"] for h in snap["hot_sectors"][:3] if h.get("name"))
    if hot:
        lines.append("热点:%s" % hot)
    try:
        push_send(title, "\n".join(lines))
    except Exception as e:
        print("push fail: %s" % e, flush=True)


if __name__ == "__main__":
    main()
