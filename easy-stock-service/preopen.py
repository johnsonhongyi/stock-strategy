#!/usr/bin/env python3
"""预开仓计划:盘前 09:00 跑,结合 T+1 + VWAP 生成开仓清单。

对齐 TK stock_live_strategy / bidding_momentum_detector 的核心件:
- 结构分:收盘>MA20/MA60、昨日异动形态(低开高走/高开高走/强势维持)、3连阳
- 龙虎榜分:昨日上榜、机构净买入
- 否决:60日涨幅>=80%(高位不接盘)

输入: 观察池重点股(watchpool Top30) + 数据底座龙虎榜 + 日K批量
输出: logs/preopen_YYYY-MM-DD.json {candidates:[{code,name,pre_score,reason,plan}]} 
竞价阶段由 auction_scan.py 做 TK 动量确认(成交额分档/高开区间/开盘即最低),
确认后由 paper_trade.py --preopen 在 09:27 模拟买入(竞价开盘价,T+1锁定)。

用法: python3 preopen.py [--dry-run]
"""
import json
import os
import sys
from datetime import datetime
from zoneinfo import ZoneInfo

BJ = ZoneInfo("Asia/Shanghai")
SVC = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, SVC)
import daily_review as dr  # noqa: E402
import realpos  # noqa: E402

MIN_PRE_SCORE = 4   # 预开仓入选最低分
HIGH_60D = 80.0     # 60日涨幅超80%直接否决


def to_api(sym):
    s = dr.norm_sym(sym)
    return ("sh" if s[0] in "69" else "sz") + s


def load_watchpool_top(date, n=30):
    """观察池重点股:取当日 watchpool pool 前N(按score排序)。"""
    p = os.path.join(SVC, "logs", "watchpool_%s.json" % date)
    if not os.path.exists(p):
        return []
    try:
        d = json.load(open(p))
        pool = d.get("pool") or d.get("top30") or []
        pool = sorted(pool, key=lambda x: x.get("score", 0), reverse=True)
        return pool[:n]
    except Exception:
        return []


def load_billboard():
    """昨日龙虎榜:code -> {net_amount, institution_buyers, reason}。"""
    try:
        d = dr.api("/api/v1/market/billboard?limit=80", timeout=25)
        items = d.get("data") if isinstance(d, dict) else d
        out = {}
        for it in items or []:
            code = dr.norm_sym(it.get("symbol", ""))
            if code:
                out[code] = {
                    "net": it.get("net_amount") or 0,
                    "inst_buy": it.get("institution_buyers") or 0,
                    "reason": it.get("reason", ""),
                    "chg": it.get("change_percent"),
                }
        return out
    except Exception as e:
        print("billboard failed: %s" % str(e)[:80])
        return {}


def batch_daily(codes):
    """批量日K:code -> bars(含 previous_close)。"""
    api_syms = [to_api(c) for c in codes]
    out = {}
    for i in range(0, len(api_syms), 15):
        try:
            data = dr.api("/api/v1/quotes/kline/batch?symbols=%s&period=day"
                          "&limit=65" % ",".join(api_syms[i:i + 15]),
                          timeout=45)["data"]
            for sym, bars in (data or {}).items():
                c = dr.norm_sym(sym)
                if c and bars:
                    out[c] = bars
        except Exception as e:
            print("batch daily failed: %s" % str(e)[:60])
    return out


def ma(bars, n):
    if len(bars) < n:
        return None
    return sum(b["close"] for b in bars[-n:]) / n


def anomaly_pattern(bars):
    """TK _has_anomaly_pattern 对齐:昨日K线异动形态。返回 (形态名, 加分)。"""
    if len(bars) < 2:
        return None
    b = bars[-1]
    pc = b.get("previous_close") or bars[-2]["close"]
    o, h, c = b["open"], b["high"], b["close"]
    chg = (c - pc) / pc * 100
    high_chg = (h - pc) / pc * 100
    # 低开高走
    if o < pc * 0.995 and c > o and chg >= 1.0:
        return ("低开高走", 3)
    # 高开高走
    if o > pc * 1.01 and c > h * 0.98 and chg >= 2.0:
        return ("高开高走", 3)
    # 强势维持
    if high_chg >= 3.0 and c > h * 0.97 and chg >= 1.0:
        return ("强势维持", 3)
    return None


def up_streak(bars, n=3):
    """连阳n天。"""
    if len(bars) < n + 1:
        return False
    return all(bars[-i]["close"] > bars[-i - 1]["close"] for i in range(1, n + 1))


