#!/usr/bin/env python3
"""P11 两日高低网格做T(用户2026-09-29口述,北汽蓝谷实盘)。

规则:
  网格基准 = 最近2个完整交易日的 最高价H2 / 最低价L2(每日收盘后滚动更新)
  卖出挂单区 = [H2×1.005, H2×1.01]   # 冲高到此区,卖出可卖份额,做T降成本
  买入挂单区 = [L2×1.01,  L2×1.02]    # 回踩到此区,买回(等额)
  趋势走坏 → 一键清仓,不再做T:
    - 空头排列(1<3<5<10日VWAP),或
    - 价格跌破10日VWAP×0.99(盘中)/收盘跌破10日线(盘后)
  T+1约束:卖出只能卖可卖份额(昨日及以前买入);买回的份额次日才可卖。

纯函数,无IO(调用方传K线进来)。
"""

SELL_LO = 1.005
SELL_HI = 1.01
BUY_LO = 1.01
BUY_HI = 1.02
TREND_BRK_VWAP = 0.99


def levels_from_daily(daily_bars, today_str):
    """最近2个完整交易日的高低点 -> 网格挂单区。"""
    done = [k for k in daily_bars if k["time"][:10] < today_str]
    if len(done) < 2:
        return None
    last2 = done[-2:]
    h2 = max(k["high"] for k in last2)
    l2 = min(k["low"] for k in last2)
    h2d = max(last2, key=lambda k: k["high"])["time"][:10]
    return {
        "h2": round(h2, 3), "l2": round(l2, 3), "h2_date": h2d,
        "sell_lo": round(h2 * SELL_LO, 3), "sell_hi": round(h2 * SELL_HI, 3),
        "buy_lo": round(l2 * BUY_LO, 3), "buy_hi": round(l2 * BUY_HI, 3),
    }


def trend_broken(price, st):
    """盘中趋势走坏判断(st为vwap.analyze结果)。返回 reason 列表,空=趋势未坏。"""
    reasons = []
    if st.get("alignment") == "bear":
        reasons.append("空头排列")
    d10 = (st.get("vwap") or {}).get("d10")
    if d10 and price < d10 * TREND_BRK_VWAP:
        reasons.append("跌破10日VWAP")
    if st.get("flow") == "down" and st.get("alignment") == "bear":
        reasons.append("四线同下+空头")
    # 去重保序
    seen = []
    for r in reasons:
        if r not in seen:
            seen.append(r)
    return seen


def trend_broken_daily(daily_bars, price, today_str):
    """盘后/模拟盘用:收盘价跌破10日收盘均线×0.99 -> 趋势走坏。"""
    done = [k for k in daily_bars if k["time"][:10] <= today_str]
    closes = [k["close"] for k in done[-10:]]
    if len(closes) < 10:
        return []
    ma10 = sum(closes) / 10
    return ["跌破10日线"] if price < ma10 * TREND_BRK_VWAP else []


def zone_of(price, gl):
    """价格落在哪个网格区: sell / buy / mid / None(无网格)。"""
    if not gl:
        return None
    if gl["sell_lo"] <= price <= gl["sell_hi"]:
        return "sell"
    if gl["buy_lo"] <= price <= gl["buy_hi"]:
        return "buy"
    return "mid"


# ============ P12 资金轮动:统一强度分(2026-09-29用户口述) ============
# 资金不闲置,该卖的卖,强势的跟(打码),仓位轮动起来。
# 强度分(持仓股统一标尺):
#   站上当日VWAP +2 | 5日涨幅>5% +2 / >0 +1 / <-5% -2
#   价格到网格卖出区 -1(该T了) / 到买入区 +1
#   疑似破位(marked) -2(标记≠卖出,轮动卖弱阈值-3,标记 alone 不触发)
ROT_WEAK_TH = -3     # 强度分<=-3 -> 卖弱轮动
ROT_STRONG_TH = 4    # 强度分>=4 -> 强者打码


