#!/usr/bin/env python3
"""P30 时间周期结构单测(point-in-time,只用已收盘K,不用未来数据)。
跑: python3 test_p30_cycle.py
"""
import datetime
import os
import sys

SVC = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, SVC)

import cycle_structure

PASS, FAIL = 0, 0


def check(name, cond, detail=""):
    global PASS, FAIL
    if cond:
        PASS += 1
        print("  PASS %s %s" % (name, detail))
    else:
        FAIL += 1
        print("  FAIL %s %s" % (name, detail))


def mk_hourly(n, closes, t0="2026-10-01 00:00"):
    """合成已收盘小时K:high=close*1.0005, low=close*0.9995。"""
    bars = []
    t = datetime.datetime.strptime(t0, "%Y-%m-%d %H:%M")
    for i, c in enumerate(closes):
        bars.append({"time": (t + datetime.timedelta(hours=i)).strftime("%Y-%m-%d %H:%M"),
                     "open": c, "high": c * 1.0005, "low": c * 0.9995,
                     "close": c, "volume": 100.0, "amount": c * 100.0})
    assert len(bars) == n
    return bars


def btc_case():
    """复现 2026-10-03 BTC 实测:上涨→下跌6h/3368点→反弹9h/715点。"""
    up = [83123 + i * (4106.0 / 24) for i in range(25)]          # idx0..24, 最高87229
    dn = [87229 - j * (3368.0 / 6) for j in range(1, 7)]        # idx25..30, 最低83861
    rb = [83861 + j * (715.0 / 9) for j in range(1, 10)]        # idx31..39, 到84576
    return mk_hourly(40, up + dn + rb)


print("== 1. BTC 实测复现(下跌6h/反弹9h/修复~21%/weak_rebound) ==")
bars = btc_case()
a = cycle_structure.analyze(bars)
legs = a["legs"]
check("段数>=3", len(legs) >= 3, "n=%d" % len(legs))
dn, rb = legs[-2], legs[-1]
check("下跌段6h", dn["dir"] == "down" and dn["hours"] == 6.0, str(dn["hours"]))
check("下跌点数~3368", abs(dn["points"] - 3368) < 150, str(dn["points"]))
check("下跌斜率~561", abs(dn["slope"] - 561) < 30, str(dn["slope"]))
check("反弹段9h", rb["dir"] == "up" and rb["hours"] == 9.0, str(rb["hours"]))
check("time_ratio=1.5", abs(a["time_ratio"] - 1.5) < 0.01, str(a["time_ratio"]))
check("retrace~0.21", 0.18 < a["retrace_ratio"] < 0.25, str(a["retrace_ratio"]))
j = a["judgments"]
check("weak_rebound=True", j["weak_rebound"] is True)
check("steady_down=True", j["steady_down"] is True)
check("steady_up=False", j["steady_up"] is False)
check("regime_switch=False", j["regime_switch"] is False)
check("版本戳v1.8", a["strategy_version"] == "v1.8", a["strategy_version"])
check("摘要含弱反弹", "弱反弹" in a["summary"], a["summary"])

print("== 2. point-in-time:forming bar 不参与 ==")
cur_h = datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%d %H:%M")
forming = dict(bars[-1])
forming["time"] = cur_h
forming["close"] = bars[-1]["close"] + 500  # forming 中跳涨500,不应影响判定
bars_f = bars[:-1] + [forming]
dropped = cycle_structure.drop_forming(bars_f)
check("drop_forming 剔除", len(dropped) == len(bars) - 1)
a2 = cycle_structure.analyze(dropped)
ref = cycle_structure.analyze(bars[:-1])
check("判定不受forming影响",
      a2["judgments"] == ref["judgments"] and a2["time_ratio"] == ref["time_ratio"],
      "weak=%s ratio=%s" % (a2["judgments"]["weak_rebound"], a2["time_ratio"]))

print("== 3. 稳态上升 ==")
# 上涨10h/+2000(斜率200) → 回踩8h/-800(斜率100,不破前低)
up = [50000 + i * 200.0 for i in range(11)]
dn = [52000 - j * 100.0 for j in range(1, 9)]
b3 = mk_hourly(19, up + dn, t0="2026-09-01 00:00")
a3 = cycle_structure.analyze(b3)
check("steady_up=True", a3["judgments"]["steady_up"] is True, a3["summary"])
check("weak_rebound=False", a3["judgments"]["weak_rebound"] is False)

print("== 4. 结构切换(V型急拉) ==")
# 下跌7h/-3000(斜率~429) → 3h内急拉+1800(斜率600>429,修复60%)
dn4 = [60000 - j * (3000.0 / 7) for j in range(8)]
up4 = [57000 + j * 600.0 for j in range(1, 4)]
b4 = mk_hourly(11, dn4 + up4, t0="2026-09-02 00:00")
a4 = cycle_structure.analyze(b4)
check("regime_switch=True", a4["judgments"]["regime_switch"] is True, a4["summary"])
check("weak_rebound=False", a4["judgments"]["weak_rebound"] is False)

print("== 5. 阈值来自 strategy.yaml ==")
cfg = cycle_structure.load_cfg()
check("weak_time_ratio=1.2", cfg["weak_time_ratio"] == 1.2)
check("weak_retrace=0.382", cfg["weak_retrace"] == 0.382)
check("steady_slope_mult=1.5", cfg["steady_slope_mult"] == 1.5)
check("accel_mult=1.0", cfg["accel_mult"] == 1.0)

print("== 6. 数据不足不崩 ==")
a6 = cycle_structure.analyze(mk_hourly(5, [100.0] * 5))
check("空legs全False", a6["legs"] == [] and not any(a6["judgments"].values()))

print("\nPASS %d, FAIL %d" % (PASS, FAIL))
sys.exit(1 if FAIL else 0)
