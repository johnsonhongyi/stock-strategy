"""数字货币动态宇宙(market='CRYPTO'):每天从 Kraken 全量 Ticker 按 24h 成交额选流动性好的币种。
静态 6 币(BTC/ETH/SOL/XRP/DOGE/ADA) + 动态 TopN,总数上限 18。
paper only,不碰实盘。宇宙落盘 logs/crypto_universe_<UTC日期>.json,pair 映射持久化 crypto_pairs.json。
"""
import market_cache
import datetime
import json
import os
import time
import urllib.request

SVC = os.path.dirname(os.path.abspath(__file__))
STATIC = ["BTC", "ETH", "SOL", "XRP", "DOGE", "ADA"]
PAIRS_FILE = os.path.join(SVC, "crypto_pairs.json")
ASSETS_FILE = os.path.join(SVC, "crypto_assets.json")
UA = {"User-Agent": "Mozilla/5.0"}

# 稳定币/法币:直接剔除(altname 口径)
STABLES = {"USDT", "USDC", "DAI", "PYUSD", "USDP", "TUSD", "FDUSD",
           "USD", "EUR", "GBP", "JPY", "CAD", "AUD", "CHF", "EURT", "EURI"}
# 杠杆代币后缀
LEVER_SUFFIX = ("3L", "3S", "2L", "2S", "BULL", "BEAR")


def _get(url, timeout=30):
    req = urllib.request.Request(url, headers=UA)
    return json.load(market_cache.urlopen(req, timeout=timeout))


def load_cfg():
    """strategy.yaml crypto_universe 段,缺失时用默认值兜底。"""
    cfg = {"min_vol_24h_usd": 20000000, "dynamic_top_n": 12,
           "max_total": 18, "backfill_days": 180}
    try:
        import yaml
        y = yaml.safe_load(open(os.path.join(SVC, "strategy.yaml")))
        cfg.update((y or {}).get("crypto_universe") or {})
    except Exception:
        pass
    return cfg


def fetch_assets():
    """Kraken Assets 全量(缓存到文件,base_key -> altname)。"""
    try:
        d = _get("https://api.kraken.com/0/public/Assets")
        assets = {k: v.get("altname", k) for k, v in d["result"].items()}
        json.dump(assets, open(ASSETS_FILE, "w"))
        return assets
    except Exception:
        if os.path.exists(ASSETS_FILE):
            return json.load(open(ASSETS_FILE))
        raise


def norm_code(base_key, assets):
    """base_key(XXBT)->系统 code(BTC)。XBT/DOGE 做历史兼容。"""
    alt = assets.get(base_key, base_key)
    if alt == "XBT":
        return "BTC"
    if alt == "XDG":
        return "DOGE"
    return alt


def is_stablecoin(code):
    return code in STABLES


def is_leveraged(code):
    return code.endswith(LEVER_SUFFIX)


def fetch_tickers():
    """全量 Ticker,一次调用。返回 {pair_key: ticker}。"""
    d = _get("https://api.kraken.com/0/public/Ticker")
    if d.get("error"):
        raise RuntimeError("kraken ticker error: %s" % d["error"])
    return d["result"]


def _split_key(key):
    """pair_key -> (base_key, quoted_usd)。非 USD 计价返回 None。"""
    if "." in key:
        return None
    if key.endswith("ZUSD"):
        return key[:-4], True
    if key.endswith("USD"):
        return key[:-3], True
    return None


def select_universe(rows, cfg):
    """纯函数:rows=[{key,code,vol_24h_usd}] -> (selected, dropped)。
    selected:静态6币 + 动态TopN(总数≤max_total),每项 {code,pair,vol_24h_usd,reason}。
    dropped:每项 {code,reason}。"""
    min_vol = cfg["min_vol_24h_usd"]
    topn = cfg["dynamic_top_n"]
    cap = cfg["max_total"]
    seen, selected, dropped = set(), [], []
    for r in rows:
        code = r["code"]
        if code in seen:
            continue
        seen.add(code)
        if is_stablecoin(code):
            dropped.append({"code": code, "reason": "稳定币/法币"})
            continue
        if is_leveraged(code):
            dropped.append({"code": code, "reason": "杠杆代币"})
            continue
        if r["vol_24h_usd"] < min_vol:
            dropped.append({"code": code, "reason": "24h成交额%.1fM<门槛%.0fM" %
                            (r["vol_24h_usd"] / 1e6, min_vol / 1e6)})
            continue
        selected.append(r)
    selected.sort(key=lambda r: r["vol_24h_usd"], reverse=True)
    static_rows = [r for r in selected if r["code"] in STATIC]
    dyn_rows = [r for r in selected if r["code"] not in STATIC][:topn]
    universe = static_rows + dyn_rows
    universe = universe[:cap]
    for r in universe:
        r["reason"] = "静态" if r["code"] in STATIC else "动态Top"
    # 没进宇宙但过门槛的,记为落选(名额/上限)
    in_u = {r["code"] for r in universe}
    for r in selected:
        if r["code"] not in in_u:
            dropped.append({"code": r["code"], "reason": "TopN/上限之外"})
    return universe, dropped


