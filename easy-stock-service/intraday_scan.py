#!/usr/bin/env python3
"""盘中企稳跟踪:只盯跟踪池里"状态变了"的股票,全量推送到企业微信。

跟踪池 = reviews/<最近交易日>.json(盘后回踩分析落盘,含 triggered/tracking)。
每轮(交易时段 cron 每30分钟):
  1) 批量取实时行情 -> 每只算相对昨日1日VWAP的位置:强势(>1.2%)、贴线、弱势(<-1.2%)
  2) 位置跨档变化 -> 推送一条(回踩贴线/跌破/重新站上/反抽回线)
  3) 对"今日涨幅>=8%且未提示过"的 -> 推"盘中大涨"(提示别追,等回踩)
  4) 对"跌破昨日动态止损且未提示过"的 -> 推"破位"(最重要)
  5) 对跨档的股票拉当日5分钟K算实时VWAP,确认信号有效性
不跨档、不破位、不大涨 -> 静默。首轮只记基线不推。
北汽蓝谷(用户主仓,成本5.12)在池中时,推送附带仓位提示。
用法: python3 intraday_scan.py [--dry-run]
"""
import json
import os
import sys
from datetime import datetime
from zoneinfo import ZoneInfo

BJ = ZoneInfo("Asia/Shanghai")
SVC = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, SVC)
import daily_review as dr  # noqa: E402
from push import send as push_send  # noqa: E402
import realpos  # noqa: E402

STATE_FILE = os.path.join(SVC, "logs", "intraday_state.json")
# (实盘持仓成本统一由 realpos.py / positions_real.json 提供)
SURGE_LINE = 8.0            # 当日涨幅>=8%提示大涨
DEV_BAND = 1.2              # 相对昨日1日VWAP的贴线带(±%)
OWN = {"600733"}            # 跳过?不——600733要盯,只是附带仓位提示


def to_api(sym):
    s = dr.norm_sym(sym)
    return ("sh" if s[0] in "69" else "sz") + s


def load_pool():
    """最近的 reviews/*.json"""
    cands = sorted((f for f in os.listdir(os.path.join(SVC, "reviews"))
                    if f.endswith(".json")), reverse=True)
    for f in cands:
        try:
            d = json.load(open(os.path.join(SVC, "reviews", f)))
            pool = (d.get("triggered") or []) + (d.get("tracking") or [])
            if pool:
                return d.get("trade_date", f[:-5]), pool
        except Exception:
            continue
    return None, []


def load_state():
    try:
        return json.load(open(STATE_FILE))
    except OSError:
        return {}


def save_state(st):
    os.makedirs(os.path.dirname(STATE_FILE), exist_ok=True)
    json.dump(st, open(STATE_FILE, "w"), ensure_ascii=False)


SIGLOG_FILE = os.path.join(SVC, "logs", "intraday_siglog.json")
# 跨日降噪的信号类型:贴线类位置更新,连续交易日重复且价格无实质变化时不推。
# 破位/大涨不受限(稀有且重要,止损位每日更新,重破即新信息)。
CROSS_DAY_QUIET = {"回踩贴线", "重新站上", "反抽回线", "跌破贴线",
                   "转弱跌破", "V起拉回"}


def load_siglog():
    try:
        return json.load(open(SIGLOG_FILE))
    except OSError:
        return {}


def save_siglog(sl):
    os.makedirs(os.path.dirname(SIGLOG_FILE), exist_ok=True)
    json.dump(sl, open(SIGLOG_FILE, "w"), ensure_ascii=False)


def cross_day_dup(code, tag, price, trade_date, siglog):
    """同一(code,信号)在近2个交易日内推过且价格变动<1.5% → True(降噪)。"""
    if tag not in CROSS_DAY_QUIET:
        return False
    last = (siglog.get(code) or {}).get(tag)
    if not last or last.get("date") == trade_date:
        return False
    try:
        from trading_calendar import prev_trading_day
        if last["date"] < prev_trading_day(trade_date, 2):
            return False
        lp = last.get("price") or 0
        if lp and abs(price - lp) / lp * 100 < 1.5:
            return True
    except Exception:
        pass
    return False


def live_vwap(bars):
    """当日5分钟K -> 实时VWAP"""
    amt = vol = 0.0
    for b in bars or []:
        v = b.get("volume") or 0
        a = b.get("amount") or 0
        if v <= 0:
            continue
        amt += a if a > 0 else (b.get("high", 0) + b.get("low", 0)
                                + b.get("close", 0)) / 3 * v * 100
        vol += v
    return amt / (vol * 100) if vol > 0 else None


def zone(dev):
    if dev is None:
        return "?"
    if dev > DEV_BAND:
        return "强势"
    if dev < -DEV_BAND:
        return "弱势"
    return "贴线"