def chg60(bars):
    if len(bars) >= 61 and bars[-61].get("close"):
        return (bars[-1]["close"] - bars[-61]["close"]) / bars[-61]["close"] * 100
    return None


def yesterday_vwap(bars):
    """昨日VWAP = amount/Σ(volume*100),作为竞价 VWAP 门禁锚点。"""
    b = bars[-1]
    vol = (b.get("volume") or 0) * 100
    amt = b.get("amount") or 0
    return amt / vol if vol and amt else None


def build_plan(date, dry=False):
    top = load_watchpool_top(date)
    if not top:
        print("no watchpool top30 for %s" % date)
        return {"date": date, "candidates": [], "reason": "no_pool"}
    codes = [dr.norm_sym(t.get("code") or t.get("symbol", "")) for t in top]
    codes = [c for c in codes if c]
    names = {dr.norm_sym(t.get("code") or t.get("symbol", "")): t.get("name", "")
             for t in top}
    lhb = load_billboard()
    kl = batch_daily(codes)

    cands = []
    for code in codes:
        bars = kl.get(code, [])
        if len(bars) < 25:
            continue
        score, reasons = 0, []
        c60 = chg60(bars)
        if c60 is not None and c60 >= HIGH_60D:
            continue  # 高位直接否决,不接盘
        last = bars[-1]["close"]
        m20, m60 = ma(bars, 20), ma(bars, 60)
        if m20 and last > m20:
            score += 1
            reasons.append("站上MA20")
        if m60 and last > m60:
            score += 1
            reasons.append("站上MA60")
        ap = anomaly_pattern(bars)
        if ap:
            score += ap[1]
            reasons.append("昨日" + ap[0])
        if up_streak(bars, 3):
            score += 2
            reasons.append("3连阳")
        #  setup类型:回踩(站上MA20+昨日低点触及MA20) vs 反弹(MA20下方) vs 其他
        #  用户北汽蓝谷复盘:被套持仓走不出通道趋势回踩,反弹型信号要降权标识
        setup = "其他"
        if m20:
            if last > m20 and bars[-1]["low"] <= m20 * 1.03:
                setup = "回踩MA20"
                score += 2
                reasons.append("回踩MA20企稳")
            elif last < m20:
                setup = "反弹(MA20下方)"
        lb = lhb.get(code)
        if lb:
            score += 2
            reasons.append("昨日龙虎榜")
            if (lb.get("net") or 0) > 0:
                score += 2
                reasons.append("机构净买入+")
        if score < MIN_PRE_SCORE:
            continue
        yvwap = yesterday_vwap(bars)
        pos = realpos.get(code)
        cands.append({
            "code": code, "name": names.get(code, ""),
            "pre_score": score, "reasons": reasons,
            "setup": setup,
            # 持仓股:信号=做T/加仓观察,非新开仓
            "held": bool(pos),
            "held_cost": pos["cost"] if pos else None,
            "prev_close": round(bars[-1].get("previous_close") or bars[-2]["close"], 2),
            "yesterday_vwap": round(yvwap, 2) if yvwap else None,
            "chg60": round(c60, 1) if c60 is not None else None,
            # T+1:买入日T,可卖日T+1(交易日,节假日顺延由 paper 端按交易日历算)
            "plan": "竞价确认后买入,次交易日可卖" if not pos else "持仓股:做T/加仓观察,非新开仓",
        })
    cands.sort(key=lambda x: -x["pre_score"])
    out = {"date": date, "as_of": datetime.now(BJ).strftime("%Y-%m-%d %H:%M:%S"),
           "strategy_version": "v1.1",
           "candidates": cands,
           "billboard_ok": bool(lhb)}
    if not dry:
        p = os.path.join(SVC, "logs", "preopen_%s.json" % date)
        json.dump(out, open(p, "w"), ensure_ascii=False, indent=1)
    print("预开仓候选 %d 只(观察池%d只,龙虎榜%s)" % (
        len(cands), len(codes), "ok" if lhb else "失败"))
    for c in cands[:10]:
        print("  %s %s %d分 %s" % (c["code"], c["name"], c["pre_score"],
                                   "/".join(c["reasons"])))
    return out


def main():
    from trading_calendar import guard_trading_day
    guard_trading_day("preopen")
    dry = "--dry-run" in sys.argv
    today = datetime.now(BJ).strftime("%Y-%m-%d")
    build_plan(today, dry=dry)


if __name__ == "__main__":
    main()
