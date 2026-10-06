"""P22 龙头早发现验证:涨跌不对称 + 强度阶梯。
1) point-in-time:江淮 09-22~09-29 每日收盘视角,不剧透
2) 北汽当前不对称<3,不误判为龙头
3) 数据不足 fail-closed
4) 异动高点抬升识别"""
import sys, os
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from sector_leader import asymmetry, leader_strength, surge_highs_rising

ok = 0
def check(name, cond, info=""):
    global ok
    print(("PASS " if cond else "FAIL ") + name + (" | " + str(info) if info else ""))
    ok += 1 if cond else 0

# 1) 江淮 point-in-time(date_s=次日,看前一日收盘视角,只能用当日及之前已收盘K)
exp = {"2026-09-24": 1, "2026-09-25": 2, "2026-09-29": 2}  # date_s=次日,看前一日收盘
for date_s, want_min in exp.items():
    ls = leader_strength("600418", date_s=date_s)
    check("江淮%s前收盘level>=%d" % (date_s, want_min),
          ls.get("ok") and ls.get("level", 0) >= want_min, ls)
# 09-23收盘视角:首板次日只到L1观察,不确认(胆量不用在首板次日)
ls = leader_strength("600418", date_s="2026-09-24")
check("江淮09-23收盘仅L1观察", ls.get("ok") and ls.get("level") == 1, ls)
# 09-28收盘视角:不对称4.08,连续结构成立->L2确认(换仓可执行日)
ls = leader_strength("600418", date_s="2026-09-29")
a = asymmetry("600418", date_s="2026-09-29")
check("江淮09-28收盘不对称>3", a.get("ok") and a["ratio"] > 3.0, a)

# 2) 北汽:涨多跌少不成立,不进L2
a = asymmetry("600733")
check("北汽不对称<3", a.get("ok") and a["ratio"] < 3.0, a)
ls = leader_strength("600733")
check("北汽level<2", ls.get("ok") and ls.get("level", 9) < 2, ls)

# 3) fail-closed:无名代码
a = asymmetry("999999")
check("无数据fail-closed", a.get("ok") is False, a)
ls = leader_strength("999999")
check("强度无数据level=0", ls.get("level") == 0 and ls.get("ok") is False, ls)

# 4) 异动高点抬升:江淮09-30前有多个异动日且高点抬升
sh = surge_highs_rising("600418", date_s="2026-09-30")
check("江淮异动高点抬升", sh.get("ok") and sh.get("rising"), sh)

# 4b) 上轨运行:江淮09-30视角近5日沿上轨运行>=4天,北汽=0
from sector_leader import boll_ride
br = boll_ride("600418", date_s="2026-09-30")
check("江淮沿上轨运行", br.get("ok") and br.get("ride", 0) >= 4, br)
br = boll_ride("600733", date_s="2026-09-30")
check("北汽没碰上轨", br.get("ok") and br.get("ride", 0) == 0, br)

# 5) OBV资金环(黄白线):启动日站上黄线 / 磨底期底背离护盘
from sector_leader import obv_status
ob = obv_status("600418", date_s="2026-09-23")  # 看09-22涨停收盘
check("江淮09-22涨停OBV站上黄线", ob.get("ok") and ob.get("above_ma"), ob)
ob = obv_status("600418", date_s="2026-09-22")  # 看09-21磨底收盘
check("江淮09-21收盘OBV已站上黄线(资金先行价格2天)",
      ob.get("ok") and ob.get("above_ma") is True, ob)
ls = leader_strength("600418", date_s="2026-09-29")  # 看09-28收盘
check("江淮09-28收盘L2+资金确认",
      ls.get("ok") and ls.get("level", 0) >= 2 and ls.get("obv_above_ma") is True, ls)

print("\n%d/15 通过" % ok)
sys.exit(0 if ok == 15 else 1)
