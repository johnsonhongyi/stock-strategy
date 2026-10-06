#!/usr/bin/env python3
"""盘前关注池:55188 异动集(板块/龙头/原因) + 数据底座(通道策略/量能/VWAP) -> 缩圈。

流程:
1. 候选 = 重点股 + 跟踪池 + 55188 异动集(yidong.py:涨停池/人气榜/选股宝异动)
2. 板块归因:选股宝 plates 接口拿板块名+异动原因;同板块内连板最高者标龙头
3. 数据底座 enrichment:通道结构(channel_structure)/量比/昨日VWAP偏离/60日涨幅
4. 缩圈打分排序 -> logs/watchpool_<date>.json + 推送 Top30

用法: python3 watchpool.py [--dry-run]
cron: 交易日 08:30 (周末脚本内静默)。
"""
import market_cache
import json
import os
import sys
import urllib.request
from datetime import datetime
from zoneinfo import ZoneInfo

BJ = ZoneInfo("Asia/Shanghai")
SVC = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, SVC)
from structure_scan import load_pool, KEY_STOCKS, HIGH_60D, STRAT_SHORT  # noqa: E402
from push import send as push_send  # noqa: E402
import yidong as yd  # noqa: E402

UA = {"User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36"}
POOL_TOPN = 30   # 关注池取前30
YIDONG_N = 120   # 异动集最多取120进打分


def fetch_plates():
    """选股宝板块:{name: description(异动原因)}"""
    try:
        req = urllib.request.Request(
            "https://flash-api.xuangubao.cn/api/surge_stock/plates", headers=UA)
        with market_cache.urlopen(req, timeout=15) as r:
            items = json.loads(r.read().decode("utf-8", "ignore"))["data"]["items"]
        return {it["name"]: it.get("description", "") for it in items}
    except Exception as e:
        print("plates fail:", e)
        return {}


def plate_of(s):
    """一只票的板块归因:选股宝 plates > 人气榜概念标签"""
    pl = s.get("surge_plates") or ""
    if isinstance(pl, list):  # 选股宝返回的是 [{"id":..,"name":".."}] 或 [".."]
        pl = pl[0] if pl else ""
        if isinstance(pl, dict):
            pl = pl.get("name", "")
    pl = str(pl).strip()
    if pl:
        return pl.split(",")[0].split("，")[0]
    tags = s.get("concept_tags") or []
    if tags:
        return tags[0]
    return ""


def board_num(s):
    return yd.board_num(s.get("high_days") or s.get("m_days_n_boards"))


def enrich(code, name, cost=None, yi=None):
    """数据底座 enrichment:通道/量比/VWAP/位置(一次 K 线拉取)"""
    import numpy as np
    from structure_scan import fetch_daily
    from channel_structure import diagnose
    kl = fetch_daily(code, limit=250)
    if not kl or len(kl) < 60:
        return None
    H = np.array([k["high"] for k in kl], dtype=float)
    L = np.array([k["low"] for k in kl], dtype=float)
    C = np.array([k["close"] for k in kl], dtype=float)
    V = np.array([k["volume"] for k in kl], dtype=float)      # 东财单位:手
    A = np.array([k.get("amount") or 0 for k in kl], dtype=float)  # 单位:元
    last = kl[-1]
    r = diagnose(H, L, C, lookback=200)
    f = r["features"]
    pct60 = (C[-1] / C[-61] - 1) * 100 if len(C) > 61 else 0.0
    vol_ratio = float(V[-1] / V[-6:-1].mean()) if V[-6:-1].mean() > 0 else 0.0
    vwap1 = float(A[-1] / (V[-1] * 100)) if V[-1] > 0 else 0.0  # 元/股
    dist_vwap = (last["close"] / vwap1 - 1) * 100 if vwap1 > 0 else 0.0
    # P23:大阳买卖点(江淮/北汽精华)。08:30跑时只用已收盘K,point-in-time。
    try:
        import dayang as _dy
        dy = _dy.dayang_signal(code)
    except Exception:
        dy = {"signal": "neutral", "reason": "err"}
    return {
        "symbol": code, "name": name, "cost": cost,
        "price": last["close"], "change_percent": last.get("change_percent"),
        "f": f, "hits": r["hits"], "pct60": pct60,
        "vol_ratio": vol_ratio, "vwap1": vwap1, "dist_vwap": dist_vwap,
        "yidong": yi, "plate": plate_of(yi) if yi else "",
        "dayang": dy.get("signal", "neutral"),
        "dayang_why": dy.get("why", dy.get("reason", "")),
    }


