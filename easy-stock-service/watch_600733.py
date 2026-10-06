#!/usr/bin/env python3
"""北汽蓝谷(600733)VWAP 盯盘哨兵.

交易逻辑(用户口径):以 1/3/5/10 日 VWAP 为核心,看 VWAP 线的流动方向做交易。
  VWAP = Σ成交额/Σ成交量,精确值。

信号(每个自然日每种只推送一次;穿越类按"状态变化"触发):
  price_cross_v1_up    价格上穿 1 日 VWAP      -> 日内转强
  price_cross_v1_down  价格跌破 1 日 VWAP      -> 日内转弱
  v1_golden_v3         1 日 VWAP 上穿 3 日 VWAP -> 短期动能转强
  v1_dead_v3           1 日 VWAP 下穿 3 日 VWAP -> 短期动能转弱
  align_bull           形成多头排列(P>1>3>5>10日) -> 最强结构
  align_bear           形成空头排列               -> 最弱结构
  flow_up              四线流动方向一致向上       -> 趋势向上
  flow_down            四线流动方向一致向下       -> 趋势向下
  deviation_warn       价格偏离 1 日 VWAP >=3%   -> 不追高/不杀跌
  close_summary        收盘小结                  -> 次日策略

只在交易时段(北京时间周一至周五 09:25-15:10)工作,其余时间静默。
输出 JSON,cron worker 据 notify 决定是否打扰用户。
"""
import json
import os
import re
import subprocess
import sys
import time
import urllib.request
import market_cache
from datetime import datetime
from zoneinfo import ZoneInfo

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from vwap import analyze, describe
import t1_strategy

BJ = ZoneInfo("Asia/Shanghai")
HOME = os.path.expanduser("~")
SVC = os.path.join(HOME, "workspace", "easy-stock-service")
BASE = "http://127.0.0.1:20081"
SYMBOL = "sh600733"
STATE_FILE = os.path.join(SVC, "watch_600733_state.json")


def out(obj):
    print(json.dumps(obj, ensure_ascii=False))
    sys.exit(0)


def load_token():
    try:
        with open(os.path.join(SVC, ".env")) as f:
            for line in f:
                line = line.strip()
                if line.startswith("A_STOCK_TOKEN="):
                    v = line.split("=", 1)[1].strip().strip('"').strip("'")
                    if v:
                        return v
    except OSError:
        pass
    return None


def api(path, timeout=20, force_refresh=False):
    req = urllib.request.Request(BASE + path, headers={"X-A-Stock-Token": TOKEN})
    with market_cache.urlopen(req, timeout=timeout, force_refresh=force_refresh) as r:
        return json.load(r)


def direct_sina_quote(symbol="600733", timeout=12):
    """后端实时接口故障时的直连兜底:新浪 hq.sinajs.cn 必须带 Referer。
    返回与后端 /api/v1/quotes/realtime 同形的 quote dict。带 3 次重试,
    应对上游抖动期的空响应。"""
    code = "sh%s" % symbol if symbol.startswith("6") else "sz%s" % symbol
    last_err = "unknown"
    for _ in range(3):
        try:
            url = "https://hq.sinajs.cn/rn=%d&list=%s" % (
                int(time.time() * 1000), code)
            req = urllib.request.Request(url, headers={
                "Referer": "https://finance.sina.com.cn/",
                "User-Agent": "Mozilla/5.0"})
            with market_cache.urlopen(req, timeout=timeout) as r:
                raw = r.read().decode("gbk", "ignore")
            m = re.search(r'"([^"]*)"', raw)
            f = m.group(1).split(",") if m else []
            if len(f) >= 10 and f[3]:
                name, o, pc = f[0], float(f[1]), float(f[2])
                price, high, low = float(f[3]), float(f[4]), float(f[5])
                return {"symbol": symbol, "name": name, "price": price,
                        "open": o, "previous_close": pc, "high": high,
                        "low": low, "volume": int(float(f[8])),
                        "amount": float(f[9]),
                        "change_percent": round((price - pc) / pc * 100, 2)
                        if pc else 0.0,
                        "_source": "sina_direct"}
            last_err = "bad_fields=%d" % len(f)
        except Exception as e:  # noqa: BLE001 - 重试
            last_err = "%s:%s" % (type(e).__name__, str(e)[:60])
        time.sleep(1)
    raise RuntimeError("sina_direct failed: %s" % last_err)


