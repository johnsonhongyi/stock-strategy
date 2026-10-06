"""数字货币日线/小时线底座(market='CRYPTO')。
数据源: Kraken 公开 OHLC API(免key,单次可拉721根日K;Binance 451被墙不用)。
"日"定义:UTC 自然日(00:00-24:00 UTC),Kraken 日K本来就是按 UTC 切的,
1/3/5日VWAP、MA20、P25趋势门等全部按 UTC 日原样复用,零改动。
volume单位=币,amount=美元成交额。日线落 daily_bars(market='CRYPTO')。
"""
import market_cache
import data_store
import datetime
import json
import sqlite3
import time
import urllib.request

import os
SVC = os.path.dirname(os.path.abspath(__file__))
DB = data_store.DB_PATH

# code -> Kraken pair(OHLC 用),静态 6 币硬编码;动态币走 crypto_pairs.json(见下 get_pair)
PAIRS = {
    "BTC": "XBTUSD", "ETH": "ETHUSD", "SOL": "SOLUSD",
    "XRP": "XRPUSD", "DOGE": "DOGEUSD", "ADA": "ADAUSD",
}
PAIRS_FILE = os.path.join(SVC, "crypto_pairs.json")


def get_pair(code):
    """code -> Kraken pair。静态硬编码;动态币读 crypto_pairs.json;都没有则试 {CODE}USD 直连验证并缓存。"""
    if code in PAIRS:
        return PAIRS[code]
    try:
        m = json.load(open(PAIRS_FILE))
        if code in m:
            return m[code]
    except Exception:
        pass
    pair = code + "USD"
    d = _get("https://api.kraken.com/0/public/OHLC?pair=%s&interval=1440" % pair)
    if d.get("error"):
        raise RuntimeError("no kraken pair for %s: %s" % (code, d["error"]))
    try:
        m = json.load(open(PAIRS_FILE))
    except Exception:
        m = {}
    m[code] = pair
    json.dump(m, open(PAIRS_FILE, "w"), indent=1)
    return pair
# code -> Kraken Ticker 结果 key(别名,硬编码不模糊匹配)
TICKER_KEYS = {
    "BTC": "XXBTZUSD", "ETH": "XETHZUSD", "SOL": "SOLUSD",
    "XRP": "XXRPZUSD", "DOGE": "XDGUSD", "ADA": "ADAUSD",
}
UA = {"User-Agent": "Mozilla/5.0"}


def _get(url, timeout=20, force_refresh=False):
    req = urllib.request.Request(url, headers=UA)
    return json.load(market_cache.urlopen(req, timeout=timeout, force_refresh=force_refresh))


def kraken_ohlc(code, interval=1440, force_refresh=False):
    """interval:1440=日K,60=小时K。返回 [(utc_date/open_ts, o,h,l,c, volume), ...] 按时间升序。
    注意:Kraken 最后一根是" forming 中"的 K 线(未收盘),调用方自行判断。"""
    pair = get_pair(code)
    d = _get("https://api.kraken.com/0/public/OHLC?pair=%s&interval=%d" % (pair, interval),
             force_refresh=force_refresh)
    if d.get("error"):
        raise RuntimeError("kraken error: %s" % d["error"])
    key = [k for k in d["result"] if k != "last"][0]
    out = []
    for t, o, h, l, c, vwap, vol, cnt in d["result"][key]:
        out.append((int(t), float(o), float(h), float(l), float(c), float(vol)))
    return out


def _db():
    from bars import db as shared_db
    return shared_db()


