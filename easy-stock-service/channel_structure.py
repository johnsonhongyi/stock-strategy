#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
通道结构特征引擎 (Channel Structure Feature Engine) — v2 对齐版

与用户底座 SSOT (pyQuant3/stock_standalone/JSONData/tdx_channel_factory.py
:: TDXChannelFactory.calculate) 的纯 NumPy 移植,无 talib/pandas 依赖。

移植的核心定义 (逐字对齐):
  通道三轨   通达信《GG通道线走势》: UR=6, LR=6
             TC2 = BARSLAST(H=HHV(H,36))+1, BC2 = BARSLAST(L=LLV(L,36))+1
             NOD = |TC2-BC2|; 中轨 = 收盘价在波段上的线性回归外推
             上轨 = 中轨 + MAX(波段内 high-中轨)
             下轨 = 中轨 - MAX(波段内 中轨-low)
  ch_pos     = (close - lower) / (upper - lower) * 100
  ch_slope_deg = degrees(arctan(slope / close_ref * 100)), close_ref=最新收盘价
  ch_dir     = sign(slope)
  支撑线     通达信 KX 上涨支撑线 DRAWLINE(LOW<=LLV(LOW,20), LOW,
             HIGH>=HHV(HIGH,20), LLV(LOW,4), 1), 最新波段向右延伸
  supp_pos   = (close - supp_price) / supp_price * 100
  supp_slope_deg = degrees(arctan(supp_slope / close * 100))

与 SSOT 的已知差异 (简化,已标注):
  - 未移植"主导大暴跌保留主通道 / 反弹子通道探索"分支,统一走主波段回归
  - 未移植 limit_min/limit_max 停画逻辑 (DRAWNULL),末端值恒有效
  - ch_pos 取末端直接计算,不做 valid_pos[-1] 回退 (避免 89.64 式跨时点混用;
    如需复刻 SSOT 行为,见注释)
