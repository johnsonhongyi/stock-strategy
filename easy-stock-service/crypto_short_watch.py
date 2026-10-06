#!/usr/bin/env python3
"""空头持仓小时级盯盘(1h 迭代),两类输出:
1. SL/TP 触发 -> 平 paper 仓 + 企业微信推送(强通知)
2. 结构预警(只推送操作建议,不自动平仓):
   - 转浮亏: mark 首次站上 entry
   - 接近止损: mark >= SL*0.997 / 接近止盈: mark <= TP*1.003
   - 下破延续: mark < entry*0.998 且创新低
   - VWAP收复: 小时收盘 > 开仓日小时VWAP*1.005(策略自有平空信号,镜像单仅建议)
同一预警按冷却期只推一次(默认4h,接近止损/止盈2h)。
只动 logs/paper_ledger_crypto_short.json。--dry-run 只打印不推送不写账本。
"""
import datetime
import json
import os
import sys

SVC = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, SVC)
import bars_crypto  # noqa: E402

LEDGER = os.path.join(SVC, "logs", "paper_ledger_crypto_short.json")
FEE = 0.001
DRY = "--dry-run" in sys.argv

COOLDOWN = {"underwater": 8 * 3600, "stop_near": 2 * 3600, "tp_near": 2 * 3600,
            "new_low": 4 * 3600, "vwap_reclaim": 4 * 3600, "p30_cycle": 4 * 3600,
            "expect_warn": 4 * 3600, "expect_dead": 8 * 3600}


def expect_action(pos, ratio, warn_thr, exit_thr):
    """P30 不及预期时间窗的行为分野(纯函数):
    返回 'close'(策略单达出局线,自动平) / 'alert_only'(只强预警) / None。
    镜像单(reason 含 mirror_)永不自动平,只预警,保证与用户实盘同进退。"""
    if ratio is None:
        return None
    is_mirror = "mirror_" in str(pos.get("reason") or "")
    if ratio >= exit_thr:
        return "alert_only" if is_mirror else "close"
    if ratio >= warn_thr:
        return "alert_only"
    return None


def utcnow():
    return datetime.datetime.now(datetime.timezone.utc)


def push(title, content):
    if DRY:
        print("[DRY push]", title, "|", content.replace("\n", " "))
        return
    import push as pushmod
    pushmod.send(title, content)


def cooled(state, key, now_ts):
    last = state.get(key, 0)
    return now_ts - last >= COOLDOWN[key]


def entry_day_vwap(code, open_time):
    """开仓日(UTC)小时 VWAP。"""
    try:
        day = open_time[:10]
        hs = [h for h in bars_crypto.intraday_hourly(code, n=72) if h["time"][:10] == day]
        amt = sum(h["amount"] for h in hs)
        vol = sum(h["volume"] for h in hs)
        return amt / vol if vol else None
    except Exception:
        return None