def score(a):
    """缩圈打分,透明可解释"""
    f, yi = a["f"], a.get("yidong") or {}
    s = 0.0
    why = []
    # 异动分
    bn = board_num(yi)
    if bn >= 2:
        s += min(bn * 1.5, 7.5); why.append("%d板" % bn)
    elif bn == 1:
        s += 2; why.append("首板")
    hr = yi.get("hot_rank") or 999
    if hr <= 20:
        s += 2; why.append("人气%d" % hr)
    elif hr <= 50:
        s += 1
    if "异动" in yi.get("sources", []):
        s += 1
    if len(yi.get("sources", [])) >= 2:
        s += 1; why.append("多源")
    # 结构分
    hits = [k for k, v in a["hits"].items() if v]
    if hits:
        s += min(len(hits) * 2, 6); why.append("策略%d" % len(hits))
    if 20 <= f["ch_pos"] <= 65:
        s += 1.5
    if not f["ch_supp_broken"]:
        s += 1
    if f["ch_dir"] == 1:
        s += 1
    # 量能分
    vr = a["vol_ratio"]
    if vr >= 2:
        s += 2; why.append("量比%.1f" % vr)
    elif vr >= 1.5:
        s += 1
    elif vr >= 1.2:
        s += 0.5
    # VWAP
    dv = abs(a["dist_vwap"])
    if dv <= 2:
        s += 1
    elif dv > 5:
        s -= 1; why.append("乖离大")
    # 高位否决
    if a["pct60"] >= HIGH_60D:
        s -= 10; why.append("高位否决")
    # P23 大阳买卖点:异动池从"跟单池"改为"大阳事件发现池",买卖点由dayang给
    dy = a.get("dayang", "neutral")
    if dy == "startup_buy":
        s += 4; why.append("大阳启动")
    elif dy == "pullback_buy":
        s += 3; why.append("大阳回踩")
    elif dy in ("miss_sell", "stop_sell"):
        s -= 3; why.append("大阳走坏")
    # 龙头加成(后标)
    if a.get("is_leader"):
        s += 2; why.append("龙头")
    a["score"] = round(s, 1)
    a["score_why"] = why
    return s


def mark_leaders(pool):
    """同板块内连板数最高者标龙头"""
    groups = {}
    for a in pool:
        pl = a.get("plate")
        if pl:
            groups.setdefault(pl, []).append(a)
    for pl, members in groups.items():
        if len(members) < 2:
            continue
        members.sort(key=lambda x: board_num(x.get("yidong") or {}), reverse=True)
        if board_num(members[0].get("yidong") or {}) >= 2:
            members[0]["is_leader"] = True


def fmt(a):
    f = a["f"]
    chg = a["change_percent"]
    head = "🔴" if (chg or 0) >= 0 else "🟢"
    chg_s = ("+%0.2f%%" % chg) if chg and chg >= 0 else ("%0.2f%%" % chg if chg else "--")
    leader = "🐲龙头 " if a.get("is_leader") else ""
    L = ["%s %s%s %s %.2f (%s) [%.1f分]" % (head, leader, a["symbol"], a["name"], a["price"], chg_s, a["score"])]
    if a.get("plate"):
        L.append("  板块: %s" % a["plate"])
    yi = a.get("yidong") or {}
    reason = yi.get("reason") or yi.get("surge_desc") or ""
    if reason:
        L.append("  原因: %s" % reason[:40])
    L.append("  量比 %.1f | VWAP偏离 %+.1f%% | 60日 %+.0f%%" % (a["vol_ratio"], a["dist_vwap"], a["pct60"]))
    L.append("  三轨 %.2f/%.2f/%.2f ch_pos %.0f%% %s" % (
        f["ch_upper"], f["ch_mid"], f["ch_lower"], f["ch_pos"],
        "支撑站稳" if not f["ch_supp_broken"] else "支撑跌破"))
    if a["pct60"] >= HIGH_60D:
        L.append("  🟡 高位风险:结构信号作废,不接盘")
    else:
        hits = [STRAT_SHORT[k] for k, v in a["hits"].items() if v]
        L.append("  策略: " + ("✅ " + "/".join(hits) if hits else "无"))
    if a.get("cost"):
        L.append("  成本 %.2f 浮亏 %+.1f%%" % (a["cost"], (a["price"] / a["cost"] - 1) * 100))
    return "\n".join(L)


