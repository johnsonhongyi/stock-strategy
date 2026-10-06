#!/usr/bin/env python3
"""集合竞价扫描:9:15-9:25 采样竞价盘口,9:26后汇总分析并推送。

cron 每天 9:15 带 --session 跑一次,内循环每 60 秒采样到 9:27。
只盯跟踪池(东财竞价涨幅榜尽力而为,失败不阻塞)。

用户逻辑(不追高,量能温和小仓试错):
- 前两日杀跌 + 今日不低开 + 9:20后竞价最低价≈开盘价(开盘即最低,承接强)
  + 竞价量温和 -> 竞价企稳,可小仓试错,止损=开盘价-2%
- 高开>3% -> 提示别追;竞价爆量 -> 提示小心;低开<-2% -> 转弱
- 9:15-9:20 的挂单可随意撤,只看 9:20 之后的采样(爬坡/最低价都按此口径)

用法: python3 auction_scan.py [--session]
"""
import market_cache
import json
import os
import sys
import time
import urllib.request
from datetime import datetime
from zoneinfo import ZoneInfo

BJ = ZoneInfo("Asia/Shanghai")
SVC = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, SVC)
import daily_review as dr  # noqa: E402
from push import send as push_send  # noqa: E402
import realpos  # noqa: E402

# (实盘持仓成本统一由 realpos.py / positions_real.json 提供)
KILL_LINE = -2.0        # 前两日单日跌幅<=-2% 算杀跌
GAP_OK = -1.0           # 竞价涨幅>=-1% 算不低开
LOW_TOL = 0.002         # 竞价最低价与开盘价偏差<0.2% 算开盘即最低
VOL_MILD = 3.0          # 竞价量/昨日量<3% 算温和
VOL_BURST = 6.0         # >6% 算爆量
HIGH_OPEN = 3.0         # 高开>3% 提示别追
HIGH_60D = 80.0         # 60日涨幅超80%视为高位:形态再企稳也不给试错信号,不接盘


def to_api(sym):
    s = dr.norm_sym(sym)
    return ("sh" if s[0] in "69" else "sz") + s


def load_pool():
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


def day_path(date):
    return os.path.join(SVC, "logs", "auction_%s" % date)


def sample_pool(pool):
    """采一轮竞价快照:{code:{t,price,vol,prev_close,trade_time}}"""
    api_syms = [to_api(p["symbol"]) for p in pool]
    try:
        raw = dr.api("/api/v1/quotes/realtime?symbols=%s"
                     % ",".join(api_syms), timeout=20)["data"]
    except Exception:
        return {}
    quotes = {}
    for q in raw or []:
        c = dr.norm_sym(q.get("symbol", ""))
        if c:
            quotes[c] = q
    now = datetime.now(BJ).strftime("%H:%M:%S")
    out = {}
    for p in pool:
        code = p["symbol"]
        q = quotes.get(code) or {}
        price = q.get("price") or q.get("close")
        if not price:
            continue
        out[code] = {"t": now, "price": price, "vol": q.get("volume") or 0,
                     "prev_close": q.get("previous_close"),
                     "trade_time": q.get("trade_time", "")}
    return out


def eastmoney_auction_top(n=10):
    """东财竞价涨幅榜(尽力而为):9:26-9:30 的涨幅榜即竞价结果排行。"""
    url = ("https://push2.eastmoney.com/api/qt/clist/get?pn=1&pz=%d&po=1&np=1"
           "&ut=bd1d9ddb04089700cf9c27f6f7429421&fltt=2&invt=2&fid=f3"
           "&fs=m:0+t:6,m:0+t:80,m:1+t:2,m:1+t:23"
           "&fields=f1,f2,f3,f12,f13,f14" % n)
    try:
        req = urllib.request.Request(
            url, headers={"Referer": "https://quote.eastmoney.com/"})
        data = json.load(market_cache.urlopen(req, timeout=15))
        diff = (data.get("data") or {}).get("diff") or []
        return [{"name": d.get("f14"), "symbol": dr.norm_sym(d.get("f12")),
                 "price": d.get("f2"), "chg": d.get("f3")} for d in diff]
    except Exception:
        return []


def load_preopen(date):
    """读预开仓清单。"""
    p = os.path.join(SVC, "logs", "preopen_%s.json" % date)
    try:
        return json.load(open(p)).get("candidates", [])
    except Exception:
        return []


