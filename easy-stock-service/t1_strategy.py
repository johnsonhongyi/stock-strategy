#!/usr/bin/env python3
"""T+1 交易策略引擎.

A股 T+1 约束:今日买入明日才能卖,今日卖出的是昨日及以前的仓位。
因此每天只有一个好买点(给明天的卖留空间)和一个好卖点(处理昨天的仓位),
外加一个隔夜决策(防次日高开/低开下杀打掉止损或错过止盈)。

组件:
  regime()      走势定式识别(含"两日重心小幅下移+不碰10日"=强势整理)
  buy_point()   盘中买点:多头结构下回踩1日VWAP
  sell_point()  盘中卖点:偏离过大止盈 / 跌破1日VWAP止损
  gap_plan()    高开/低开的开盘处理计划
  overnight()   收盘前隔夜决策:持有过夜 / 尾盘减仓 / 清仓 + 动态止损线

全部基于 VWAP 结构,不预测点位,只给纪律。
"""
from __future__ import annotations

BUY_TOUCH = 0.008    # 买点:价格贴近1日VWAP的容差
TP_DEV = 0.025       # 止盈:偏离1日VWAP
WARN_DEV = 0.03      # 预警线(与 vwap 引擎一致)
GAP_UP = 0.02        # 高开阈值
GAP_DOWN = -0.015    # 低开阈值


def daily_vwap(bar) -> float | None:
    from vwap import vwap_of  # 与引擎同口径: 有 amount 精确, 无则典型价估算
    return vwap_of([bar])


def regime(daily_bars, struct: dict, today: str,
           session_vwap: float | None = None) -> dict:
    """识别当前走势定式.session_vwap 传入则把今日盘中VWAP作为最新一点."""
    hist = [b for b in (daily_bars or []) if (b.get("time") or "")[:10] < today]
    vwaps = [daily_vwap(b) for b in hist[-3:]]
    vwaps = [x for x in vwaps if x]
    if session_vwap:
        vwaps = (vwaps + [session_vwap])[-3:]
    price = struct.get("price")
    d10 = (struct.get("vwap") or {}).get("d10")
    out = {"name": "震荡", "desc": "", "bullish": None}
    if len(vwaps) >= 3 and price and d10:
        # 近两日重心:昨日单日VWAP相对前日
        drift1 = (vwaps[-1] - vwaps[-2]) / vwaps[-2]
        d10_slope = (struct.get("slope") or {}).get("d10")
        if vwaps[-1] < vwaps[-2] and drift1 > -0.015 \
                and price > d10 and d10_slope == "up":
            out.update({
                "name": "强势整理",
                "desc": f"1日VWAP重心下移{round(-drift1*100,2)}%但幅度不大,"
                        f"未回踩10日VWAP({d10})",
                "bullish": True,
            })
        elif struct.get("alignment") == "bull" and struct.get("flow") == "up":
            out.update({"name": "多头趋势", "desc": "多头排列+四线同上",
                        "bullish": True})
        elif struct.get("alignment") == "bear" and struct.get("flow") == "down":
            out.update({"name": "空头趋势", "desc": "空头排列+四线同下",
                        "bullish": False})
    return out


def buy_point(price: float, struct: dict) -> dict:
    """判断当前是否为今日好买点.返回 {hit, price_zone, stop, reason}."""
    v = struct.get("vwap") or {}
    v1 = v.get("d1")
    res = {"hit": False, "price_zone": None, "stop": None, "reason": ""}
    if not price or not v1:
        res["reason"] = "数据不足"
        return res
    dev = (price - v1) / v1
    slope1 = (struct.get("slope") or {}).get("d1")
    if struct.get("alignment") == "bear":
        res["reason"] = "空头排列,今日无买点"
        return res
    if struct.get("flow") == "down":
        res["reason"] = "VWAP四线同下,今日不买"
        return res
    if dev > 0.01:
        res["reason"] = f"偏离1日VWAP+{round(dev*100,1)}%,今日买点已错过,不追高"
        return res
    if abs(dev) <= BUY_TOUCH and slope1 in ("up", "flat"):
        stop = v.get("d3")
        res.update({
            "hit": True,
            "price_zone": round(price, 2),
            "stop": round(stop * 0.995, 2) if stop else None,
            "reason": f"回踩1日VWAP({v1})±0.8%,1日线斜率{slope1},"
                      f"结构{struct.get('alignment')}",
        })
    else:
        res["reason"] = f"等待回踩1日VWAP({v1}),当前偏离{round(dev*100,1)}%"
    return res


