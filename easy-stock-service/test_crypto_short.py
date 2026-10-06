#!/usr/bin/env python3
"""crypto_short 空头镜像策略单测(point-in-time,只用已收盘K,不用未来数据)。
跑: python3 test_crypto_short.py
"""
import os
import sys

SVC = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, SVC)

from crypto_util import (fmt_price, short_pnl_margin, short_close_pnl,
                         liq_price, liq_guard_triggered, margin_ret,
                         short_notional, short_qty)
from crypto_pool import s_p25_ok, score_short_bars

PASS, FAIL = 0, 0


def check(name, cond, detail=""):
    global PASS, FAIL
    if cond:
        PASS += 1
        print("  PASS %s %s" % (name, detail))
    else:
        FAIL += 1
        print("  FAIL %s %s" % (name, detail))


def mk_bars(n, start_close, step, vol=100.0):
    """合成已收盘日K:收盘价按 step 逐日变化,amount=close*volume。"""
    bars, px = [], start_close
    for i in range(n):
        bars.append({"date": "2026-09-%02d" % (i + 1), "open": px, "high": px * 1.01,
                     "low": px * 0.99, "close": px, "prev_close": bars[-1]["close"] if bars else None,
                     "volume": vol, "amount": px * vol})
        px = px * (1 + step)
    return bars


print("== 1. S-P25 空头趋势门 ==")
bear = mk_bars(30, 100.0, -0.01)   # 每天跌1%:空头排列且收盘<MA20
check("空头排列判定True", s_p25_ok(bear) is True)
bull = mk_bars(30, 50.0, 0.01)     # 每天涨1%:多头
check("多头排列判定False", s_p25_ok(bull) is False)
check("不足25根False", s_p25_ok(mk_bars(10, 100.0, -0.01)) is False)
# 空头排列但收盘站上MA20(末段反弹) -> False
mixed = mk_bars(25, 100.0, -0.01)
for b in mixed[-3:]:
    b["close"] = 200.0
    b["amount"] = 200.0 * b["volume"]
check("空头排列但站上MA20判False", s_p25_ok(mixed) is False)

print("== 2. score_short_bars 空头打分 ==")
bars = mk_bars(30, 100.0, -0.005)  # 日跌0.5%:24h=-0.5% 不在[-8%,-1%]内
s, tags, p25, chg = score_short_bars(bars)
check("趋势+4分", s == 4.0 and p25, "score=%s tags=%s" % (s, tags))
bars2 = mk_bars(30, 100.0, -0.03)  # 日跌3%:24h=-3% 在区间内
s2, tags2, p25_2, chg2 = score_short_bars(bars2)
check("趋势+动量=7分", s2 == 7.0 and p25_2, "score=%s chg=%s" % (s2, chg2))
bars3 = mk_bars(30, 100.0, -0.03)
bars3[-1]["volume"] = bars3[-1]["volume"] * 3  # 昨日放量3倍(量价同增)
bars3[-1]["amount"] = bars3[-1]["close"] * bars3[-1]["volume"]
s3, tags3, _, _ = score_short_bars(bars3)
check("趋势+动量+放量=10分", s3 == 10.0, "score=%s tags=%s" % (s3, tags3))
bars4 = mk_bars(30, 100.0, -0.15)  # 日跌15%:崩盘,不给动量分
s4, _, _, _ = score_short_bars(bars4)
check("崩盘(-15%)不给动量分", s4 == 4.0, "score=%s" % s4)
check("不足25根0分", score_short_bars(mk_bars(10, 100.0, -0.01))[0] == 0.0)