def ensure_backend():
    try:
        if api("/api/health", timeout=8).get("ok"):
            return True
    except Exception:
        pass
    try:
        subprocess.run(["bash", os.path.join(SVC, "start.sh")],
                       capture_output=True, timeout=60)
    except Exception:
        pass
    try:
        return bool(api("/api/health", timeout=8).get("ok"))
    except Exception:
        return False


TOKEN = load_token()
now = datetime.now(BJ)
today = now.strftime("%Y-%m-%d")
hm = now.hour * 60 + now.minute

# 交易日历门禁:非交易日直接静默(国庆等休市期间不推送)
from trading_calendar import is_trading_day as _is_td
if not _is_td(today):
    out({"notify": False, "reason": "not_trading_day"})

if now.weekday() >= 5:
    out({"notify": False, "reason": "weekend"})
if not (9 * 60 + 25 <= hm <= 15 * 60 + 10):
    out({"notify": False, "reason": "off_hours"})
if not TOKEN:
    out({"notify": False, "reason": "no_token"})
if not ensure_backend():
    out({"notify": False, "reason": "backend_down"})

try:
    with open(STATE_FILE) as f:
        state = json.load(f)
except (OSError, json.JSONDecodeError):
    state = {}
if state.get("date") != today or state.get("engine") != "vwap":
    state = {"date": today, "engine": "vwap", "fired": [],
             "prev": {}, "summary_done": False}

try:
    try:
        q = api(f"/api/v1/quotes/realtime?symbols={SYMBOL}")["data"][0]
    except Exception:
        q = direct_sina_quote(SYMBOL)  # 后端实时源故障时直连新浪兜底
    min5 = api(f"/api/v1/quotes/kline?symbol={SYMBOL}&period=5&limit=60")["data"]
    daily = api(f"/api/v1/quotes/kline?symbol={SYMBOL}&period=day&limit=25")["data"]
except Exception as e:
    out({"notify": False, "reason": f"data_error: {e}"})

price = q.get("price")
chg = q.get("change_percent") or 0.0
st = analyze(price, min5, daily, today)

events = []
fired = set(state.get("fired", []))
prev = state.get("prev", {})

def fire(key, title, detail):
    # 颜色语义:🔴涨/走强 🟢跌/转弱 🟡观望/警示
    emo = {"price_cross_v1_up": "🔴", "price_cross_v1_down": "🟢",
           "v1_golden_v3": "🔴", "v1_dead_v3": "🟢",
           "align_bull": "🔴", "align_bear": "🟢",
           "flow_up": "🔴", "flow_down": "🟢",
           "deviation_warn": "🟡", "t1_gap_confirm": "🟢",
           "t1_buy": "🔴", "t1_sell": "🟢", "t1_overnight": "🟡"}.get(key, "")
    if key == "t1_gap":
        emo = "🔴" if "高开" in title else ("🟢" if "低开" in title else "🟡")
    if key == "close_summary":
        emo = "🔴" if chg > 0 else ("🟢" if chg < 0 else "🟡")
    if key not in fired:
        fired.add(key)
        events.append({"key": key, "title": (emo + " " + title).strip(),
                       "detail": detail})

pos_v1 = st["position"].get("d1")
if pos_v1 and prev.get("pos_v1") and pos_v1 != prev["pos_v1"]:
    if pos_v1 == "above":
        fire("price_cross_v1_up", "价格站上1日VWAP",
             f"现价{price}站上1日VWAP{st['vwap']['d1']},日内转强")
    else:
        fire("price_cross_v1_down", "价格跌破1日VWAP",
             f"现价{price}跌破1日VWAP{st['vwap']['d1']},日内转弱")

d1v3 = None
if st["vwap"]["d1"] is not None and st["vwap"]["d3"] is not None:
    d1v3 = "above" if st["vwap"]["d1"] >= st["vwap"]["d3"] else "below"
if d1v3 and prev.get("d1_vs_v3") and d1v3 != prev["d1_vs_v3"]:
    if d1v3 == "above":
        fire("v1_golden_v3", "1日VWAP上穿3日VWAP",
             f"1日{st['vwap']['d1']}上穿3日{st['vwap']['d3']},短期动能转强")
    else:
        fire("v1_dead_v3", "1日VWAP下穿3日VWAP",
             f"1日{st['vwap']['d1']}下穿3日{st['vwap']['d3']},短期动能转弱")

if st["alignment"] in ("bull", "bear") and prev.get("alignment") \
        and st["alignment"] != prev["alignment"]:
    if st["alignment"] == "bull":
        fire("align_bull", "形成多头排列",
             f"价格>1日>3日>5日>10日VWAP,最强结构")
    else:
        fire("align_bear", "形成空头排列",
             f"价格<1日<3日<5日<10日VWAP,最弱结构")