def sell_point(price: float, struct: dict) -> dict:
    """判断当前是否为今日好卖点(卖的是昨日及以前的仓位)."""
    v = struct.get("vwap") or {}
    v1 = v.get("d1")
    res = {"hit": False, "kind": None, "price_zone": None, "reason": ""}
    if not price or not v1:
        return res
    dev = (price - v1) / v1
    slope1 = (struct.get("slope") or {}).get("d1")
    if dev >= TP_DEV:
        res.update({"hit": True, "kind": "take_profit",
                    "price_zone": round(price, 2),
                    "reason": f"偏离1日VWAP+{round(dev*100,1)}%,止盈卖点"})
    elif price < v1 and slope1 == "down":
        res.update({"hit": True, "kind": "stop",
                    "price_zone": round(price, 2),
                    "reason": f"跌破1日VWAP({v1})且斜率转下,止损卖点"})
    else:
        res["reason"] = f"持有:偏离{round(dev*100,1)}%,1日斜率{slope1}"
    return res


def gap_plan(open_price: float, prev_close: float) -> dict:
    """开盘缺口处理计划."""
    if not open_price or not prev_close:
        return {"kind": "none", "plan": ""}
    g = (open_price - prev_close) / prev_close
    if g >= GAP_UP:
        return {"kind": "gap_up",
                "plan": f"高开+{round(g*100,1)}%,高开常是全天高点,"
                        f"止盈卖点前移到开盘30分钟内,不追高"}
    if g <= GAP_DOWN:
        return {"kind": "gap_down",
                "plan": f"低开{round(g*100,1)}%,不立即止损,等开盘15分钟确认:"
                        f"收复1日VWAP则为假下杀持有,反之为真下杀再卖"}
    return {"kind": "none", "plan": "平开,按盘中VWAP结构走"}


def overnight(price: float, struct: dict, daily_bars, today: str,
             session_vwap: float | None = None) -> dict:
    """收盘前隔夜决策:防次日高开/低开下杀."""
    v = struct.get("vwap") or {}
    v1, v3, v5 = v.get("d1"), v.get("d3"), v.get("d5")
    rg = regime(daily_bars, struct, today, session_vwap)
    stop_line = None
    if v3:
        stop_line = round(min(x for x in (v3, v5) if x) * 0.995, 2)
    target = round(v1 * 1.03, 2) if v1 else None

    align, flow = struct.get("alignment"), struct.get("flow")
    if price and v1 and price > v1 and align == "bull" and flow == "up":
        return {"bias": "次日高开概率大", "action": "持有过夜",
                "detail": "收盘站上1日VWAP+多头排列+四线同上",
                "stop_line": stop_line, "target": target, "regime": rg}
    if price and v1 and price < v1 and (flow == "down" or align == "bear"):
        return {"bias": "次日低开下杀概率大", "action": "尾盘减仓或清仓",
                "detail": "收盘跌破1日VWAP且结构转弱,不抱仓过夜",
                "stop_line": stop_line, "target": target, "regime": rg}
    if rg["name"] == "强势整理":
        return {"bias": "中性偏多", "action": "持有过夜,止损放10日VWAP",
                "detail": rg["desc"],
                "stop_line": stop_line, "target": target, "regime": rg}
    return {"bias": "中性", "action": "持有过夜,设好动态止损",
            "detail": "结构无明确方向",
            "stop_line": stop_line, "target": target, "regime": rg}
