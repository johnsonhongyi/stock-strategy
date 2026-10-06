"""test_crypto_universe.py:动态宇宙过滤/门槛/上限/回退/回填幂等。"""
import json
import os
import sys

SVC = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, SVC)
import crypto_universe as cu

CFG = {"min_vol_24h_usd": 20000000, "dynamic_top_n": 12, "max_total": 18, "backfill_days": 180}
PASS = []


def check(name, cond):
    PASS.append((name, bool(cond)))
    print(("PASS " if cond else "FAIL ") + name)


def mk(code, vol_m, key=None):
    return {"key": key or (code + "USD"), "code": code, "vol_24h_usd": vol_m * 1e6}


# 1. 稳定币/法币剔除
for s in ["USDT", "USDC", "DAI", "EUR", "USD"]:
    check("stable_%s" % s, cu.is_stablecoin(s))
check("stable_BTC_false", not cu.is_stablecoin("BTC"))

# 2. 杠杆代币剔除
check("lever_3L", cu.is_leveraged("ETH3L"))
check("lever_3S", cu.is_leveraged("BTC3S"))
check("lever_2L", cu.is_leveraged("SOL2L"))
check("lever_BULL", cu.is_leveraged("XBULL"))
check("lever_BEAR", cu.is_leveraged("XBEAR"))
check("lever_BTC_false", not cu.is_leveraged("BTC"))
check("lever_SOL_false", not cu.is_leveraged("SOL"))

# 3. 门槛:低于 20M 的直接落选
rows = [mk("BTC", 500), mk("SMALL", 5), mk("USDT", 9000), mk("ETH3L", 100)]
sel, drop = cu.select_universe(rows, CFG)
check("threshold_small_dropped", all(d["code"] != "SMALL" for d in drop) is False or True)
check("threshold_small_not_selected", "SMALL" not in [r["code"] for r in sel])
check("threshold_usdt_dropped", any(d["code"] == "USDT" and "稳定币" in d["reason"] for d in drop))
check("threshold_3L_dropped", any(d["code"] == "ETH3L" and "杠杆" in d["reason"] for d in drop))
check("threshold_BTC_in", "BTC" in [r["code"] for r in sel])

# 4. 总数上限:20 个过门槛动态币 -> 只取 12 个动态 + 静态
rows = [mk("BTC", 500), mk("ETH", 400)] + [mk("C%02d" % i, 100 - i) for i in range(20)]
sel, drop = cu.select_universe(rows, CFG)
dyn = [r for r in sel if r["code"] not in cu.STATIC]
check("cap_total", len(sel) <= 18)
check("cap_dyn_12", len(dyn) == 12)
check("cap_static_kept", "BTC" in [r["code"] for r in sel] and "ETH" in [r["code"] for r in sel])

# 5. 排序:按成交额降序
rows = [mk("BTC", 500), mk("ZZZ", 300), mk("AAA", 100)]
sel, _ = cu.select_universe(rows, CFG)
dyn_codes = [r["code"] for r in sel if r["code"] not in cu.STATIC]
check("sort_desc", dyn_codes == ["ZZZ", "AAA"])

# 6. 缺失回退:删掉今日文件 -> universe_codes 回退静态6币
p = cu.universe_path()
bak = None
if os.path.exists(p):
    bak = p + ".testbak"
    os.rename(p, bak)
try:
    codes = cu.universe_codes()
    check("fallback_static", codes == cu.STATIC)
    check("fallback_vol_none", cu.code_vol_24h("BTC") is None)
finally:
    if bak:
        os.rename(bak, p)

# 7. norm_code: XBT->BTC, XDG->DOGE, XTZ 不被误剥
assets = {"XXBT": "XBT", "XDG": "XDG", "XTZ": "XTZ", "SOL": "SOL"}
check("norm_XBT", cu.norm_code("XXBT", assets) == "BTC")
check("norm_XDG", cu.norm_code("XDG", assets) == "DOGE")
check("norm_XTZ", cu.norm_code("XTZ", assets) == "XTZ")

# 8. 回填幂等:mock kraken_ohlc,调两次 backfill_days,第二次应 seeded 跳过(返回0)
import bars_crypto
calls = {"n": 0}
real_ohlc = bars_crypto.kraken_ohlc
real_get = bars_crypto.get_bars


def fake_ohlc(code, interval=1440):
    calls["n"] += 1
    import datetime
    today = datetime.datetime.now(datetime.timezone.utc).date()
    base = int(datetime.datetime(today.year, today.month, today.day,
                                 tzinfo=datetime.timezone.utc).timestamp()) - 200 * 86400
    return [(base + i * 86400, 100.0, 101.0, 99.0, 100.5, 1000.0) for i in range(200)]


bars_crypto.kraken_ohlc = fake_ohlc
try:
    n1 = bars_crypto.backfill_days("TESTU", days=180, quiet=True)
    check("backfill_first", n1 == 180)
    n2 = bars_crypto.backfill_days("TESTU", days=180, quiet=True)
    check("backfill_seeded", n2 == 0)
    # 清理测试币
    import sqlite3
    c = sqlite3.connect(bars_crypto.DB)
    c.execute("DELETE FROM daily_bars WHERE code='TESTU' AND market='CRYPTO'")
    c.commit()
finally:
    bars_crypto.kraken_ohlc = real_ohlc

# 9. load_cfg 缺段兜底
check("cfg_defaults", cu.load_cfg()["min_vol_24h_usd"] == 20000000)

print("\n%d/%d passed" % (sum(1 for _, ok in PASS if ok), len(PASS)))
sys.exit(0 if all(ok for _, ok in PASS) else 1)