def confirm_preopen(today, by_code, kl):
    """TK bidding_momentum 对齐:对预开仓候选做竞价动量确认。

    竞价确认分:
    - 竞价成交额分档(主板):>=1.2亿+3 / >=5000万+2 / >=2000万+1
    - 高开0.5%~7.5%: +2
    - 开盘即最低(9:20后最低价≈开盘价): +2
    否决: 高开>7.5% / 9:20后回落超1% / 开盘价<昨日VWAP×0.995(VWAP门禁)
    确认: 盘前分>=4 且 竞价分>=3 且无否决
    """
    cands = load_preopen(today)
    if not cands:
        return {}
    out = {}
    for c in cands:
        code = c["code"]
        ss = by_code.get(code)
        if not ss:
            out[code] = {"confirmed": False, "reason": "竞价无采样"}
            continue
        real = [s for s in ss if s["t"] >= "09:20:00"] or ss
        prices = [s["price"] for s in real]
        vols = [s["vol"] for s in real]
        prev_close = real[0].get("prev_close") or c.get("prev_close")
        if not prev_close:
            out[code] = {"confirmed": False, "reason": "无昨收"}
            continue
        open_px = prices[-1]
        gap = (open_px - prev_close) / prev_close * 100
        auction_low = min(prices)
        low_eq_open = abs(auction_low - open_px) / open_px <= LOW_TOL
        # 竞价成交额 = 最后一轮竞价量(手)×开盘价×100
        last_vol = vols[-1] if vols else 0
        auction_amt = last_vol * 100 * open_px
        score, notes, veto = 0, [], None
        if auction_amt >= 120_000_000:
            score += 3
            notes.append("竞价抢筹1.2亿+")
        elif auction_amt >= 50_000_000:
            score += 2
            notes.append("竞价5000万+")
        elif auction_amt >= 20_000_000:
            score += 1
            notes.append("竞价2000万+")
        if 0.5 <= gap <= 7.5:
            score += 2
            notes.append("高开%+.1f%%" % gap)
        if low_eq_open:
            score += 2
            notes.append("开盘即最低")
        if gap > 7.5:
            veto = "高开>7.5%不追"
        elif prices[-1] < open_px * 0.99 and len(prices) > 1:
            # 9:20后最后一轮相对开盘回落(用首轮9:20后价做开盘参考)
            first_real = prices[0]
            if prices[-1] < first_real * 0.99:
                veto = "竞价回落超1%"
        yvwap = c.get("yesterday_vwap")
        if yvwap and open_px < yvwap * 0.995:
            veto = "开盘价<昨日VWAP×0.995"
        confirmed = (not veto and c.get("pre_score", 0) >= 4 and score >= 3)
        out[code] = {
            "confirmed": confirmed,
            "name": c.get("name", ""),
            "pre_score": c.get("pre_score"),
            "auction_score": score,
            "auction_notes": notes,
            "veto": veto,
            "price": round(open_px, 2),  # 竞价开盘价=模拟买入价
            "gap": round(gap, 2),
            "auction_amt": int(auction_amt),
            "yesterday_vwap": yvwap,
            "reason": "确认" if confirmed else (veto or "竞价分不足%d" % score),
        }
    return out