def main():
    from trading_calendar import guard_trading_day
    guard_trading_day("intraday_scan")
    dry = "--dry-run" in sys.argv
    force = "--force" in sys.argv
    now = datetime.now(BJ)
    # 非交易时段静默(午休12:00-13:00也静默);--force 跳过用于测试
    if now.weekday() >= 5 and not force:
        print(json.dumps({"notify": False, "reason": "weekend"},
                         ensure_ascii=False))
        return
    hm = now.strftime("%H:%M")
    if not force and not (("09:25" <= hm <= "11:32")
                          or ("13:00" <= hm <= "15:02")):
        print(json.dumps({"notify": False, "reason": "off_hours", "hm": hm},
                         ensure_ascii=False))
        return

    trade_date, pool = load_pool()
    if not pool:
        print(json.dumps({"notify": False, "reason": "no_pool"},
                         ensure_ascii=False))
        return
    api_syms = [to_api(p["symbol"]) for p in pool]
    back = {a: p for a, p in zip(api_syms, pool)}

    try:
        raw = dr.api("/api/v1/quotes/realtime?symbols=%s"
                     % ",".join(api_syms), timeout=25)["data"]
    except Exception as e:
        print(json.dumps({"notify": False, "reason": "data_error: %s" % e},
                         ensure_ascii=False))
        return
    quotes = {}
    for q in raw or []:
        c = dr.norm_sym(q.get("symbol", ""))
        if c:
            quotes[c] = q

    state = load_state()
    if state.get("trade_date") != trade_date:
        state = {"trade_date": trade_date}  # 新交易日,状态清零
    first_run = not any(k != "trade_date" for k in state)

    alerts, lines, alert_meta = [], [], []
    for p in pool:
        code = p["symbol"]
        q = quotes.get(code) or {}
        name = p.get("name", "")
        price = q.get("price") or q.get("close")
        chg = q.get("change_percent")
        if not price:
            continue
        v1 = p.get("vwap1")
        dev = (price - v1) / v1 * 100 if v1 else None
        z = zone(dev)
        prev = state.get(code, {})
        pz, warned_surge = prev.get("zone"), prev.get("surge", False)
        warned_stop = prev.get("stop", False)
        stop = p.get("stop")

        sig = None
        # 破位:跌破昨日动态止损(最高优先级,只报一次)
        if stop and price < stop and not warned_stop:
            sig = ("破位", "跌破昨日动态止损%.2f,现价%.2f" % (stop, price))
            warned_stop = True
        # 大涨:当日>=8%(只报一次,提示别追)
        elif chg is not None and chg >= SURGE_LINE and not warned_surge:
            sig = ("大涨", "今日已+%.1f%%,别追,等回踩" % chg)
            warned_surge = True
        # 跨档变化
        elif pz and pz != z and z != "?":
            trans = {"强势": {"贴线": "回踩贴线", "弱势": "转弱跌破"},
                     "贴线": {"强势": "重新站上", "弱势": "跌破贴线"},
                     "弱势": {"贴线": "反抽回线", "强势": "V起拉回"}}
            t = (trans.get(pz) or {}).get(z)
            if t:
                detail = ("现价%.2f,相对昨日1日VWAP(%s)偏离%+.2f%%"
                          % (price, ("%.2f" % v1) if v1 else "未知",
                             dev if dev is not None else 0))
                sig = (t, detail)

        # 跨档信号用实时VWAP二次确认(贴线类信号才需要)
        if sig and sig[0] in ("回踩贴线", "重新站上", "反抽回线", "跌破贴线"):
            try:
                bars = dr.api("/api/v1/quotes/kline?symbol=%s&period=5&limit=48"
                              % to_api(code), timeout=25)["data"]
                lv = live_vwap(bars)
                if lv:
                    ldev = (price - lv) / lv * 100
                    if sig[0] == "回踩贴线" and not (-2.0 <= ldev <= 1.0):
                        sig = None  # 实时VWAP确认不成立,丢弃
                    elif sig[0] == "重新站上" and ldev < -0.5:
                        sig = None
                    else:
                        sig = (sig[0], sig[1] + ",实时VWAP%.2f偏离%+.2f%%"
                               % (lv, ldev))
            except Exception:
                pass

        if sig and not first_run:
            tag = sig[0]
            emo = {"破位": "🟢", "大涨": "🔴", "回踩贴线": "🟡",
                   "重新站上": "🔴", "反抽回线": "🟡", "跌破贴线": "🟢",
                   "转弱跌破": "🟢", "V起拉回": "🔴"}.get(tag, "🟡")
            extra = ""
            ph = realpos.hint(code, price)
            if ph:
                extra = ph
            alerts.append("%s %s %s %s:%s%s"
                            % (emo, name, code, tag, sig[1], extra))
            alert_meta.append((code, tag, price))
        state[code] = {"zone": z, "surge": warned_surge, "stop": warned_stop}
    save_state(state)

    # 跨日降噪(2026-09-29):同一(code,信号)连续交易日重复且价格无实质变化
    # → 只记不推。破位/大涨不受限。持仓股不受限(用户要求:手里持仓的用我们的
    # 逻辑继续推,只有非持仓的才看信号异动价值)。dry-run不写siglog。
    siglog = load_siglog()
    kept, suppressed = [], 0
    for a, (code, tag, price) in zip(alerts, alert_meta):
        if (not dry and not realpos.is_held(code)
                and cross_day_dup(code, tag, price, trade_date, siglog)):
            suppressed += 1
            continue
        kept.append(a)
        if not dry:
            siglog.setdefault(code, {})[tag] = {"date": trade_date,
                                                "price": price}
    if not dry:
        save_siglog(siglog)
    alerts = kept

    out = {"notify": bool(alerts), "count": len(alerts),
           "first_run": first_run, "dry": dry, "suppressed": suppressed}
    if alerts and not dry:
        title = "盘中信号 %s|%d只" % (hm, len(alerts))
        # 标题由 push 的企业微信通道自动加粗置顶,正文不再重复
        body = "\n\n".join("- %s" % a for a in alerts[:8])
        try:
            push_send(title, body)  # 全通道:企业微信+息知,互为保底
            out["pushed"] = True
        except Exception as e:
            out["pushed"] = False
            out["push_error"] = str(e)
    elif dry:
        out["alerts_preview"] = alerts
    print(json.dumps(out, ensure_ascii=False))


if __name__ == "__main__":
    main()