def build_rows(tickers, assets):
    rows = []
    for key, t in tickers.items():
        sp = _split_key(key)
        if not sp:
            continue
        base_key, _ = sp
        code = norm_code(base_key, assets)
        try:
            last = float(t["c"][0])
            vol24 = float(t["v"][1])
        except Exception:
            continue
        if last <= 0:
            continue
        rows.append({"key": key, "code": code, "vol_24h_usd": vol24 * last})
    return rows


def save_pairs(universe):
    """code->pair 持久化(供 bars_crypto.get_pair),只增不减。"""
    cur = {}
    if os.path.exists(PAIRS_FILE):
        try:
            cur = json.load(open(PAIRS_FILE))
        except Exception:
            cur = {}
    for r in universe:
        cur.setdefault(r["code"], r["key"])
    json.dump(cur, open(PAIRS_FILE, "w"), indent=1)
    return cur


def universe_path(date=None):
    if date is None:
        date = datetime.datetime.now(datetime.timezone.utc).date().isoformat()
    return os.path.join(SVC, "logs", "crypto_universe_%s.json" % date)


def universe_codes(date=None):
    """当日宇宙 code 列表;文件缺失回退静态 6 币(保证不崩)。"""
    p = universe_path(date)
    if os.path.exists(p):
        try:
            d = json.load(open(p))
            codes = [r["code"] for r in d.get("universe", [])]
            if codes:
                return codes
        except Exception:
            pass
    return list(STATIC)


def code_vol_24h(code, date=None):
    """某币当日 24h 成交额(美元);缺失返回 None(调用方 fail-open 跳过门禁)。"""
    p = universe_path(date)
    if os.path.exists(p):
        try:
            for r in json.load(open(p)).get("universe", []):
                if r["code"] == code:
                    return r.get("vol_24h_usd")
        except Exception:
            pass
    return None


def backfill_new(codes, days=180, quiet=False):
    """新币回填 days 天日K;已有≥days*0.9 天则跳过(seeded 防重复)。返回 {code: n/skip}。"""
    import bars_crypto
    out = {}
    for code in codes:
        try:
            have = len(bars_crypto.get_bars(code))
            if have >= int(days * 0.9):
                out[code] = "seeded_skip(%d)" % have
                continue
            n = bars_crypto.backfill_days(code, days=days, quiet=True)
            out[code] = n
            if not quiet:
                print(code, "backfill", n)
        except Exception as e:
            out[code] = "FAIL:%s" % str(e)[:60]
        time.sleep(2)  # Kraken 限频
    return out


def refresh():
    """主入口:拉榜->选宇宙->落盘->pair持久化->新币回填。返回宇宙文件路径。"""
    cfg = load_cfg()
    assets = fetch_assets()
    tickers = fetch_tickers()
    rows = build_rows(tickers, assets)
    universe, dropped = select_universe(rows, cfg)
    date = datetime.datetime.now(datetime.timezone.utc).date().isoformat()
    doc = {"date": date, "strategy_cfg": cfg,
           "universe": universe, "dropped": dropped[:50],
           "dropped_total": len(dropped), "tickers_total": len(tickers)}
    p = universe_path(date)
    json.dump(doc, open(p, "w"), ensure_ascii=False, indent=1)
    save_pairs(universe)
    new_codes = [r["code"] for r in universe if r["code"] not in STATIC]
    bf = backfill_new(new_codes, days=cfg["backfill_days"])
    doc["backfill"] = bf
    json.dump(doc, open(p, "w"), ensure_ascii=False, indent=1)
    print("universe %s: %d coins (%d static + %d dynamic), dropped %d" %
          (date, len(universe), len([r for r in universe if r["code"] in STATIC]),
           len([r for r in universe if r["code"] not in STATIC]), len(dropped)))
    for r in universe:
        print("  %-8s %10.1fM USD  %s" % (r["code"], r["vol_24h_usd"] / 1e6, r["reason"]))
    return p


if __name__ == "__main__":
    import sys
    if len(sys.argv) > 1 and sys.argv[1] == "--refresh":
        refresh()
    else:
        print("codes:", universe_codes())