def position_strength(price, sess_vwap, daily_bars, today_str, gl, bd_state):
    """返回 (score, tags)。纯函数。"""
    s, tags = 0, []
    if sess_vwap:
        if price >= sess_vwap:
            s += 2
            tags.append("站上VWAP")
        else:
            tags.append("VWAP下方")
    done = [b for b in daily_bars if b["time"][:10] < today_str]
    if len(done) >= 6:
        mom5 = done[-1]["close"] / done[-6]["close"] - 1
        if mom5 > 0.05:
            s += 2
            tags.append("5日+%.0f%%" % (mom5 * 100))
        elif mom5 > 0:
            s += 1
            tags.append("5日+%.0f%%" % (mom5 * 100))
        elif mom5 < -0.05:
            s -= 2
            tags.append("5日%.0f%%" % (mom5 * 100))
    zone = zone_of(price, gl)
    if zone == "sell":
        s -= 1
        tags.append("到卖出区")
    elif zone == "buy":
        s += 1
        tags.append("到买入区")
    if (bd_state or {}).get("phase") == "marked":
        s -= 2
        tags.append("疑似破位")
    return s, tags


# ============ P11 破位三段式(2026-09-29用户口述) ============
# 走坏不是一跌破就砍(那是震仓),而是:
#   标记:跌破当日VWAP×0.995 -> 疑似破位,先标记不卖(防震仓)
#   解除:快速收复VWAP -> 假破位/震仓,解除标记继续做T
#   确认:从低点反弹≥1%但高点仍在VWAP×0.998下方,且反弹量能<下破段×0.8
#         -> 真破位,在反弹那一笔VWAP下方卖出,不等主杀
# 状态机: normal -> marked -> confirmed,跨轮持久化在调用方state里。
BREAK_PCT = 0.995    # 跌破线
RECLAIM_PCT = 1.0    # 收复线
BOUNCE_MIN = 0.01    # 反弹至少1%才有确认意义
BOUNCE_VWAP_CAP = 0.998  # 反弹高点仍在VWAP下方
VOL_RATIO_CAP = 0.8  # 反弹均量 < 下破段均量×0.8 视为量能衰竭


def _leg_vol_ratio(min5, vwap_d1):
    """反弹段均量 / 下破段均量。None=无法判断(数据不足或脏数据)。"""
    closes = [b["close"] for b in min5]
    vols = [b.get("volume") or 0 for b in min5]
    if len(closes) < 6 or any(v <= 0 for v in vols):
        return None
    # 脏数据 guard:单根量超过中位数50倍视为单位突变
    import statistics
    med = statistics.median(vols)
    if med > 0 and max(vols) / med > 50:
        return None
    lo = min(closes)
    li = max(i for i, c in enumerate(closes) if c == lo)
    bi = next((i for i, c in enumerate(closes) if c < vwap_d1 * BREAK_PCT), 0)
    break_vols = vols[bi:li + 1]
    bounce_vols = vols[li + 1:]
    if not break_vols or not bounce_vols:
        return None
    bv = sum(break_vols) / len(break_vols)
    if bv <= 0:
        return None
    return (sum(bounce_vols) / len(bounce_vols)) / bv


def breakdown_update(min5, vwap_d1, state):
    """破位三段式状态机。返回 (event, new_state)。
    event: None | "mark"(疑似破位) | "unmark"(假破位/震仓解除) | "confirm"(确认破位)。"""
    state = dict(state or {})
    phase = state.get("phase", "normal")
    if not min5 or not vwap_d1:
        return None, state
    price = min5[-1]["close"]
    if phase == "normal":
        if price < vwap_d1 * BREAK_PCT:
            return "mark", {"phase": "marked", "mark_px": round(price, 3),
                            "low_px": round(price, 3),
                            "bounce_high": round(price, 3),
                            "mark_vwap": round(vwap_d1, 3)}
        return None, state
    if phase == "marked":
        s = dict(state)
        s["low_px"] = round(min(s.get("low_px", price), price), 3)
        s["bounce_high"] = round(max(s.get("bounce_high", price), price), 3)
        if price >= vwap_d1 * RECLAIM_PCT:
            return "unmark", {"phase": "normal"}  # 震仓:假破位
        bounce = (s["bounce_high"] - s["low_px"]) / s["low_px"] if s["low_px"] else 0
        if bounce >= BOUNCE_MIN and s["bounce_high"] < vwap_d1 * BOUNCE_VWAP_CAP:
            ratio = _leg_vol_ratio(min5, vwap_d1)
            if ratio is not None and ratio < VOL_RATIO_CAP:
                s["phase"] = "confirmed"
                s["vol_ratio"] = round(ratio, 2)
                s["bounce_pct"] = round(bounce * 100, 2)
                return "confirm", s
        return None, s
    if phase == "confirmed":
        if price >= vwap_d1 * RECLAIM_PCT:
            return "unmark", {"phase": "normal"}
        return None, state
    return None, state