"""
import math
import numpy as np


def _arr(x):
    return np.asarray(x, dtype=float)


def _rolling_max(a, w):
    a = _arr(a)
    n = len(a)
    out = np.empty(n)
    for i in range(n):
        s = max(0, i - w + 1)
        out[i] = np.max(a[s:i + 1])
    return out


def _rolling_min(a, w):
    a = _arr(a)
    n = len(a)
    out = np.empty(n)
    for i in range(n):
        s = max(0, i - w + 1)
        out[i] = np.min(a[s:i + 1])
    return out


def slope_deg(slope_per_bar, ref_price):
    """倾角定义: degrees(atan(斜率/参考价*100)),与 SSOT 逐字一致。"""
    if not ref_price or ref_price <= 0:
        return 0.0
    return math.degrees(math.atan(slope_per_bar * 100.0 / ref_price))


def tdx_channel(high, low, close, ur=6, lr=6):
    """通达信 GG通道线走势核心 (SSOT 主路径移植)。

    返回末端三轨与全序列,键:
      upper/mid/lower (末端值), upper_s/mid_s/lower_s (全序列),
      slope (中轨每根K斜率), slope_deg, ch_dir, ch_pos,
      ch_width, ch_width_pct, tc2, bc2, nod
    """
    high, low, close = _arr(high), _arr(low), _arr(close)
    n = len(close)
    assert n >= 10, "至少需要 10 根K线"

    hhv_win = min(n, 6 * ur)
    llv_win = min(n, 6 * lr)
    tc1_idx = np.where(high == _rolling_max(high, hhv_win))[0]
    bc1_idx = np.where(low == _rolling_min(low, llv_win))[0]
    tc2 = int(n - tc1_idx[-1]) if len(tc1_idx) else 1
    bc2 = int(n - bc1_idx[-1]) if len(bc1_idx) else 1
    tc2, bc2 = max(1, tc2), max(1, bc2)

    nod = abs(tc2 - bc2)
    if nod < 2:
        nod = max(tc2, bc2, 5)

    # 锚定波段回归: 以 min(tc2,bc2) 为锚点,向前取 nod 根收盘价做 OLS
    anc = min(tc2, bc2)
    anc_idx = n - anc
    r_start = max(0, anc_idx - nod)
    r_slice = close[r_start:anc_idx + 1]
    kl = len(r_slice)
    if kl >= 2:
        xm = (kl - 1.0) / 2.0
        xdev = np.arange(kl, dtype=float) - xm
        vx = kl * (kl * kl - 1.0) / 12.0
        ym = float(np.mean(r_slice))
        slp = float(np.dot(xdev, r_slice - ym) / vx) if vx > 1e-8 else 0.0
        icp = ym - slp * xm
        npv = slp * (kl - 1.0) + icp
    else:
        slp, npv = 0.0, float(close[-1])

    cb = np.arange(n, 0, -1, dtype=float)
    mid_s = npv - slp * (cb - anc)

    c_start = max(0, n - max(tc2, bc2))
    c_end = min(n - 1, n - min(tc2, bc2))
    r_h = high[c_start:c_end + 1]
    r_m = mid_s[c_start:c_end + 1]
    r_l = low[c_start:c_end + 1]
    at = max(0.0, float(np.max(r_h - r_m))) if len(r_h) else 0.0
    ut = max(0.0, float(np.max(r_m - r_l))) if len(r_l) else 0.0

    upper_s = mid_s + at
    lower_s = mid_s - ut

    upper, mid, lower = float(upper_s[-1]), float(mid_s[-1]), float(lower_s[-1])
    width = upper - lower
    width_safe = width if width > 1e-6 else max(0.05, float(close[-1]) * 0.08)
    ch_pos = float((close[-1] - lower) / width_safe * 100.0)
    # 注: SSOT 对全序列做 in_trend 掩码后取 valid_pos[-1],末端 NaN 时会回退到
    # 更早的柱 (这就是 600127 ch_pos=89.64 跨时点混用的根因)。本移植恒用末端
    # 直接计算,保证 ch_pos 与同行 ch_upper/ch_lower/close 同一时点。

    close_ref = max(0.1, float(close[-1]))
    ch_dir = 1 if slp > 1e-6 else (-1 if slp < -1e-6 else 0)
    return {
        "upper": upper, "mid": mid, "lower": lower,
        "upper_s": upper_s, "mid_s": mid_s, "lower_s": lower_s,
        "slope": float(slp),
        "slope_deg": slope_deg(slp, close_ref),
        "ch_dir": ch_dir,
        "ch_pos": ch_pos,
        "ch_width": float(width),
        "ch_width_pct": float(width / close_ref * 100.0),
        "tc2": tc2, "bc2": bc2, "nod": int(nod),
    }


def kx_support_line(high, low, close):
    """通达信 KX 上涨支撑线 (SSOT DRAWLINE 语义移植)。

    KX_RAW:=DRAWLINE(LOW<=LLV(LOW,20), LOW, HIGH>=HHV(HIGH,20), LLV(LOW,4), 1)
    返回: supp_price (末端支撑线价格), supp_slope, supp_slope_deg,
          supp_pos (%), supp_days, is_broken
    """
    high, low, close = _arr(high), _arr(low), _arr(close)
    n = len(close)
    llv20 = _rolling_min(low, 20)
    hhv20 = _rolling_max(high, 20)
    llv4 = _rolling_min(low, 4)

    cond1 = (low <= llv20)
    cond2 = (high >= hhv20)

    pairs = []
    curr_c1, last_c2 = -1, -1
    for i in range(n):
        if cond1[i]:
            if curr_c1 != -1 and last_c2 != -1 and last_c2 > curr_c1:
                pairs.append((curr_c1, float(low[curr_c1]), last_c2, float(llv4[last_c2])))
                last_c2 = -1
            curr_c1 = i
        elif cond2[i] and curr_c1 != -1:
            last_c2 = i
    if curr_c1 != -1 and last_c2 != -1 and last_c2 > curr_c1:
        pairs.append((curr_c1, float(low[curr_c1]), last_c2, float(llv4[last_c2])))

    c_last = float(close[-1]) if close[-1] > 1e-8 else 1.0
    if pairs:
        i_a, p_a, i_b, p_b = pairs[-1]
        k = (p_b - p_a) / float(i_b - i_a)
        supp_price = k * ((n - 1) - i_a) + p_a
        supp_days = (n - 1) - i_a
    else:
        # 兜底: 下轨低点向收盘连线 (与 SSOT fallback 同构)
        k = (c_last - float(low[-1])) / 1.0
        supp_price = float(low[-1]) + k * 1
        supp_days = 1

    supp_price = max(0.01, float(supp_price))
    return {
        "supp_price": supp_price,
        "supp_slope": float(k),
        "supp_slope_deg": slope_deg(k, c_last),
        "supp_pos": float((c_last - supp_price) / supp_price * 100.0),
        "supp_days": int(supp_days),
        "is_broken": bool(c_last < supp_price),
    }


def compute_channel(high, low, close, lookback=60):
    """对外主接口: 输入 OHLC 数组,返回与用户 ATS 字段同名的特征字典。

    lookback: 取最近 N 根K线参与计算 (对应早盘预处理窗口)。
    """
    high, low, close = _arr(high)[-lookback:], _arr(low)[-lookback:], _arr(close)[-lookback:]
    ch = tdx_channel(high, low, close)
    sp = kx_support_line(high, low, close)
    return {
        "ch_dir": ch["ch_dir"],
        "ch_slope": ch["slope"],
        "ch_slope_deg": ch["slope_deg"],
        "ch_upper": ch["upper"],
        "ch_mid": ch["mid"],
        "ch_lower": ch["lower"],
        "ch_pos": ch["ch_pos"],
        "ch_width_pct": ch["ch_width_pct"],
        "ch_supp_price": sp["supp_price"],
        "ch_supp_slope": sp["supp_slope"],
        "ch_supp_slope_deg": sp["supp_slope_deg"],
        "ch_supp_pos": sp["supp_pos"],
        "ch_supp_broken": sp["is_broken"],
        "tc2": ch["tc2"],
        "bc2": ch["bc2"],
        "nod": ch["nod"],
        "close": float(close[-1]),
    }


# --------------------------------------------------------------------------
# 策略翻译层: 用户四套 query 的近似表达 (阈值沿用用户 ATS: 3.0/6.0/10.0)
# --------------------------------------------------------------------------

def cond_channel_classic_dual(f):
    """通道经典双共振_主升加速型 (近似)。"""
    return (f["ch_dir"] == 1
            and f["ch_slope_deg"] > 3.0
            and f["ch_supp_slope_deg"] > 10.0
            and f["close"] >= f["ch_supp_price"]
            and f["ch_supp_pos"] <= 5.0
            and f["ch_supp_price"] >= f["ch_lower"]
            and 20.0 <= f["ch_pos"] <= 65.0)


def cond_extreme_resonance(f):
    """极致共振_黄金低吸伏击型 (近似)。"""
    return (f["ch_dir"] == 1
            and f["ch_slope_deg"] > 6.0
            and f["ch_supp_slope_deg"] > 10.0
            and f["close"] >= f["ch_supp_price"]
            and f["ch_supp_pos"] <= 3.0
            and 15.0 <= f["ch_pos"] <= 45.0)


def cond_small_yang_start(f):
    """小连阳加速启动 (近似,需配合外部连阳计数)。"""
    return (f["ch_dir"] == 1
            and f["ch_slope_deg"] > 3.0
            and f["ch_pos"] < 80.0
            and not f["ch_supp_broken"])


def cond_strict_dual_volume(f):
    """严密双共振_量价齐升型 (近似,需配合外部量能确认)。"""
    return (f["ch_dir"] == 1
            and f["ch_slope_deg"] > 6.0
            and f["ch_supp_slope_deg"] > 6.0
            and 30.0 <= f["ch_pos"] <= 70.0
            and not f["ch_supp_broken"])


STRATEGIES = {
    "通道经典双共振_主升加速型": cond_channel_classic_dual,
    "极致共振_黄金低吸伏击型": cond_extreme_resonance,
    "小连阳加速启动": cond_small_yang_start,
    "严密双共振_量价齐升型": cond_strict_dual_volume,
}


def diagnose(high, low, close, lookback=60):
    """单票诊断: 返回特征 + 四套策略命中情况。"""
    f = compute_channel(high, low, close, lookback=lookback)
    hits = {name: bool(fn(f)) for name, fn in STRATEGIES.items()}
    return {"features": f, "hits": hits}
