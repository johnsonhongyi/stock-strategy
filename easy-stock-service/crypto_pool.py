"""数字货币候选池(market='CRYPTO'):6只主流币打分,供 P8/P24 开仓链。
打分(0-10,沿用 min_pool_score 门禁语义):
  P25趋势门通过 +4
  近24h涨幅在 +1%~+8% +3(有动量但不过热)
  昨日成交额 > 20日均×1.5 +3(资金在场)
P26板块分对币圈无意义 -> 统一门因子记 neutral 50(不拦不保)。
"""
import datetime
import json
import os

import bars_crypto
from crypto_util import fmt_price

SVC = os.path.dirname(os.path.abspath(__file__))


def _codes():
    """当日动态宇宙;异常回退静态 6 币(保证不崩)。"""
    try:
        import crypto_universe
        return crypto_universe.universe_codes()
    except Exception:
        return json.load(open(os.path.join(SVC, "crypto_watchlist.json")))


def _liq_ok(code):
    """流动性门:24h 成交额 < 门槛 -> False(直接 SKIP 不进池);数据缺失 fail-open。"""
    try:
        import crypto_universe
        vol = crypto_universe.code_vol_24h(code)
        if vol is None:
            return True
        return vol >= crypto_universe.load_cfg()["min_vol_24h_usd"]
    except Exception:
        return True


def _p25_ok(code):
    """P25 趋势门(独立实现,不碰 paper_trade 全局):1日VWAP>3日VWAP>5日VWAP 且 昨收站上MA20。
    日K按 UTC 日切,P25定义天然适配。"""
    bars = bars_crypto.get_bars(code)
    if len(bars) < 25:
        return False
    def vwap_n(n):
        seg = bars[-n:]
        amt = sum(b["amount"] for b in seg)
        vol = sum(b["volume"] for b in seg)
        return amt / vol if vol else None
    v1, v3, v5 = vwap_n(1), vwap_n(3), vwap_n(5)
    if not (v1 and v3 and v5) or not (v1 > v3 > v5):
        return False
    closes = [b["close"] for b in bars[-20:]]
    return closes[-1] >= sum(closes) / 20


def build():
    out = []
    skipped = []
    for code in _codes():
        if not _liq_ok(code):
            skipped.append({"code": code, "reason": "流动性不足"})
            continue
        try:
            bars = bars_crypto.get_bars(code)
            if len(bars) < 25:
                continue
            last, prev = bars[-1], bars[-2]
            chg24 = (last["close"] / prev["close"] - 1) * 100
            score = 0
            tags = []
            p25 = _p25_ok(code)
            if p25:
                score += 4
                tags.append("趋势")
            if 1.0 <= chg24 <= 8.0:
                score += 3
                tags.append("动量+%.1f%%" % chg24)
            vols = [b["amount"] for b in bars[-20:]]
            if vols and last["amount"] and sum(vols) / len(vols) > 0 and \
                    last["amount"] > sum(vols) / len(vols) * 1.5:
                score += 3
                tags.append("放量")
            try:
                px = bars_crypto.realtime(code)
            except Exception:
                px = last["close"]
            out.append({"symbol": code, "code": code, "name": code, "score": round(score, 1),
                        "price": px, "day_chg": round(chg24, 2), "change_percent": round(chg24, 2),
                        "tags": tags, "p25": p25,
                        "f": {"ch_pos": 50, "note": "crypto_neutral"},  # 币圈无通道概念,中性
                        "plate": "crypto", "src": "crypto_pool"})
        except Exception as e:
            out.append({"code": code, "score": 0, "error": str(e)[:60]})
    out.sort(key=lambda r: r.get("score", 0), reverse=True)
    from trading_calendar import today_str
    return {"pool": out, "skipped": skipped, "date": today_str("CRYPTO"), "market": "CRYPTO"}


if __name__ == "__main__":
    for r in build()["pool"]:
        print(r)


# ============ 空头镜像打分(2026-10-03, crypto_short 策略用) ============

def s_p25_ok(bars):
    """S-P25 空头趋势门(纯函数,point-in-time):1日VWAP<3日VWAP<5日VWAP 且 昨收<MA20。
    bars=已收盘日K升序(含 date/open/high/low/close/volume/amount)。"""
    if len(bars) < 25:
        return False
    def vwap_n(n):
        seg = bars[-n:]
        amt = sum(b["amount"] for b in seg)
        vol = sum(b["volume"] for b in seg)
        return amt / vol if vol else None
    v1, v3, v5 = vwap_n(1), vwap_n(3), vwap_n(5)
    if not (v1 and v3 and v5) or not (v1 < v3 < v5):
        return False
    closes = [b["close"] for b in bars[-20:]]
    return closes[-1] < sum(closes) / 20


def score_short_bars(bars, price=None):
    """空头打分纯函数(0-10):
      S-P25 空头趋势门通过 +4
      24h 涨跌在 [-8%, -1%] +3(有下跌动量但非崩盘)
      昨日成交额 > 20日均×1.5 +3(资金在出逃)
    返回 (score, tags, p25, chg24)。"""
    if len(bars) < 25:
        return 0.0, [], False, None
    last, prev = bars[-1], bars[-2]
    chg24 = (last["close"] / prev["close"] - 1) * 100 if prev["close"] else None
    score, tags = 0, []
    p25 = s_p25_ok(bars)
    if p25:
        score += 4
        tags.append("空头趋势")
    if chg24 is not None and -8.0 <= chg24 <= -1.0:
        score += 3
        tags.append("下跌动量%+.1f%%" % chg24)
    vols = [b["amount"] for b in bars[-20:]]
    if vols and last["amount"] and sum(vols) / len(vols) > 0 and \
            last["amount"] > sum(vols) / len(vols) * 1.5:
        score += 3
        tags.append("放量出逃")
    return round(score, 1), tags, p25, (round(chg24, 2) if chg24 is not None else None)


def build_short():
    """空头候选池:6币镜像打分,同 build() schema。p25 字段记空头趋势门结果。"""
    out = []
    skipped = []
    for code in _codes():
        if not _liq_ok(code):
            skipped.append({"code": code, "reason": "流动性不足"})
            continue
        try:
            bars = bars_crypto.get_bars(code)
            score, tags, p25, chg24 = score_short_bars(bars)
            if len(bars) < 25:
                continue
            try:
                px = bars_crypto.realtime(code)
            except Exception:
                px = bars[-1]["close"]
            out.append({"symbol": code, "code": code, "name": code,
                        "score": score,
                        "price": fmt_price(px),
                        "day_chg": chg24, "change_percent": chg24,
                        "tags": tags, "p25": p25,
                        "f": {"ch_pos": 50, "note": "crypto_short_neutral"},
                        "plate": "crypto", "src": "crypto_pool_short"})
        except Exception as e:
            out.append({"code": code, "score": 0, "error": str(e)[:60]})
    out.sort(key=lambda r: r.get("score", 0), reverse=True)
    from trading_calendar import today_str
    return {"pool": out, "skipped": skipped, "date": today_str("CRYPTO"), "market": "CRYPTO",
            "side": "short"}