def finalize(today, pool):
    """9:26后:汇总采样,算形态,推送。"""
    path = day_path(today)
    if os.path.exists(path + ".done"):
        return {"notify": False, "reason": "already_done"}
    rows = []
    try:
        with open(path + ".jsonl") as f:
            for line in f:
                line = line.strip()
                if line:
                    rows.append(json.loads(line))
    except OSError:
        pass
    # 非交易日:采样时间戳不是今天;轮次太少或时间不在竞价窗口内 -> 脏数据
    ts = sorted(set(r.get("t", "") for r in rows))
    if rows:
        tt = (rows[0].get("trade_time") or "")[:10]
        if tt and tt != today:
            return {"notify": False, "reason": "not_trading_day"}
    if len(ts) < 3 or not all("09:14:00" <= t <= "09:28:00" for t in ts):
        return {"notify": False, "reason": "bad_samples",
                "rounds": len(ts)}

    by_code = {}
    for r in rows:
        by_code.setdefault(r["code"], []).append(r)
    # 昨日量+前两日涨跌:批量日K
    api_syms = [to_api(c) for c in by_code]
    kl = {}
    for i in range(0, len(api_syms), 15):
        try:
            data = dr.api("/api/v1/quotes/kline/batch?symbols=%s&period=day"
                          "&limit=65" % ",".join(api_syms[i:i + 15]),
                          timeout=45)["data"]
            for sym, bars in (data or {}).items():
                c = dr.norm_sym(sym)
                if c and bars and len(bars) >= 3:
                    kl[c] = bars
        except Exception:
            continue

    names = {p["symbol"]: p.get("name", "") for p in pool}
    # 预开仓竞价确认(TK动量对齐)
    confirm = confirm_preopen(today, by_code, kl)
    confirmed_list = [c for c in confirm.values() if c.get("confirmed")]
    cpath = os.path.join(SVC, "logs", "auction_confirm_%s.json" % today)
    with open(cpath, "w") as f:
        json.dump({"date": today,
                   "as_of": datetime.now(BJ).strftime("%Y-%m-%d %H:%M:%S"),
                   "strategy_version": "v1.1",
                   "confirm": confirm}, f, ensure_ascii=False, indent=1)
    strong, notes = [], []
    for code, ss in sorted(by_code.items()):
        # 只看 9:20 之后的真实挂单
        real = [s for s in ss if s["t"] >= "09:20:00"] or ss
        prices = [s["price"] for s in real]
        vols = [s["vol"] for s in real]
        prev_close = real[0].get("prev_close")
        if not prev_close:
            continue
        open_px = prices[-1]
        gap = (open_px - prev_close) / prev_close * 100
        auction_low = min(prices)
        low_eq_open = abs(auction_low - open_px) / open_px <= LOW_TOL
        # 量能爬坡:最后3个采样单调不减
        v3 = vols[-3:] if len(vols) >= 3 else vols
        climb = len(v3) >= 3 and all(b >= a for a, b in zip(v3, v3[1:]))
        # 昨日量/前两日涨跌
        bars = kl.get(code, [])
        yvol = bars[-2].get("volume") if len(bars) >= 2 else None
        chgs = []
        for b in bars[-3:-1]:
            pc = b.get("previous_close") or b.get("close")
            if pc:
                chgs.append((b["close"] - pc) / pc * 100)
        killed = len(chgs) == 2 and all(c <= KILL_LINE for c in chgs)
        last_vol = vols[-1] if vols else 0
        vol_str = "%.1f万手" % (last_vol / 10000) if last_vol else "未知"
        vol_ratio = (last_vol / yvol * 100) if (yvol and last_vol) else None
        name = names.get(code, "")
        # 高位过滤:60日涨幅过大,形态再企稳也不给试错信号(防高位接盘)
        chg60 = None
        if len(bars) >= 61 and bars[-61].get("close"):
            chg60 = ((bars[-1]["close"] - bars[-61]["close"])
                     / bars[-61]["close"] * 100)
        high_pos = chg60 is not None and chg60 >= HIGH_60D

        tag = None  # (标签, 说明, 颜色emoji:🔴涨/强 🟢跌/弱 🟡微涨/观望)
        if killed and gap >= GAP_OK and low_eq_open:
            base = ("前两日%s杀跌后今日%+.1f%%不低开,竞价最低%.2f=开盘价"
                    "(开盘即最低,承接强)%s;竞价量%s%s。" % (
                        "/".join("%.1f%%" % c for c in chgs), gap,
                        auction_low,
                        ",9:20后量能爬坡" if climb else "",
                        vol_str,
                        "占昨日%.1f%%" % vol_ratio if vol_ratio else ""))
            if high_pos:
                tag = ("高位风险",
                       "60日已涨%+.1f%%,高位票,形态企稳也不碰,不接盘" % chg60,
                       "🟡")
            elif vol_ratio is not None and vol_ratio <= VOL_MILD:
                tag = ("竞价企稳",
                       base + "不追高,可小仓试错,止损开盘价-2%%(%.2f)"
                       % (open_px * 0.98), "🔴")
            elif vol_ratio is not None and vol_ratio >= VOL_BURST:
                tag = ("竞价爆量", base + "爆量,小心冲高回落", "🟡")
            else:
                tag = ("竞价企稳", base + "量能待确认,不追高,等盘中承接再定",
                       "🔴")
        if not tag:
            if gap >= HIGH_OPEN:
                tag = ("高开%+.1f%%" % gap,
                       "别追,等回踩;竞价量%s" % vol_str, "🔴")
            elif gap <= -2.0:
                tag = ("低开%.1f%%" % gap,
                       "转弱,竞价承接差,观察不急着抄", "🟢")
            elif low_eq_open and gap >= GAP_OK:
                tag = ("开盘即最低",
                       "竞价最低%.2f=开盘价%+.1f%%,承接尚可,结合量能看" % (
                           auction_low, gap), "🟡")
        if tag:
            extra = realpos.hint(code, open_px) or ""
            label, detail, emo = tag
            line = ("- %s **%s %s** %s：%s%s" % (
                emo, name, code, label, detail, extra))
            (strong if label == "竞价企稳" else notes).append(line)

    # 东财全市场竞价涨幅榜(尽力而为)
    top = eastmoney_auction_top(8)
    rank_lines = ["%s%s%+.1f%%" % (d["name"], d["symbol"], d["chg"] or 0)
                  for d in top if d["chg"] and d["chg"] > 5][:5]

    alerts = strong + notes
    with open(path + ".done", "w") as f:
        f.write("finalized %s" % datetime.now(BJ).strftime("%H:%M:%S"))
    out = {"notify": bool(alerts), "strong": len(strong),
           "notes": len(notes), "samples": len(rows)}
    if not alerts:
        return out
    title = "集合竞价 09:26|企稳%d只" % len(strong) if strong \
        else "集合竞价 09:26|%d只异动" % len(notes)
    # 标题由 push 的企业微信通道自动加粗置顶,正文不再重复
    body_lines = ["> 采样%d轮 · 只看9:20后真实挂单" % len(ts)]
    if confirmed_list:
        body_lines.append("**⚡预开仓确认**(盘前结构+竞价动量,09:27预挂单)")
        for c in confirmed_list[:5]:
            code = [k for k, v in confirm.items() if v is c][0]
            held_tag = ""
            ph = realpos.hint(code, c.get("price"))
            if ph:
                held_tag = " " + ph
            body_lines.append("- 🔴**%s %s** 高开%+.1f%% %s%s" % (
                c.get("name", ""), code,
                c.get("gap", 0), "/".join(c.get("auction_notes", [])),
                held_tag))
    if strong:
        body_lines.append("**竞价企稳**（不追高，可小仓试错）")
        body_lines.extend(strong[:6])
    if notes:
        body_lines.append("**其他异动**")
        body_lines.extend(notes[:8])
    if rank_lines:
        body_lines.append("> 全市场竞价涨幅前列：%s" % "、".join(rank_lines))
    body = "\n\n".join(body_lines)
    # 重点个股自动配图:竞价确认的配K线图(最多3只)
    chart_imgs = []
    if confirmed_list:
        try:
            from stock_charts import draw_list as _draw
            cdir = os.path.join(SVC, "logs", "charts", today)
            pairs = []
            for c in confirmed_list[:3]:
                code = [k for k, v in confirm.items() if v is c][0]
                pairs.append((code, c.get("name", "")))
            chart_imgs = _draw(pairs, cdir, max_n=3)
        except Exception as e:
            print("confirm charts fail: %s" % e, flush=True)
    try:
        push_send(title, body, images=chart_imgs)
        out["pushed"] = True
    except Exception as e:
        out["pushed"] = False
        out["push_error"] = str(e)
    return out