def main():
    from trading_calendar import guard_trading_day
    guard_trading_day("watchpool")
    dry = "--dry-run" in sys.argv
    now = datetime.now(BJ)
    if now.weekday() >= 5 and not dry:
        print("weekend, silent")
        return
    date_s = now.strftime("%Y-%m-%d")

    # 1. 候选集
    cands, seen = [], set()
    for code, info in KEY_STOCKS.items():
        cands.append((code, info["name"], info.get("cost"), None, "重点"))
        seen.add(code)
    _, pool = load_pool()
    for p in pool:
        code = p.get("symbol", "").strip()
        if code and code not in seen:
            cands.append((code, p.get("name", ""), None, None, "跟踪"))
            seen.add(code)
    ydata = yd.build()
    for s in ydata["stocks"][:YIDONG_N]:
        code = s["code"]
        if code and code not in seen:
            cands.append((code, s.get("name", ""), None, s, "异动"))
            seen.add(code)
    # P10流转(2026-09-29补上断掉的线):昨日vwap_ride命中的票 → 次日关注池候选。
    # 用state的pushed全集(当日各轮累计),matched补名字;它们同样过enrich/高位过滤/打分全套;
    # 涨停后次日高开追高会被P8(偏离<2.5%)自然拦下,只有回踩企稳的才能进开仓链。
    try:
        from trading_calendar import prev_trading_day
        _pd = prev_trading_day(date_s, 1)
        _names, _codes, _seen_p10 = {}, [], set()
        _vrp = os.path.join(SVC, "logs", "vwap_ride_%s.json" % _pd)
        if os.path.exists(_vrp):
            for m in json.load(open(_vrp)).get("matched", []):
                _c = (m.get("symbol") or "").strip()
                if _c:
                    _names[_c] = m.get("name", "")
                    _codes.append(_c)
        _stp = os.path.join(SVC, "logs", "vwap_ride_state.json")
        if os.path.exists(_stp):
            _st = json.load(open(_stp))
            if _st.get("date") == _pd:
                for _c in _st.get("pushed", []):
                    if _c and _c not in _codes:
                        _codes.append(_c)
        _n = 0
        # 名字缺失的批量回填一次(名字影响推送展示)
        _noname = [c for c in _codes if not _names.get(c)]
        if _noname:
            try:
                import daily_review as _dr
                _syms = ",".join(
                    ("sh" if c[0] in "69" else "sz") + c for c in _noname)
                for _q in _dr.api(
                        "/api/v1/quotes/realtime?symbols=%s" % _syms,
                        timeout=15)["data"]:
                    _cc = str(_q.get("symbol", ""))[:6]
                    if _q.get("name"):
                        _names[_cc] = _q["name"]
            except Exception as e:
                print("P10名字回填跳过: %s" % e)
        for _c in _codes:
            if _c and _c not in seen and _c not in _seen_p10:
                cands.append((_c, _names.get(_c, ""), None, None, "P10流转"))
                seen.add(_c)
                _seen_p10.add(_c)
                _n += 1
        print("P10流转: %s 累计 %d 只加入候选" % (_pd, _n))
    except Exception as e:
        print("P10流转跳过: %s" % e)
    print("候选 %d 只 (重点%d/跟踪%d/异动%d/P10流转%d)" % (
        len(cands), len(KEY_STOCKS), len([c for c in cands if c[4] == "跟踪"]),
        len([c for c in cands if c[4] == "异动"]),
        len([c for c in cands if c[4] == "P10流转"])))

    # 2. enrichment
    pool_r = []
    for code, name, cost, yi, src in cands:
        try:
            a = enrich(code, name, cost, yi)
        except Exception as e:
            print("enrich fail %s: %s" % (code, e))
            continue
        if a:
            a["src"] = src
            pool_r.append(a)
    print("enrich 成功 %d 只" % len(pool_r))

    # 3. 龙头 + 打分 + 缩圈
    plates = fetch_plates()
    mark_leaders(pool_r)
    for a in pool_r:
        score(a)
    pool_r.sort(key=lambda x: x["score"], reverse=True)
    watch = pool_r[:POOL_TOPN]
    # 大阳直通(2026-09-30用户拍板"异动池的大阳启动特征跟随单"):dayang startup_buy/
    # pullback_buy的票保送进池,不被综合分截断(江淮score5.0曾被Top30截掉,导致P24无票可跟)。
    for a in pool_r[POOL_TOPN:]:
        if a.get("dayang") in ("startup_buy", "pullback_buy"):
            a["src"] = (a.get("src") or "") + "+大阳直通"
            watch.append(a)
    print("大阳直通 %d 只" % (len(watch) - POOL_TOPN))

    for a in watch:
        pl = a.get("plate")
        if pl and pl in plates and plates[pl]:
            a["plate_reason"] = plates[pl]

    out = {"date": date_s, "counts": ydata["counts"],
           "cand_total": len(pool_r), "pool_n": len(watch),
           "pool": [{k: a.get(k) for k in
                     ("symbol", "name", "price", "change_percent", "score", "score_why",
                      "plate", "plate_reason", "is_leader", "vol_ratio", "dist_vwap",
                      "pct60", "src", "dayang")} | {"f": a["f"], "hits": a["hits"]}
                    for a in watch]}
    jp = os.path.join(SVC, "logs", "watchpool_%s.json" % date_s)
    json.dump(out, open(jp, "w"), ensure_ascii=False, indent=1, default=str)

    # 4. 报告
    title = "📋 盘前关注池 %s (%d选%d)" % (now.strftime("%m-%d"), len(pool_r), len(watch))
    parts = [title, ""]
    # 板块视角
    pmap = {}
    for a in watch:
        if a.get("plate"):
            pmap.setdefault(a["plate"], []).append(a["symbol"])
    if pmap:
        parts.append("🧭 板块:")
        for pl, syms in sorted(pmap.items(), key=lambda x: -len(x[1]))[:8]:
            rs = plates.get(pl, "")
            parts.append("  · %s (%s)%s" % (pl, ",".join(syms[:5]), (" - " + rs[:30]) if rs else ""))
        parts.append("")
    for a in watch:
        parts.append(fmt(a))
        parts.append("")
    content = "\n".join(parts).strip()
    mp = os.path.join(SVC, "logs", "watchpool_%s.md" % date_s)
    open(mp, "w").write(content)
    print("saved:", jp)
    if dry:
        print(content[:3000])
        return
    # 推送版:企业微信 markdown 上限 4096 字节,超长会被拒收(text_fail)。
    # 完整30只落盘 md,推送只发板块总览+Top12。
    push_parts = [title, ""]
    if pmap:
        push_parts.append("🧭 板块:")
        for pl, syms in sorted(pmap.items(), key=lambda x: -len(x[1]))[:8]:
            rs = plates.get(pl, "")
            push_parts.append("  · %s (%s)%s" % (pl, ",".join(syms[:5]), (" - " + rs[:30]) if rs else ""))
        push_parts.append("")
    for a in watch[:12]:
        push_parts.append(fmt(a))
        push_parts.append("")
    push_parts.append("> 完整%d只见日志 watchpool_%s.md" % (len(watch), date_s))
    push_content = "\n".join(push_parts).strip()
    if len(push_content.encode("utf-8")) > 3800:
        # 极端情况再砍到 Top8
        push_parts = [title, ""]
        for a in watch[:8]:
            push_parts.append(fmt(a))
            push_parts.append("")
        push_parts.append("> 完整%d只见日志" % len(watch))
        push_content = "\n".join(push_parts).strip()
    print("push bytes:", len(push_content.encode("utf-8")))
    push_send(title, push_content)


if __name__ == "__main__":
    main()
