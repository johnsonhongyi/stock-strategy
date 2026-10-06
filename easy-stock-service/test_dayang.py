"""dayang.py + P23预期止损测试(2026-09-30)"""
import sys
sys.path.insert(0, "/home/hatch/workspace/easy-stock-service")
from dayang import dayang_signal, dayang_events

ok = 0


def check(name, cond, info=""):
    global ok
    print(("PASS " if cond else "FAIL ") + name, info if not cond else "")
    ok += 1 if cond else 0


# 1) 江淮09-24视角(看09-23大阳):启动买点
r = dayang_signal("600418", date_s="2026-09-24")
check("江淮09-23大阳启动买点", r["signal"] == "startup_buy", r)
check("启动含站上MA60+放量+OBV",
      r.get("ev", {}).get("above_ma60") and r["ev"]["vol_ratio"] >= 1.5
      and r["ev"]["obv_above"], r.get("ev"))

# 2) 江淮09-30视角(看09-29收盘):回踩买点
r = dayang_signal("600418", date_s="2026-09-30")
check("江淮大阳后回踩买点", r["signal"] == "pullback_buy", r)

# 3) 北汽:无大阳->neutral,不误判
r = dayang_signal("600733", date_s="2026-09-30")
check("北汽无大阳neutral", r["signal"] == "neutral", r)

# 4) 无数据fail-closed
r = dayang_signal("999999")
check("无数据neutral", r["signal"] == "neutral", r)
check("大阳事件无数据为空", dayang_events("999999") == [])

# 5) P23不及预期:合成持仓
import paper_trade as pt


def mkbars(dates, high):
    return [{"time": d + " 15:00:00", "high": h} for d, h in zip(dates, high)]


tds = ["2026-09-21", "2026-09-22", "2026-09-23", "2026-09-24", "2026-09-25"]
p = {"buy_date": "2026-09-18", "buy_price": 20.00,
     "snapshot": {"expect": {"days": 5, "target_pct": 2.0, "why": "t"}}}
check("P23到期未创新高触发",
      pt._p23_miss(p, mkbars(tds, [20.1] * 5), "2026-09-25") is True)
check("P23创新高不触发",
      pt._p23_miss(p, mkbars(tds, [20.1, 20.1, 20.5, 20.1, 20.1]),
                   "2026-09-25") is False)
check("P23未到期不触发",
      pt._p23_miss(p, mkbars(tds[:2], [20.1] * 2), "2026-09-22") is False)
p2 = {"buy_date": "2026-09-18", "buy_price": 20.00, "snapshot": {}}
check("P23无expect不触发", pt._p23_miss(p2, mkbars(tds, [20.1] * 5),
                                     "2026-09-25") is False)

print("\n%d/10 通过" % ok)
sys.exit(0 if ok == 10 else 1)