def one_shot(today):
    """采一轮;到点则汇总。"""
    _, pool = load_pool()
    if not pool:
        return {"action": "skip", "reason": "no_pool"}
    path = day_path(today)
    if os.path.exists(path + ".done"):
        return {"action": "skip", "reason": "done"}
    snap = sample_pool(pool)
    if snap:
        with open(path + ".jsonl", "a") as f:
            for code, s in snap.items():
                f.write(json.dumps({"code": code, **s},
                                   ensure_ascii=False) + "\n")
    hm = datetime.now(BJ).strftime("%H:%M")
    if hm >= "09:26":
        return {"action": "finalize", **finalize(today, pool)}
    return {"action": "sampled", "count": len(snap)}


def main():
    from trading_calendar import guard_trading_day
    guard_trading_day("auction_scan")
    session = "--session" in sys.argv
    now = datetime.now(BJ)
    today = now.strftime("%Y-%m-%d")
    if now.weekday() >= 5:
        print(json.dumps({"notify": False, "reason": "weekend"},
                         ensure_ascii=False))
        return
    hm = now.strftime("%H:%M")
    if session:
        hm = now.strftime("%H:%M")
        if hm < "09:00" or hm > "09:30":
            # 非竞价时段误启动直接退出,不采样不汇总
            print(json.dumps({"notify": False, "reason": "off_hours",
                              "hm": hm}, ensure_ascii=False))
            return
        # 早到则等到 9:15(定时任务可能提前5分钟触发)
        while datetime.now(BJ).strftime("%H:%M") < "09:15":
            time.sleep(20)
        while True:
            r = one_shot(today)
            print(json.dumps(r, ensure_ascii=False), flush=True)
            if r.get("action") == "finalize":
                break
            if datetime.now(BJ).strftime("%H:%M") >= "09:31":
                break
            time.sleep(60)
        return
    if not ("09:14" <= hm <= "09:30"):
        print(json.dumps({"notify": False, "reason": "off_hours", "hm": hm},
                         ensure_ascii=False))
        return
    print(json.dumps(one_shot(today), ensure_ascii=False))


if __name__ == "__main__":
    main()