if st["flow"] in ("up", "down") and prev.get("flow") \
        and st["flow"] != prev["flow"]:
    if st["flow"] == "up":
        fire("flow_up", "VWAP四线流动方向一致向上", "趋势向上")
    else:
        fire("flow_down", "VWAP四线流动方向一致向下", "趋势向下")

dev = st["deviation"]
if dev is not None and abs(dev) >= 0.03:
    direction = "高于" if dev > 0 else "低于"
    fire("deviation_warn", f"价格偏离1日VWAP过大",
         f"现价{price}{direction}1日VWAP{abs(round(dev*100,1))}%,"
         f"{'不追高,等回踩' if dev > 0 else '不杀跌,等企稳'}")

if hm >= 15 * 60 + 1 and not state.get("summary_done"):
    state["summary_done"] = True
    fire("close_summary", "收盘小结",
         f"收{price},日涨跌{round(chg,2)}%;{describe(st)}")

# ============ T+1 引擎 ============
open_p = q.get("open")
prev_c = q.get("previous_close")

# 开盘缺口计划(开盘后推送一次)
if hm < 9 * 60 + 45 and open_p and prev_c:
    gp = t1_strategy.gap_plan(open_p, prev_c)
    state["gap_kind"] = gp["kind"]
    if gp["kind"] in ("gap_up", "gap_down"):
        title = "高开卖点窗口" if gp["kind"] == "gap_up" else "低开下杀预案"
        fire("t1_gap", title, gp["plan"])

# 低开下杀 15 分钟确认(09:45 后)
if state.get("gap_kind") == "gap_down" and hm >= 9 * 60 + 45:
    rec = "收复1日VWAP,为假下杀,持有" \
        if pos_v1 == "above" else "未收复1日VWAP,为真下杀,卖出"
    fire("t1_gap_confirm", "低开下杀确认", rec + f",现价{price}")

# T+1 买点(一天一个,给明天的卖留空间)
bp = t1_strategy.buy_point(price, st)
if bp["hit"]:
    fire("t1_buy", "T+1买点出现",
         f"{bp['reason']};买点区{bp['price_zone']},"
         f"动态止损{bp['stop']}(明日被低开下杀打到即走)")

# T+1 卖点(卖的是昨日及以前的仓位,一天一个)
sp = t1_strategy.sell_point(price, st)
if sp["hit"]:
    kind_txt = "止盈" if sp["kind"] == "take_profit" else "止损"
    fire("t1_sell", f"T+1卖点出现({kind_txt})",
         f"{sp['reason']};卖点区{sp['price_zone']}")

# 隔夜决策(14:45 后,防次日高开/低开下杀)
if hm >= 14 * 60 + 45:
    from vwap import vwap_of
    sess_vwap = vwap_of(min5)
    on = t1_strategy.overnight(price, st, daily, today, sess_vwap)
    rg = on.get("regime") or {}
    fire("t1_overnight",
         f"隔夜决策:{on['action']}",
         f"{on['bias']},{on['detail']};"
         f"定式:{rg.get('name')};"
         f"动态止损线{on['stop_line']},止盈目标{on['target']}")

# ============ P11 两日高低网格做T ============
import grid_t
gl = grid_t.levels_from_daily(daily, today)
if gl:
    brk = grid_t.trend_broken(price, st)
    if brk:
        fire("grid_clear", "🟢趋势走坏一键清仓",
             f"{'+'.join(brk)},网格做T终止,全部清仓不再接回;现价{price}")
    else:
        zone = grid_t.zone_of(price, gl)
        if zone == "sell":
            fire("grid_sell", "🔴网格卖出区挂单",
                 f"现价{price}进入卖出区{gl['sell_lo']}~{gl['sell_hi']}"
                 f"(两日高{gl['h2']}+0.5%~1%),卖出可卖份额做T降成本")
        elif zone == "buy":
            fire("grid_buy", "🔴网格买入区挂单",
                 f"现价{price}进入买入区{gl['buy_lo']}~{gl['buy_hi']}"
                 f"(两日低{gl['l2']}+1%~2%),买回做T")
    grid_txt = (f"网格卖出区{gl['sell_lo']}~{gl['sell_hi']}/"
               f"买入区{gl['buy_lo']}~{gl['buy_hi']}")
else:
    grid_txt = ""

# ============ P11 破位三段式:先标记(防震仓) -> 反弹确认 -> VWAP下方卖出 ============
bd_event, bd_new = grid_t.breakdown_update(min5, st["vwap"]["d1"],
                                           state.get("breakdown"))
