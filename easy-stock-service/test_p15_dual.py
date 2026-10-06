"""P15双轨决策树合成测试(2026-09-30)。
场景: 1换仓成功 2不同板块拒绝 3次日龙头证伪卖出换仓腿 4偏离过高不追
      5数据缺失不动 6复牌一字陷阱不追 7a早盘下杀15分钟收复持有 7b未收复卖出
全程 dry=True,不写账本不记日志。"""
import os
import sys
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import copy
import paper_trade as pt
import grid_t
import sector_leader as sl

CFG = {"version": "test", "buy": {"amount_per_trade": 100000},
       "rotation": {"weak_th": -3, "strong_th": 3, "pyramid_amount": 10000}}
TODAY = pt._today()

BASE_POS = {"code": "600733", "name": "北汽蓝谷", "buy_date": "2026-09-28",
            "buy_price": 5.00, "shares": 20000, "cost": 100000, "locked": 0,
            "adds": 0, "strategy_version": "test", "snapshot": {}}

LEADER_OK = {"ok": True, "code": "600418", "name": "江淮汽车", "chg": 6.0,
             "price": 27.6, "high": 28.0, "low": 26.0, "plate": "新能源汽车",
             "is_self": False, "vwap": 27.3, "above_vwap": True,
             "new_high": True, "dead": False}

# ---- monkeypatch ----
_orig = {}
def _patch(mod, name, fn):
    _orig[(mod, name)] = getattr(mod, name)
    setattr(mod, name, fn)

STRONG = {"弱": (-5, ["weak"])}
def fake_strength(price, sess_vwap, bars, today_s, gl, bd):
    return STRONG["弱"]

_patch(grid_t, "position_strength", fake_strength)
_patch(pt, "_sector_mode_of", lambda x: "normal")

def mkledger(pos):
    return {"cash": 500000, "positions": [copy.deepcopy(pos)]}

def run_case(name, pos, ld, struct, trace, trap, plate_p, price_map,
            m5=None, bars=None, expect=None):
    _patch(sl, "leader_status", lambda code, date_s=None: copy.deepcopy(ld))
    _patch(sl, "leader_structure", lambda code, date_s=None, market="CN", **k: copy.deepcopy(struct))
    _patch(sl, "sector_capital_trace", lambda plate, date_s=None, **k: trace)
    _patch(sl, "resume_trap", lambda code, date_s=None, **k: trap)
    _patch(sl, "plate_of", lambda code, date_s=None: plate_p if code == "600733" else (ld.get("plate") if ld.get("ok") else ""))
    _patch(sl, "leader_track", lambda code, date_s=None: {"ok": True, "switched": False})
    _patch(pt, "realtime_price", lambda code: price_map.get(code, 0))
    _patch(pt, "min5_bars", lambda code, limit=48: m5 or [])
    _patch(pt, "daily_bars", lambda code, limit=15: bars or [])
    lg = mkledger(pos)
    sold, pyramided = pt.run_rotation(CFG, lg, dry=True)
    acts = []
    for p, sh, px, pnl, why in sold:
        acts.append(("SELL", p["code"], sh, round(px, 2), why[:24]))
    # 买入腿:看持仓里有没有新增龙头
    for p in lg["positions"]:
        sw = (p.get("snapshot") or {}).get("p15_swap") or {}
        if p["code"] == "600418" and sw.get("date") == TODAY:
            acts.append(("BUY", p["code"], p["shares"], p["buy_price"], "P15换仓买入龙头"))
    ok = True
    for e in (expect or []):
        if e[0] == "NOP15":
            continue
        if not any(a[0] == e[0] and a[1] == e[1] for a in acts):
            ok = False
    if any(e[0] == "NOP15" for e in (expect or [])):
        if any("P15" in a[4] for a in acts):
            ok = False
    print(("PASS " if ok else "FAIL ") + name, acts if not ok or True else "")
    return ok