def main():
    d = json.load(open(LEDGER))
    positions = d.get("positions", [])
    state = d.setdefault("alert_state", {})
    now = utcnow()
    now_ts, now_s = now.timestamp(), now.strftime("%Y-%m-%dT%H:%M:%SZ")
    if not positions:
        print("no positions, quiet")
        return
    kept = []
    for pos in positions:
        if pos.get("side") != "short":
            kept.append(pos)
            continue
        code = pos["code"]
        try:
            mark = float(bars_crypto.realtime(code))
        except Exception as e:
            print("realtime fail", code, str(e)[:80])
            kept.append(pos)
            continue
        pos["mark"] = mark
        entry, lev, margin, notional = pos["entry"], pos["leverage"], pos["margin"], pos["notional"]
        sl, tp = pos.get("stop_loss"), pos.get("take_profit")
        chg = (mark - entry) / entry * 100  # 正数=空单浮亏
        unreal = round((entry - mark) / entry * lev * margin, 2)

        # ---- 1) SL/TP 触发:平 paper 仓 ----
        trig = None
        if sl and mark >= sl:
            trig = ("止损", sl)
        elif tp and mark <= tp:
            trig = ("止盈", tp)
        if trig:
            kind, lvl = trig
            pnl = round((entry - mark) / entry * lev * margin - notional * FEE * 2, 2)
            ret = round(pnl / margin * 100, 2)
            d["account"]["margin_balance"] = round(d["account"].get("margin_balance", 100000) + pnl, 2)
            d.setdefault("closed", []).append({
                "code": code, "side": "short", "entry": entry, "exit": mark, "leverage": lev,
                "margin": margin, "pnl": pnl, "ret_pct": ret,
                "reason": "watch_%s触发@%s" % (kind, lvl),
                "open_time": pos.get("open_time"), "close_time": now_s})
            push("空头%s触发 %s" % (kind, code),
                 "%s 空单%s触发\n开仓 %s → 平仓 %s\n保证金盈亏 %s U（%s%%）" % (code, kind, entry, mark, pnl, ret))
            print("CLOSED", code, kind, "pnl", pnl)
            continue

        # ---- 2) 结构预警 ----
        pos["unrealized"] = unreal
        pos["min_mark"] = min(pos.get("min_mark", mark), mark)
        pos["max_mark"] = max(pos.get("max_mark", mark), mark)
        alerts = []
        if mark > entry and cooled(state, "underwater", now_ts):
            alerts.append(("underwater",
                           "BTC空单转浮亏",
                           "现价%s 已站上开仓价%s，浮亏%s U。小时线低点抬高，空头逻辑走弱。建议：考虑平仓，或等%s止损。" % (mark, entry, -unreal, sl)))
        if sl and mark >= sl * 0.997 and cooled(state, "stop_near", now_ts):
            alerts.append(("stop_near",
                           "BTC空单接近止损",
                           "现价%s，距止损%s仅%s点。建议：可提前平（省滑点），或等触发。" % (mark, sl, round(sl - mark, 1))))
        if tp and mark <= tp * 1.003 and cooled(state, "tp_near", now_ts):
            alerts.append(("tp_near",
                           "BTC空单接近止盈",
                           "现价%s，距止盈%s仅%s点。建议：可分批止盈，或等触发。" % (mark, tp, round(mark - tp, 1))))
        if mark < entry * 0.998 and mark <= pos["min_mark"] and cooled(state, "new_low", now_ts):
            alerts.append(("new_low",
                           "BTC空单下破延续",
                           "现价%s创新低，跌破延续中。建议：持有看%s止盈。" % (mark, tp)))
        vwap_d = entry_day_vwap(code, pos.get("open_time", ""))
        if vwap_d:
            try:
                last_h = bars_crypto.intraday_hourly(code, n=3)[-1]
                if last_h["close"] > vwap_d * 1.005 and cooled(state, "vwap_reclaim", now_ts):
                    alerts.append(("vwap_reclaim",
                                   "BTC空单VWAP收复预警",
                                   "小时收盘%s站上开仓日VWAP%s×1.005，策略自有平空信号。镜像单跟你同进退不自动平，建议考虑平仓。" % (round(last_h["close"], 1), round(vwap_d, 1))))
            except Exception as e:
                print("vwap check skip", str(e)[:60])
        for key, title, content in alerts:
            state[key] = now_ts
            push(title, content)
            print("ALERT", key, title)
        # ---- 3) P30 周期结构(只读展示,不自动平仓) ----
        try:
            import cycle_structure
            hs = cycle_structure.drop_forming(bars_crypto.intraday_hourly(code, n=96))
            if len(hs) >= 12:
                ca = cycle_structure.analyze(hs)
                cj = ca["judgments"]
                extra = None
                if cj.get("regime_switch") and cooled(state, "p30_cycle", now_ts):
                    extra = ("p30_cycle", "BTC空单周期结构切换",
                             "P30：%s。情绪耗尽后价格选择向上，空头逻辑动摇，建议重新评估是否持有。" % ca["summary"].replace("周期结构：", ""))
                elif (cj.get("weak_rebound") or cj.get("steady_down")) and cooled(state, "p30_cycle", now_ts):
                    extra = ("p30_cycle", "BTC空单周期结构",
                             "P30：%s。反弹弱、情绪消耗中，空头结构延续。" % ca["summary"].replace("周期结构：", ""))
                if extra:
                    state[extra[0]] = now_ts
                    push(extra[1], extra[2])
                    print("ALERT", extra[0], extra[1])
        except Exception as e:
            print("p30 skip", str(e)[:60])
        # ---- 4) P30 不及预期时间窗(2026-10-03 用户拍板) ----
        # ratio≥1.0 强预警建议出局;ratio≥2.0 策略单自动平,镜像单只强预警不自动平
        expect_fired = False
        try:
            import cycle_structure
            cfg30 = cycle_structure.load_cfg()
            hs = cycle_structure.drop_forming(bars_crypto.intraday_hourly(code, n=96))
            ec = cycle_structure.expect_check(hs, "short", cfg30, now=now)
            ratio = ec["time_ratio"]
            act = expect_action(pos, ratio,
                                cfg30["expect_ratio_warn"], cfg30["expect_ratio_exit"])
            if act == "close":
                pnl = round((entry - mark) / entry * lev * margin - notional * FEE * 2, 2)
                ret = round(pnl / margin * 100, 2)
                d["account"]["margin_balance"] = round(d["account"].get("margin_balance", 100000) + pnl, 2)
                d.setdefault("closed", []).append({
                    "code": code, "side": "short", "entry": entry, "exit": mark, "leverage": lev,
                    "margin": margin, "pnl": pnl, "ret_pct": ret,
                    "reason": "watch_不及预期平空(反弹%.1fx≥%s)" % (ratio, cfg30["expect_ratio_exit"]),
                    "open_time": pos.get("open_time"), "close_time": now_s})
                push("空头不及预期平仓 %s" % code,
                     "%s 空单反弹时长达下跌%.1f倍，不及预期\n开仓 %s → 平仓 %s\n保证金盈亏 %s U（%s%%）" % (code, ratio, entry, mark, pnl, ret))
                print("CLOSED", code, "expect_dead", "pnl", pnl)
                continue
            elif act == "alert_only":
                if ratio >= cfg30["expect_ratio_exit"] and cooled(state, "expect_dead", now_ts):
                    state["expect_dead"] = now_ts
                    push("BTC空单不及预期(周期2.0x)",
                         "反弹已用下跌%.1f倍时间仍未转向，不及预期。镜像单跟你同进退不自动平，建议立即出局。" % ratio)
                    print("ALERT", "expect_dead", "BTC空单不及预期(周期2.0x)")
                    expect_fired = True
                elif ratio < cfg30["expect_ratio_exit"] and cooled(state, "expect_warn", now_ts):
                    state["expect_warn"] = now_ts
                    push("BTC空单不及预期预警",
                         "反弹时长已达下跌%.1fx，不及预期，建议出局。" % ratio)
                    print("ALERT", "expect_warn", "BTC空单不及预期预警")
                    expect_fired = True
        except Exception as e:
            print("expect skip", str(e)[:60])
        if not alerts and not expect_fired:
            print("HOLD", code, "mark", mark, "unreal", unreal, "chg%+0.2f" % chg)
        kept.append(pos)
    d["positions"] = kept
    if not DRY:
        json.dump(d, open(LEDGER, "w"))
    print("watch done", now_s, "dry=" + str(DRY))


if __name__ == "__main__":
    main()
