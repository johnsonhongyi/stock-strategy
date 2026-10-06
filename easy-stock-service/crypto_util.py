"""数字货币通用小工具:价格精度 + 永续空头数学(纯函数,可单测)。

fmt_price: 自适应精度,解决 DOGE/XRP/ADA 等小价格不能 round(...,2) 一刀切的问题。
  >=100 -> 2位; >=1 -> 4位; <1 -> 6位。
"""
from __future__ import annotations


def fmt_price(px) -> float:
    """自适应价格精度(返回 round 后的 float)。px<=0 返回 0.0。"""
    try:
        px = float(px)
    except (TypeError, ValueError):
        return 0.0
    if px <= 0:
        return 0.0
    if px >= 100:
        return round(px, 2)
    if px >= 1:
        return round(px, 4)
    return round(px, 6)


def short_notional(margin: float, leverage: float) -> float:
    """名义价值 = 保证金 × 杠杆。"""
    return margin * leverage


def short_qty(notional: float, entry: float) -> float:
    """开空数量(币) = 名义/开仓价。"""
    return notional / entry if entry > 0 else 0.0


def short_pnl_margin(entry: float, exit: float, qty: float,
                     fee_rate: float = 0.001) -> float:
    """空头保证金口径净盈亏(美元):
    (entry-exit)/entry × 名义 - 双边手续费(名义价值)。
    开仓手续费按 entry 名义,平仓按 exit 名义,精确到实际成交数量。"""
    if entry <= 0 or qty <= 0:
        return 0.0
    notional_open = qty * entry
    notional_close = qty * exit
    gross = (entry - exit) / entry * notional_open
    fees = notional_open * fee_rate + notional_close * fee_rate
    return gross - fees


def short_close_pnl(entry: float, exit: float, qty: float,
                    fee_rate: float = 0.001) -> float:
    """平仓动作当次的现金流影响(美元):毛利 - 平仓手续费(名义)。
    开仓手续费在开仓时已扣,不在此重复。"""
    if entry <= 0 or qty <= 0:
        return 0.0
    gross = (entry - exit) / entry * (qty * entry)
    return gross - (qty * exit) * fee_rate


def liq_price(entry: float, leverage: float, mmr: float = 0.005) -> float:
    """估算强平价(空头): entry×(1+1/杠杆-mmr)。mmr=维持保证金率缓冲,默认0.5%。
    10倍下 entry=100 -> 109.5(价格涨9.5%≈保证金亏95%)。"""
    if entry <= 0 or leverage <= 0:
        return 0.0
    return entry * (1 + 1.0 / leverage - mmr)


def liq_guard_triggered(mark: float, liq: float, warn_pct: float = 0.02) -> bool:
    """强平守卫:mark 距强平价 <2% 即触发紧急全平。"""
    if mark <= 0 or liq <= 0:
        return False
    return mark >= liq * (1 - warn_pct)


def margin_ret(entry: float, mark: float, leverage: float) -> float:
    """保证金口径浮动盈亏率(不含手续费):(entry-mark)/entry×leverage。
    空头:价格跌 -> 正收益。"""
    if entry <= 0:
        return 0.0
    return (entry - mark) / entry * leverage