STRUCT_OK = {"ok": True, "strong": True, "days_above": 4, "days": 5}
STRUCT_WEAK = {"ok": True, "strong": False, "days_above": 2, "days": 5}
STRUCT_NODATA = {"ok": False, "reason": "bars_short"}

allok = True
# 1 换仓成功
allok &= run_case("1换仓成功", BASE_POS, LEADER_OK, STRUCT_OK, True, False,
                  "新能源汽车", {"600733": 4.90, "600418": 27.6},
                  expect=[("SELL", "600733"), ("BUY", "600418")])
# 2 不同板块拒绝(fail-closed)
ld2 = copy.deepcopy(LEADER_OK); ld2["plate"] = "光伏"
allok &= run_case("2不同板块拒绝", BASE_POS, ld2, STRUCT_OK, True, False,
                  "新能源汽车", {"600733": 4.90, "600418": 27.6},
                  expect=[("NOP15",)])
# 3 次日龙头证伪 -> 卖出换仓腿
from trading_calendar import prev_trading_day
yday = prev_trading_day(TODAY, market="CN")
pos3 = copy.deepcopy(BASE_POS)
pos3.update({"code": "600418", "name": "江淮汽车", "buy_price": 27.6, "shares": 300,
             "snapshot": {"p15_swap": {"from": "600733", "plate": "新能源汽车",
                                       "date": yday, "buy_price": 27.6}}})
ld3 = copy.deepcopy(LEADER_OK); ld3.update({"dead": True, "above_vwap": False,
                                            "price": 26.5, "vwap": 27.3})
_patch(sl, "plate_of", lambda code, date_s=None: "新能源汽车")
allok &= run_case("3次日龙头证伪卖腿", pos3, ld3, STRUCT_OK, True, False,
                  "新能源汽车", {"600418": 26.5},
                  expect=[("SELL", "600418")])
# 4 偏离过高不追:卖一半但不买
ld4 = copy.deepcopy(LEADER_OK); ld4.update({"price": 28.5, "vwap": 27.3})  # 偏离4.4%
r = run_case("4偏离过高只卖不买", BASE_POS, ld4, STRUCT_OK, True, False,
             "新能源汽车", {"600733": 4.90, "600418": 28.5},
             expect=[("SELL", "600733")])
allok &= r
# 5 数据缺失不动
allok &= run_case("5无龙头数据不动", BASE_POS, {"ok": False}, STRUCT_NODATA,
                  False, None, "新能源汽车", {"600733": 4.90},
                  expect=[("NOP15",)])
# 6 复牌一字陷阱不追
r6acts = run_case("6一字陷阱不追", BASE_POS, LEADER_OK, STRUCT_OK, True, True,
                  "新能源汽车", {"600733": 4.90, "600418": 27.6},
                  expect=[("SELL", "600733")])
allok &= r6acts
# 7a 早盘下杀15分钟收复 -> 持有
pos7 = copy.deepcopy(pos3)
ld7 = copy.deepcopy(LEADER_OK); ld7.update({"price": 27.0, "vwap": 27.3,
                                             "above_vwap": False, "dead": False})
m5_rec = [{"close": 27.1}, {"close": 27.35}, {"close": 27.2}]  # 15分钟内收复过
allok &= run_case("7a下杀收复持有", pos7, ld7, STRUCT_OK, True, False,
                  "新能源汽车", {"600418": 27.0}, m5=m5_rec,
                  bars=[{"time": "2026-09-29", "high": 28.0, "close": 27.6}],
                  expect=[("NOP15",)])
# 7b 早盘下杀15分钟未收复 -> 卖出
m5_norec = [{"close": 27.1}, {"close": 27.2}, {"close": 27.05}]
allok &= run_case("7b下杀未收复卖出", pos7, ld7, STRUCT_OK, True, False,
                  "新能源汽车", {"600418": 27.0}, m5=m5_norec,
                  bars=[{"time": "2026-09-29", "high": 28.0, "close": 27.6}],
                  expect=[("SELL", "600418")])

print("ALL PASS" if allok else "SOME FAILED")