def backfill_one(code, quiet=False):
    """拉1年日K入库(逐币,上游限频调用方sleep)。"""
    rows = kraken_ohlc(code, 1440, force_refresh=True)
    # 去掉 forming 中的最后一根(其时间戳日期=今天UTC,未收盘)
    utc_today = datetime.datetime.now(datetime.timezone.utc).date().isoformat()
    rows = [r for r in rows
            if datetime.datetime.fromtimestamp(r[0], datetime.timezone.utc).date().isoformat() < utc_today]
    c = _db()
    n = 0
    for ts, o, h, l, cl, vol in rows:
        day = datetime.datetime.fromtimestamp(ts, datetime.timezone.utc).date().isoformat()
        amount = cl * vol
        c.execute("""INSERT OR REPLACE INTO daily_bars
            (code,date,open,high,low,close,prev_close,volume,amount,source,market)
            VALUES(?,?,?,?,?,?,?,?,?,?,?)""",
            (code, day, o, h, l, cl, None, vol, amount, "kraken", "CRYPTO"))
        n += 1
    # prev_close 回填
    dates = c.execute(
        "SELECT date,close FROM daily_bars WHERE code=? AND market='CRYPTO' ORDER BY date",
        (code,)).fetchall()
    for i in range(1, len(dates)):
        c.execute("UPDATE daily_bars SET prev_close=? WHERE code=? AND date=? AND market='CRYPTO'",
                  (dates[i-1][1], code, dates[i][0]))
    c.commit()
    if not quiet:
        print("%s backfill %d bars, last=%s" % (code, n, dates[-1][0] if dates else None))
    return n


def backfill_days(code, days=180, quiet=False):
    """拉近 days 天日K入库(新宇宙币种用)。已有≥days*0.9 天则跳过(seeded 防重复)。"""
    have = len(_read_bars(code))
    if have >= int(days * 0.9):
        return 0
    rows = kraken_ohlc(code, 1440, force_refresh=True)
    utc_today = datetime.datetime.now(datetime.timezone.utc).date().isoformat()
    rows = [r for r in rows
            if datetime.datetime.fromtimestamp(r[0], datetime.timezone.utc).date().isoformat() < utc_today]
    rows = rows[-days:]
    c = _db()
    for ts, o, h, l, cl, vol in rows:
        day = datetime.datetime.fromtimestamp(ts, datetime.timezone.utc).date().isoformat()
        c.execute("""INSERT OR REPLACE INTO daily_bars
            (code,date,open,high,low,close,prev_close,volume,amount,source,market)
            VALUES(?,?,?,?,?,?,?,?,?,?,?)""",
            (code, day, o, h, l, cl, None, vol, cl * vol, "kraken", "CRYPTO"))
    dates = c.execute(
        "SELECT date,close FROM daily_bars WHERE code=? AND market='CRYPTO' ORDER BY date",
        (code,)).fetchall()
    for i in range(1, len(dates)):
        c.execute("UPDATE daily_bars SET prev_close=? WHERE code=? AND date=? AND market='CRYPTO'",
                  (dates[i-1][1], code, dates[i][0]))
    c.commit()
    if not quiet:
        print("%s backfill_days %d bars, last=%s" % (code, len(rows), dates[-1][0] if dates else None))
    return len(rows)


def _universe_codes():
    """当日动态宇宙;文件缺失回退静态 PAIRS。"""
    try:
        import crypto_universe
        legacy = crypto_universe.universe_codes()
    except Exception:
        legacy = list(PAIRS)
    c = data_store.connect()
    try:
        c.execute("""CREATE TABLE IF NOT EXISTS stock_tracking(
            market TEXT NOT NULL, code TEXT NOT NULL, name TEXT NOT NULL DEFAULT '',
            enabled INTEGER NOT NULL DEFAULT 1, source TEXT NOT NULL DEFAULT 'manual',
            updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, PRIMARY KEY(market,code))""")
        for code in legacy:
            c.execute("""INSERT OR IGNORE INTO stock_tracking
                (market,code,name,enabled,source) VALUES('CRYPTO',?,'',1,'auto-discovered')""", (str(code).upper(),))
        c.commit()
        rows = c.execute("SELECT code FROM stock_tracking WHERE market='CRYPTO' AND enabled=1 ORDER BY code").fetchall()
        return [row[0] for row in rows]
    finally:
        c.close()


