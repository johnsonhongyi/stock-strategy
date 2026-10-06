#!/usr/bin/env python3
"""VWAP 引擎:1/3/5/10 日 VWAP 与流动方向分析.

定义:
  VWAP = Σ成交额 / Σ成交量(股). 有成交额时用精确值;
  源缺成交额时(如新浪兜底)用典型价 (H+L+C)/3 估算, 口径统一为元/股.
  1 日 VWAP  = 当日 5 分钟 K 累加(实时)
  N 日 VWAP  = 前 N-1 日日 K + 当日实时聚合,锚定累计
  流动方向   = 各 VWAP 线今日值 vs 昨日终值的斜率(up/down/flat,阈值 0.3%)
  排列       = 价格与四条线的大小关系 bull(多头)/bear(空头)/mixed(混乱)
  金叉/死叉   = 1 日 VWAP 与 3 日 VWAP 相对位置的变化(对比昨日)

所有函数都是纯计算;拉数由调用方通过 backend API 完成。
"""
from __future__ import annotations

SLOPE_THRESHOLD = 0.003  # 0.3%


def _shares_per_unit(bar) -> int:
    """bar 的 volume 单位: 新浪 K 线是股, 东财等是手."""
    meta = bar.get("meta") or {}
    return 1 if meta.get("source") in ("sina", "yahoo", "kraken") else 100


def _bar_px_vol(bar) -> tuple[float, float]:
    """返回 (单价, 股数).
    有成交额时用 amount/量(精确); 源没给成交额时(如新浪分钟 K 兜底)
    用典型价 (H+L+C)/3 估算单价. 两种路径都统一到元/股口径,
    与 Σ成交额/Σ成交量(股) 同口径, 与 volume 单位无关."""
    vol = bar.get("volume") or 0
    if vol <= 0:
        return 0.0, 0.0
    spu = _shares_per_unit(bar)
    amt = bar.get("amount") or 0
    shares = vol * spu
    if amt > 0:
        return amt / shares, shares
    h, l, c = bar.get("high") or 0, bar.get("low") or 0, bar.get("close") or 0
    typ = (h + l + c) / 3.0 if (h or l or c) else 0.0
    return typ, shares

def vwap_of(bars) -> float | None:
    """从一组 K 线 bar(含 amount 成交额、volume 成交量)算 VWAP.
    有 amount 用精确值; 无 amount(如新浪兜底)用典型价估算, 口径统一为元/股."""
    pxv = 0.0
    w = 0.0
    for b in bars or []:
        px, shares = _bar_px_vol(b)
        if px > 0 and shares > 0:
            pxv += px * shares
            w += shares
    if w <= 0:
        return None
    return pxv / w


def _slope(cur, prev) -> str:
    if cur is None or prev is None or prev == 0:
        return "flat"
    r = (cur - prev) / prev
    if r >= SLOPE_THRESHOLD:
        return "up"
    if r <= -SLOPE_THRESHOLD:
        return "down"
    return "flat"


def analyze(price: float, minute_bars, daily_bars, today: str) -> dict:
    """输入实时价、当日 5 分钟 K、近 20+ 日 K(含今日),返回完整 VWAP 结构."""
    hist = [b for b in (daily_bars or [])
            if (b.get("time") or "")[:10] < today]

    def anchored(n: int) -> float | None:
        """含今日的 N 日锚定 VWAP(前 N-1 日日 K + 当日 5 分钟 K 聚合)."""
        past = hist[-(n - 1):] if n > 1 else []
        return vwap_of(list(past) + list(minute_bars or []))

    def anchored_prev(n: int) -> float | None:
        """昨日终值的 N 日锚定 VWAP(纯历史,不含今日)."""
        past = hist[-n:] if n >= 1 else []
        return vwap_of(past)

    vwap = {f"d{n}": anchored(n) for n in (1, 3, 5, 10)}
    vwap_prev = {f"d{n}": anchored_prev(n) for n in (1, 3, 5, 10)}
    slope = {k: _slope(vwap[k], vwap_prev[k]) for k in vwap}

    # 价格相对各线位置
    pos = {}
    for k, v in vwap.items():
        if v is None or price is None:
            pos[k] = None
        else:
            pos[k] = "above" if price >= v else "below"

    # 排列:price 与 d1>d3>d5>d10 全成立为多头,全反之为 空头
    alignment = "mixed"
    vals = [price, vwap["d1"], vwap["d3"], vwap["d5"], vwap["d10"]]
    if all(x is not None for x in vals):
        if vals[0] > vals[1] > vals[2] > vals[3] > vals[4]:
            alignment = "bull"
        elif vals[0] < vals[1] < vals[2] < vals[3] < vals[4]:
            alignment = "bear"

    # 金叉/死叉:今日 d1 vs d3 相对位置,对比昨日
    cross = None
    if vwap["d1"] is not None and vwap["d3"] is not None \
            and vwap_prev["d1"] is not None and vwap_prev["d3"] is not None:
        now_rel = "above" if vwap["d1"] >= vwap["d3"] else "below"
        prev_rel = "above" if vwap_prev["d1"] >= vwap_prev["d3"] else "below"
        if now_rel == "above" and prev_rel == "below":
            cross = "golden"
        elif now_rel == "below" and prev_rel == "above":
            cross = "dead"

    # 流动方向一致性
    ups = sum(1 for s in slope.values() if s == "up")
    dns = sum(1 for s in slope.values() if s == "down")
    flow = "up" if ups == 4 else ("down" if dns == 4 else "mixed")

    # 价格偏离 1 日 VWAP
    deviation = None
    if price and vwap["d1"]:
        deviation = (price - vwap["d1"]) / vwap["d1"]

    return {
        "price": price,
        "vwap": {k: round(v, 3) if v else None for k, v in vwap.items()},
        "vwap_prev": {k: round(v, 3) if v else None for k, v in vwap_prev.items()},
        "slope": slope,
        "position": pos,
        "alignment": alignment,
        "cross": cross,
        "flow": flow,
        "deviation": round(deviation, 4) if deviation is not None else None,
    }


def describe(struct: dict) -> str:
    """给人看的一句话结构描述."""
    v = struct["vwap"]
    parts = [f"1日{v['d1']}", f"3日{v['d3']}", f"5日{v['d5']}", f"10日{v['d10']}"]
    align = {"bull": "多头排列", "bear": "空头排列", "mixed": "排列混乱"}[struct["alignment"]]
    flow = {"up": "四线同上", "down": "四线同下", "mixed": "方向分化"}[struct["flow"]]
    return f"VWAP({'/'.join(parts)}),{align},{flow}"
