#!/usr/bin/env python3
"""P10「MA60回踩+VWAP ride启动」盘中扫描。

用户口述模式(掌阅科技 603533 2026-09-29 首板验证):
  日线: 连续回踩 + 振幅收窄 + 在MA60附近企稳
  盘中: 小幅高开 + 连续放量 + 沿着VWAP拉升不碰VWAP

链路: 异动集(yidong.build 实时)
  → 日线过滤: 10日最大回撤>=DDRAW_MIN、昨日收盘在MA60±MA60_DEV内
  → 盘中缩圈: 去除破VWAP的(09:45后有bar收盘<VWAP×VWAP_BRK)
  → 保留: 踩VWAP(09:45后最低价>=VWAP×VWAP_TOUCH) + 连续放量
  → 去重后推送(企业微信+息知, 正文<=3800字节)

cron: 交易时段每30分钟; 脚本内门控 09:35-11:30/13:00-15:00, 周末静默。
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
import yidong as yd  # noqa: E402

# ---- 可调阈值(用户批准后升级) ----
DDRAW_MIN = 8.0      # 10日最大回撤下限(%)
MA60_DEV = 2.5       # 昨日收盘 vs MA60 允许偏离(±%)
VWAP_BRK = 0.995     # 09:45后收盘跌破 VWAP×此系数 → 剔除
VWAP_TOUCH = 0.99    # 09:45后最低价允许下探到 VWAP×此系数
VOL_RATIO_MIN = 1.5  # 量比下限(当根5分均量/昨日5分均量)
CHG_MIN = 2.0        # 当日涨幅下限(%)
RIDE_FROM = "09:45"  # ride判定起始时间(开盘前15分钟允许轻微下探)
YIDONG_TOPN = 40     # 异动集取前40

STATE = os.path.join(SVC, "logs", "vwap_ride_state.json")


def to_api(sym):
    s = sym.strip()
    return ("sh" if s[0] in "69" else "sz") + s


def in_session(now):
    if now.weekday() >= 5:
        return False
    t = now.strftime("%H:%M")
    return ("09:35" <= t <= "11:30") or ("13:00" <= t <= "15:00")


def load_state(date_s):
    try:
        st = json.load(open(STATE))
        if st.get("date") == date_s:
            return set(st.get("pushed", []))
    except Exception:
        pass
    return set()


def save_state(date_s, pushed):
    json.dump({"date": date_s, "pushed": sorted(pushed)},
              open(STATE, "w"), ensure_ascii=False)


def daily_filter(symbols):
    """日线: 10日最大回撤 + 昨日收盘贴MA60。返回 {sym: {ddraw, ma60dev}}"""
    out = {}
    api_syms = [to_api(s) for s in symbols]
    for i in range(0, len(api_syms), 30):
        chunk = api_syms[i:i + 30]
        try:
            d = dr.api("/api/v1/quotes/kline/batch?symbols=%s&period=day&limit=70"
                       % ",".join(chunk), timeout=40)
        except Exception:
            continue
        data = d.get("data") or {}
        for api_s, kl in data.items():
            if not kl or len(kl) < 61:
                continue
            sym = api_s.split(".")[0]
            closes = [k["close"] for k in kl]
            highs = [k["high"] for k in kl]
            yclose = closes[-2]  # 昨日收(今日日K未收盘)
            ma60 = sum(closes[-61:-1]) / 60
            ma60dev = (yclose - ma60) / ma60 * 100
            peak = max(highs[-11:-1])
            trough = min(k["low"] for k in kl[-11:-1])
            ddraw = (trough - peak) / peak * 100
            if ddraw <= -DDRAW_MIN and abs(ma60dev) <= MA60_DEV:
                out[sym] = {"ddraw": round(ddraw, 1),
                            "ma60dev": round(ma60dev, 2),
                            "yclose": yclose}
    return out


def ride_check(sym, date_s):
    """盘中VWAP ride检查。返回 None(剔除) 或 stats dict。"""
    try:
        d = dr.api("/api/v1/quotes/kline?symbol=%s&period=5&limit=60"
                   % to_api(sym), timeout=30)
    except Exception:
        return None
    bars = [k for k in (d.get("data") or []) if k["time"][:10] == date_s]
    if len(bars) < 4:
        return None
    ride_bars = [k for k in bars if k["time"][11:16] >= RIDE_FROM]
    if len(ride_bars) < 2:
        return None
    cum_amt = cum_vol = 0.0
    vwaps = []
    for k in bars:
        cum_amt += k["amount"]
        cum_vol += k["volume"] * 100  # volume单位是手
        vwaps.append(cum_amt / cum_vol if cum_vol else 0)
    # 去除破VWAP的: RIDE_FROM后收盘跌破 → 剔除
    for k, v in zip(ride_bars, vwaps[len(bars) - len(ride_bars):]):
        if v and k["close"] < v * VWAP_BRK:
            return None
    # 踩VWAP: RIDE_FROM后最低价不低于 VWAP×VWAP_TOUCH
    devs = [(k["low"] - v) / v for k, v in
            zip(ride_bars, vwaps[len(bars) - len(ride_bars):]) if v]
    if not devs:
        return None
    min_low_dev = min(devs)
    if min_low_dev < VWAP_TOUCH - 1:
        return None
    last = bars[-1]
    vwap_now = vwaps[-1]
    if last["close"] < vwap_now * VWAP_BRK:
        return None
    chg = (last["close"] / bars[0]["open"] - 1) * 100 if bars[0]["open"] else 0
    # 用change_percent更准(相对昨收)
    chg = last.get("change_percent") or chg
    if chg < CHG_MIN:
        return None
    # 连续放量: 量比 + 近3根量价齐升>=2
    tot_vol = sum(k["volume"] for k in bars)
    yvol = None
    try:
        dd = dr.api("/api/v1/quotes/kline?symbol=%s&period=day&limit=2"
                    % to_api(sym), timeout=20)["data"]
        if dd and len(dd) >= 2:
            yvol = dd[-2]["volume"]
    except Exception:
        pass
    vol_ratio = (tot_vol / len(bars)) / (yvol / 48) if yvol else 0
    if vol_ratio < VOL_RATIO_MIN:
        return None
    up_n = sum(1 for a, b in zip(bars[-3:], bars[-4:-1])
               if a["close"] > b["close"] and a["volume"] > b["volume"])
    if up_n < 2 and vol_ratio < 2.0:
        return None
    return {
        "price": last["close"], "chg": round(chg, 2),
        "vwap": round(vwap_now, 3),
        "vwap_dev": round((last["close"] - vwap_now) / vwap_now * 100, 2),
        "low_dev": round(min_low_dev * 100, 2),
        "vol_ratio": round(vol_ratio, 2),
    }


def main():
    from trading_calendar import guard_trading_day
    guard_trading_day("vwap_ride")
    dry = "--dry-run" in sys.argv
    now = datetime.now(BJ)
    date_s = now.strftime("%Y-%m-%d")
    if not dry and not in_session(now):
        print("off_session, exit")
        return
    print("异动集中拉取...", flush=True)
    yd_data = yd.build()
    stocks = yd_data.get("stocks", [])[:YIDONG_TOPN]
    print("异动 %d 只" % len(stocks), flush=True)
    syms = [s["code"] for s in stocks]
    name_of = {s["code"]: s.get("name", "") for s in stocks}

    print("日线过滤(MA60回踩)...", flush=True)
    dpass = daily_filter(syms)
    print("日线通过 %d 只: %s" % (len(dpass), list(dpass)[:10]), flush=True)

    matched = []
    for sym, dinfo in dpass.items():
        r = ride_check(sym, date_s)
        if r:
            r.update(dinfo)
            r["symbol"] = sym
            r["name"] = name_of.get(sym, "")
            matched.append(r)
            print("  ✅ %s %s 现价%.2f %+.2f%% VWAP偏离%+.2f%% 量比%.1f"
                  % (sym, r["name"], r["price"], r["chg"],
                     r["vwap_dev"], r["vol_ratio"]), flush=True)
    matched.sort(key=lambda x: -x["chg"])

    outp = os.path.join(SVC, "logs", "vwap_ride_%s.json" % date_s)
    json.dump({"date": date_s, "as_of": now.strftime("%H:%M"),
               "yidong_n": len(stocks), "daily_pass_n": len(dpass),
               "matched": matched},
              open(outp, "w"), ensure_ascii=False, indent=1)
    print("saved:", outp, "matched:", len(matched))

    # 推送只取焦点前10: 按综合分排序(涨幅 + VWAP偏离×2 + 量比),防刷屏
    for m in matched:
        m["score"] = round(m["chg"] + m["vwap_dev"] * 2
                           + min(m["vol_ratio"], 10) * 0.5, 2)
    matched.sort(key=lambda x: -x["score"])
    top = matched[:10]

    pushed = load_state(date_s)
    new = [m for m in top if m["symbol"] not in pushed]
    if dry or not new:
        print("dry/no_new, no push")
        return
    title = "🛹 VWAP ride启动 %s|%d只" % (now.strftime("%H:%M"), len(new))
    parts = [title, ""]
    for m in new:
        parts.append("🔴 %s %s %.2f (%+.2f%%)" % (
            m["symbol"], m["name"], m["price"], m["chg"]))
        parts.append("  踩VWAP偏离%+.2f%%(最低%+.2f%%) | 量比%.1f" % (
            m["vwap_dev"], m["low_dev"], m["vol_ratio"]))
        parts.append("  日线: 10日回撤%.1f%% | 昨收贴MA60 %+.2f%%" % (
            m["ddraw"], m["ma60dev"]))
        parts.append("")
    content = "\n".join(parts).strip()
    if len(content.encode("utf-8")) > 3800:
        content = content.encode("utf-8")[:3800].decode("utf-8", "ignore")
    res = push_send(title, content)
    print("push:", res)
    ok = any(r.get("ok") for r in (res or []))
    if ok:
        # 推送成功才记为已见:已评估的全部标记,避免低排名的30分钟后滴漏刷屏
        pushed.update(m["symbol"] for m in matched)
        save_state(date_s, pushed)


if __name__ == "__main__":
    main()