state["breakdown"] = bd_new
if bd_event == "mark":
    fire("bd_mark", "🟡疑似破位先标记",
         f"现价{price}跌破1日VWAP{st['vwap']['d1']},先标记不卖防震仓;"
         f"收复VWAP则为假破位解除")
elif bd_event == "unmark":
    fire("bd_unmark", "🔴假破位震仓解除",
         f"现价{price}收复1日VWAP,为震仓假破位,继续网格做T")
elif bd_event == "confirm":
    fire("bd_confirm", "🟢破位确认反弹无力",
         f"低点{bd_new['low_px']}反弹{bd_new['bounce_pct']}%"
         f"高点{bd_new['bounce_high']}仍在VWAP{st['vwap']['d1']}下方,"
         f"反弹量能仅下破段{bd_new['vol_ratio']}倍,在VWAP下方卖出,不等主杀")

# ============ P12 统一强度分(实盘同步) ============
str_s, str_tags = grid_t.position_strength(
    price, st["vwap"]["d1"], daily, today, gl, state.get("breakdown"))
strength_txt = "强度分%d(%s)" % (str_s, ",".join(str_tags)) if str_tags else ""

state["prev"] = {"pos_v1": pos_v1, "d1_vs_v3": d1v3,
                 "alignment": st["alignment"], "flow": st["flow"]}
state["fired"] = sorted(fired)
try:
    with open(STATE_FILE, "w") as f:
        json.dump(state, f, ensure_ascii=False)
except OSError:
    pass

# 多通道外部推送(微信等),与 cron worker 的站内消息互为保底。
# 通道 Key 在 push_config.json 里配,没配的通道自动跳过。
push_results = []
leader_txt = ""
if events:
    # P15 板块龙头状态:只在有事件时查一次,不额外刷屏
    try:
        from sector_leader import leader_status as _ldst
        _ld = _ldst("600733", today)
        if _ld.get("ok"):
            leader_txt = "🐲%s龙头:%s%s(%+.2f%%)%s" % (
                _ld["plate"], _ld["name"], _ld["code"], _ld["chg"],
                "站上VWAP" if _ld["above_vwap"] else
                ("跌破VWAP" if _ld["dead"] else "VWAP下方"))
            if _ld.get("locked_note"):
                leader_txt += "[%s]" % _ld["locked_note"]
            if _ld["dead"]:
                leader_txt += "→龙头不龙板块退潮,北汽只卖不买"
            elif _ld.get("strong") and str_s <= -3:
                leader_txt += "→强弱分化,可考虑半仓换龙头"
    except Exception:  # noqa: BLE001
        pass
    try:
        from push import send as push_send
        for ev in events:
            # 息知在微信卡片里只显示标题:把现价/涨跌幅/信号塞进标题
            chg_emo = "🔴" if chg > 0 else ("🟢" if chg < 0 else "🟡")
            push_results.append({
                "event": ev["key"],
                "channels": push_send(
                    "%s北汽蓝谷%s(%+.2f%%)%s" % (chg_emo, price, chg,
                                              ev["title"]),
                    "%s\n现价%s(%+.2f%%) 1日VWAP%s%s%s%s" % (
                        ev["detail"], price, chg, st["vwap"]["d1"],
                        ("\n" + grid_txt) if grid_txt else "",
                        ("\n" + strength_txt) if strength_txt else "",
                        ("\n" + leader_txt) if leader_txt else "")),
            })
    except Exception as e:  # noqa: BLE001 - 推送失败不影响主流程
        push_results = [{"error": "%s" % e}]

out({
    "notify": bool(events),
    "events": events,
    "pushed": push_results,
    "quote": {"name": q.get("name"), "price": price,
               "change_percent": round(chg, 2),
               "high": q.get("high"), "low": q.get("low"),
               "open": q.get("open"),
               "previous_close": q.get("previous_close")},
    "vwap_structure": st,
    "structure_text": describe(st),
    "strength": {"score": str_s, "tags": str_tags},
    "leader": leader_txt,
    "grid": gl,
    "t1": {
        "buy": {"hit": bp["hit"], "reason": bp["reason"],
                "price_zone": bp["price_zone"], "stop": bp["stop"]},
        "sell": {"hit": sp["hit"], "kind": sp["kind"], "reason": sp["reason"]},
        "regime": t1_strategy.regime(
            daily, st, today,
            __import__("vwap").vwap_of(min5)),
    },
})