def append_all(codes=None):
    """每日迭代:upsert 最近5个 UTC 日(幂等)。Kraken 日K按UTC收盘,UTC 00:05后跑。
    codes 缺省走当日动态宇宙(回退静态6币)。"""
    c = _db()
    for code in (codes or _universe_codes()):
        try:
            rows = kraken_ohlc(code, 1440, force_refresh=True)
            utc_today = datetime.datetime.now(datetime.timezone.utc).date().isoformat()
            rows = [r for r in rows
                    if datetime.datetime.fromtimestamp(r[0], datetime.timezone.utc).date().isoformat() < utc_today]
            for ts, o, h, l, cl, vol in rows[-5:]:
                day = datetime.datetime.fromtimestamp(ts, datetime.timezone.utc).date().isoformat()
                c.execute("""INSERT OR REPLACE INTO daily_bars
                    (code,date,open,high,low,close,prev_close,volume,amount,source,market)
                    VALUES(?,?,?,?,?,?,?,?,?,?,?)""",
                    (code, day, o, h, l, cl, None, vol, cl * vol, "kraken", "CRYPTO"))
            dates = c.execute(
                "SELECT date,close FROM daily_bars WHERE code=? AND market='CRYPTO' ORDER BY date",
                (code,)).fetchall()
            for i in range(1, len(dates)):
                c.execute("UPDATE daily_bars SET prev_close=? WHERE code=? AND date=? AND market='CRYPTO'",
                          (dates[i-1][1], code, dates[i][0]))
            c.commit()
            print(code, "append ok, last=", dates[-1][0] if dates else None)
        except Exception as e:
            print(code, "append FAIL:", str(e)[:80])
        time.sleep(2)  # Kraken 限频,温柔一点


def _read_bars(code, start=None, end=None):
    c = _db()
    try:
        q = "SELECT date,open,high,low,close,prev_close,volume,amount FROM daily_bars WHERE code=? AND market='CRYPTO'"
        args = [code]
        if start:
            q += " AND date>=?"; args.append(start)
        if end:
            q += " AND date<=?"; args.append(end)
        q += " ORDER BY date"
        cols = ["date", "open", "high", "low", "close", "prev_close", "volume", "amount"]
        return [dict(zip(cols, r)) for r in c.execute(q, args).fetchall()]
    finally:
        c.close()


def get_bars(code, start=None, end=None):
    """读本地日K;首次缺失时回填并落库,之后重复读取本地。"""
    bars = _read_bars(code, start, end)
    if not bars and not _read_bars(code):
        backfill_days(code, quiet=True)
        bars = _read_bars(code, start, end)
    return bars


def intraday_hourly(code, n=40):
    """近 n 根小时K(盘中结构用,替代美股5分钟K)。
    返回 [{time(UTC 'YYYY-MM-DD HH:MM'),open,high,low,close,volume,amount}] 升序。
    最后一根 forming 中,保留(实时性优先,调用方知晓)。"""
    rows = kraken_ohlc(code, 60)[-n:]
    out = []
    for ts, o, h, l, cl, vol in rows:
        t = datetime.datetime.fromtimestamp(ts, datetime.timezone.utc).strftime("%Y-%m-%d %H:%M")
        out.append({"time": t, "open": o, "high": h, "low": l, "close": cl,
                    "volume": vol, "amount": cl * vol,
                    "meta": {"source": "kraken"}})  # volume单位=币,不是手
    return out


def realtime(code):
    """实时价:Kraken Ticker 最后一笔。"""
    pair = get_pair(code)
    d = _get("https://api.kraken.com/0/public/Ticker?pair=" + pair)
    if d.get("error"):
        raise RuntimeError("kraken ticker error: %s" % d["error"])
    key = [k for k in d["result"] if k != "last"][0]
    return float(d["result"][key]["c"][0])


def realtime_all():
    """一次查全币种实时价 {code: price}。"""
    d = _get("https://api.kraken.com/0/public/Ticker?pair=" +
             ",".join(PAIRS.values()))
    out = {}
    if d.get("error"):
        return out
    res = d["result"]
    for code, key in TICKER_KEYS.items():
        try:
            out[code] = float(res[key]["c"][0])
        except Exception:
            pass
    return out


if __name__ == "__main__":
    import sys
    if len(sys.argv) > 1 and sys.argv[1] == "--append":
        append_all()
    else:
        for code in PAIRS:
            backfill_one(code)
            time.sleep(3)