print("== 3. 10倍手续费数学 ==")
# spec 例:entry100 -> exit97, margin10000, lev10, 名义100000
# 毛利=(100-97)/100×100000=3000;手续费=100+97=197;净≈2803
# (spec 原文"+30000"有误,正确为+3000毛利)
pnl = short_pnl_margin(100, 97, short_qty(short_notional(10000, 10), 100))
check("全平净pnl≈2803", abs(pnl - 2803.0) < 0.01, "pnl=%.2f" % pnl)
# 平仓侧单次:毛利3000-平仓费97=2903(开仓费已扣过)
cp = short_close_pnl(100, 97, 1000.0)
check("平仓侧=2903", abs(cp - 2903.0) < 0.01, "cp=%.2f" % cp)
# 亏损例:entry100 exit103(涨3%):毛利-3000,费用100+103=203,净-3203(保证金-32%)
pnl2 = short_pnl_margin(100, 103, 1000.0)
check("亏损净pnl≈-3203", abs(pnl2 - (-3203.0)) < 0.01, "pnl=%.2f" % pnl2)

print("== 4. 强平守卫 ==")
check("liq=109.5", abs(liq_price(100, 10) - 109.5) < 1e-9)
check("mark108触发(<2%距离)", liq_guard_triggered(108, 109.5) is True)
check("mark100不触发", liq_guard_triggered(100, 109.5) is False)
check("边界107.31触发", liq_guard_triggered(109.5 * 0.98, 109.5) is True)
check("边界下方不触发", liq_guard_triggered(109.5 * 0.98 - 0.01, 109.5) is False)

print("== 5. 部分止盈 2/3 数量 ==")
qty = 1.190476  # 100000/84000
qclose = qty * (2.0 / 3.0)
check("平2/3剩余1/3", abs((qty - qclose) - qty / 3) < 1e-9,
      "剩余=%.6f" % (qty - qclose))
# 部分止盈pnl:entry84000 mark82740(-1.5%),平2/3
pp = short_close_pnl(84000, 82740, qclose)
expect = (84000 - 82740) / 84000 * (qclose * 84000) - (qclose * 82740) * 0.001
check("部分止盈pnl正确", abs(pp - expect) < 0.01, "pp=%.2f" % pp)

print("== 6. 保证金盈亏率 ==")
check("-1.5%价格=保证金+15%", abs(margin_ret(100, 98.5, 10) - 0.15) < 1e-9)
check("+1.5%价格=保证金-15%", abs(margin_ret(100, 101.5, 10) + 0.15) < 1e-9)

print("== 7. fmt_price 自适应精度 ==")
check("BTC 2位", fmt_price(84849.126) == 84849.13)
check("小币种4位", fmt_price(2.345678) == 2.3457)
check("DOGE 6位", fmt_price(0.12345678) == 0.123457)
check("0/负保护", fmt_price(0) == 0.0 and fmt_price(-5) == 0.0)

print("== 8. 镜像单判定 ==")
sys.path.insert(0, SVC)
import paper_trade as pt
check("显式SL判镜像", pt._is_mirror({"stop_loss": 84880.0}) is True)
check("mirror_ reason判镜像",
      pt._is_mirror({"reason": "mirror_user_real_short@84000_10x"}) is True)
check("策略单非镜像", pt._is_mirror({"reason": "S-P8"}) is False)

print("== 9. 账本合并:已有持仓不被清空 ==")
lg = {"account": {"currency": "USD", "margin_balance": 90000.0},
      "positions": [{"code": "BTC", "entry": 84000.0, "margin": 10000.0,
                     "notional": 100000.0, "leverage": 10,
                     "open_time": "2026-10-02T19:21:00Z",
                     "stop_loss": 84880.0, "take_profit": 83000.0,
                     "reason": "mirror_user_real_short@84000_10x", "side": "short"}],
      "closed": []}
nlg = pt._normalize_short_ledger(lg)
check("持仓保留", len(nlg["positions"]) == 1 and nlg["positions"][0]["entry"] == 84000.0)
check("qty派生", abs(nlg["positions"][0]["qty"] - 100000.0 / 84000.0) < 1e-9)
check("资金保留", nlg["account"]["margin_balance"] == 90000.0)
check("kill字段补齐", nlg["short_kill"]["armed"] is False)
check("entry/leverage/margin未动",
      nlg["positions"][0]["leverage"] == 10 and nlg["positions"][0]["margin"] == 10000.0)

print("\n结果: %d 通过, %d 失败" % (PASS, FAIL))
sys.exit(1 if FAIL else 0)
