"""数字货币日线/小时线底座(market='CRYPTO')。
数据源: Binance 公共行情专用 API 优先,Kraken 公开 OHLC API 回退。
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
import urllib.parse
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
BINANCE_BASE = "https://data-api.binance.vision"
BINANCE_INTERVALS = {1440: "1d", 60: "1h"}


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
    d = _get("https://api.kraken.com/0/public/OHLC?pair=%s&interval=1440" % pair, timeout=8)
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


def _kraken_ohlc(code, interval=1440, force_refresh=False):
    """interval:1440=日K,60=小时K。返回 [(utc_date/open_ts, o,h,l,c, volume), ...] 按时间升序。
    注意:Kraken 最后一根是" forming 中"的 K 线(未收盘),调用方自行判断。"""
    pair = get_pair(code)
    d = _get("https://api.kraken.com/0/public/OHLC?pair=%s&interval=%d" % (pair, interval),
             timeout=8, force_refresh=force_refresh)
    if d.get("error"):
        raise RuntimeError("kraken error: %s" % d["error"])
    key = [k for k in d["result"] if k != "last"][0]
    out = []
    for t, o, h, l, c, vwap, vol, cnt in d["result"][key]:
        out.append((int(t), float(o), float(h), float(l), float(c), float(vol)))
    return out


def _binance_ohlc(code, interval=1440, force_refresh=False):
    """Binance public market-data klines; timestamps and daily candles use UTC."""
    binance_interval = BINANCE_INTERVALS.get(int(interval))
    if not binance_interval:
        raise ValueError("unsupported crypto interval: %s" % interval)
    params = urllib.parse.urlencode({
        "symbol": str(code).upper() + "USDT",
        "interval": binance_interval,
        "limit": 1000,
    })
    data = _get(BINANCE_BASE + "/api/v3/klines?" + params,
                timeout=12, force_refresh=force_refresh)
    if isinstance(data, dict):
        raise RuntimeError("Binance kline error: %s" % data.get("msg", data))
    if not isinstance(data, list) or not data:
        raise RuntimeError("Binance returned no klines for %sUSDT" % code)
    rows = []
    for item in data:
        if not isinstance(item, (list, tuple)) or len(item) < 6:
            continue
        rows.append((int(item[0]) // 1000, float(item[1]), float(item[2]),
                     float(item[3]), float(item[4]), float(item[5])))
    if not rows:
        raise RuntimeError("Binance returned malformed klines for %sUSDT" % code)
    return sorted(rows, key=lambda row: row[0])


def _ohlc_with_source(code, interval=1440, force_refresh=False):
    try:
        return _binance_ohlc(code, interval, force_refresh), "binance"
    except Exception as binance_error:
        try:
            return _kraken_ohlc(code, interval, force_refresh), "kraken"
        except Exception as kraken_error:
            raise RuntimeError("Binance failed (%s); Kraken fallback failed (%s)" %
                               (str(binance_error)[:100], str(kraken_error)[:100]))


def kraken_ohlc(code, interval=1440, force_refresh=False):
    """兼容既有调用方;实际按 Binance 优先、Kraken 回退读取 OHLC。"""
    return _ohlc_with_source(code, interval, force_refresh)[0]


def _db():
    from bars import db as shared_db
    return shared_db()


def backfill_one(code, quiet=False):
    """拉1年日K入库(逐币,上游限频调用方sleep)。"""
    rows, source = _ohlc_with_source(code, 1440, force_refresh=True)
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
            (code, day, o, h, l, cl, None, vol, amount, source, "CRYPTO"))
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
    rows, source = _ohlc_with_source(code, 1440, force_refresh=True)
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
            (code, day, o, h, l, cl, None, vol, cl * vol, source, "CRYPTO"))
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
    """当前动态宇宙 + 手动跟踪币；动态池退出的币保留历史但停止自动回补。"""
    has_snapshot = False
    try:
        import crypto_universe
        has_snapshot = os.path.isfile(crypto_universe.universe_path())
        legacy = crypto_universe.universe_codes()
    except Exception:
        legacy = list(PAIRS)
    universe = sorted({str(code).strip().upper() for code in legacy if str(code).strip()})
    if not universe:
        universe = sorted(PAIRS)
    c = data_store.connect()
    try:
        c.execute("""CREATE TABLE IF NOT EXISTS stock_tracking(
            market TEXT NOT NULL, code TEXT NOT NULL, name TEXT NOT NULL DEFAULT '',
            enabled INTEGER NOT NULL DEFAULT 1, source TEXT NOT NULL DEFAULT 'manual',
            updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, PRIMARY KEY(market,code))""")
        for code in universe:
            c.execute("""INSERT OR IGNORE INTO stock_tracking
                (market,code,name,enabled,source) VALUES('CRYPTO',?,'',1,'auto-discovered')""", (code,))
        if has_snapshot:
            placeholders = ",".join("?" for _ in universe)
            c.execute(
                "UPDATE stock_tracking SET enabled=0,updated_at=CURRENT_TIMESTAMP "
                "WHERE market='CRYPTO' AND source='auto-discovered' AND enabled=1 "
                "AND code NOT IN (%s)" % placeholders, universe)
            # Re-enable a previously auto-discovered coin if it re-enters today's pool.
            c.execute(
                "UPDATE stock_tracking SET enabled=1,updated_at=CURRENT_TIMESTAMP "
                "WHERE market='CRYPTO' AND source='auto-discovered' AND enabled=0 "
                "AND code IN (%s)" % placeholders, universe)
            rows = c.execute(
                "SELECT code FROM stock_tracking WHERE market='CRYPTO' AND enabled=1 "
                "AND (source<>'auto-discovered' OR code IN (%s)) ORDER BY code" % placeholders,
                universe).fetchall()
        else:
            # Keep the last known auto universe if today's refresh failed or has not run yet.
            rows = c.execute(
                "SELECT code FROM stock_tracking WHERE market='CRYPTO' AND enabled=1 ORDER BY code").fetchall()
        c.commit()
        return [row[0] for row in rows]
    finally:
        c.close()


def append_all(codes=None):
    """Catch up closed UTC daily bars, seed new tracked coins, and report incomplete symbols."""
    selected = sorted({str(code).strip().upper() for code in
                       (_universe_codes() if codes is None else codes) if str(code).strip()})
    if not selected:
        print("CRYPTO append failed: tracked universe is empty", flush=True)
        return 1

    utc_today = datetime.datetime.now(datetime.timezone.utc).date()
    expected_day = utc_today - datetime.timedelta(days=1)
    expected_date = expected_day.isoformat()
    check_from = expected_day - datetime.timedelta(days=4)
    total_bars = 0
    failures = 0
    c = _db()
    for code in selected:
        try:
            rows, source = _ohlc_with_source(code, 1440, force_refresh=True)
            rows = [r for r in rows if
                    datetime.datetime.fromtimestamp(r[0], datetime.timezone.utc).date() < utc_today]
            if not rows:
                raise RuntimeError("Kraken returned no closed daily bars")

            row_days = {datetime.datetime.fromtimestamp(r[0], datetime.timezone.utc).date() for r in rows}
            latest_day = max(row_days)
            if latest_day != expected_day:
                raise RuntimeError("latest=%s expected=%s" % (latest_day.isoformat(), expected_date))
            first_day = min(row_days)
            required_from = max(check_from, first_day)
            missing = [day.isoformat() for day in
                       (required_from + datetime.timedelta(days=i)
                        for i in range((expected_day - required_from).days + 1))
                       if day not in row_days]
            if missing:
                raise RuntimeError("recent daily bars missing: %s" % ",".join(missing))

            local_count = c.execute(
                "SELECT COUNT(*) FROM daily_bars WHERE code=? AND market='CRYPTO'", (code,)
            ).fetchone()[0]
            # A new/empty tracked coin gets an initial 180-day seed in this same request.
            write_rows = rows[-180:] if local_count < 162 else rows[-5:]
            for ts, o, h, low, cl, vol in write_rows:
                if min(float(o), float(h), float(low), float(cl)) <= 0 or float(h) < float(low) or float(vol) < 0:
                    raise RuntimeError("invalid OHLC data at %s" %
                                       datetime.datetime.fromtimestamp(ts, datetime.timezone.utc).date())
                day = datetime.datetime.fromtimestamp(ts, datetime.timezone.utc).date().isoformat()
                c.execute("""INSERT OR REPLACE INTO daily_bars
                    (code,date,open,high,low,close,prev_close,volume,amount,source,market)
                    VALUES(?,?,?,?,?,?,?,?,?,?,?)""",
                    (code, day, o, h, low, cl, None, vol, cl * vol, source, "CRYPTO"))
            dates = c.execute(
                "SELECT date,close FROM daily_bars WHERE code=? AND market='CRYPTO' ORDER BY date",
                (code,)).fetchall()
            repair_from = max(1, len(dates) - len(write_rows))
            for i in range(repair_from, len(dates)):
                c.execute("UPDATE daily_bars SET prev_close=? WHERE code=? AND date=? AND market='CRYPTO'",
                          (dates[i-1][1], code, dates[i][0]))
            c.commit()
            total_bars += len(write_rows)
            print(code, "append ok, bars=%d last=%s seeded=%s" %
                  (len(write_rows), dates[-1][0] if dates else None, local_count < 162), flush=True)
        except Exception as e:
            c.rollback()
            failures += 1
            print(code, "append FAIL:", str(e)[:120], flush=True)
        time.sleep(2)  # 留出上游限频余量
    c.close()
    print("CRYPTO append finished: symbols=%d bars=%d failures=%d expected=%s" %
          (len(selected), total_bars, failures, expected_date), flush=True)
    return failures


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
    rows, source = _ohlc_with_source(code, 60)
    rows = rows[-n:]
    out = []
    for ts, o, h, l, cl, vol in rows:
        t = datetime.datetime.fromtimestamp(ts, datetime.timezone.utc).strftime("%Y-%m-%d %H:%M")
        out.append({"time": t, "open": o, "high": h, "low": l, "close": cl,
                    "volume": vol, "amount": cl * vol,
                    "meta": {"source": source}})  # volume单位=币,不是手
    return out


def realtime(code):
    """实时价:Binance USDT ticker 优先,Kraken Ticker 回退。"""
    symbol = urllib.parse.quote(str(code).upper() + "USDT")
    try:
        data = _get(BINANCE_BASE + "/api/v3/ticker/price?symbol=" + symbol, timeout=8)
        return float(data["price"])
    except Exception as binance_error:
        try:
            pair = get_pair(code)
            data = _get("https://api.kraken.com/0/public/Ticker?pair=" + pair, timeout=8)
            if data.get("error"):
                raise RuntimeError(data["error"])
            key = [k for k in data["result"] if k != "last"][0]
            return float(data["result"][key]["c"][0])
        except Exception as kraken_error:
            raise RuntimeError("Binance ticker failed (%s); Kraken fallback failed (%s)" %
                               (str(binance_error)[:100], str(kraken_error)[:100]))


def realtime_all():
    """一次查询静态币种实时价 {code: price};上游失败时回退 Kraken。"""
    codes = sorted(PAIRS)
    symbols = json.dumps([code + "USDT" for code in codes], separators=(",", ":"))
    query = urllib.parse.urlencode({"symbols": symbols})
    try:
        data = _get(BINANCE_BASE + "/api/v3/ticker/price?" + query, timeout=8)
        out = {item["symbol"][:-4]: float(item["price"])
               for item in data if item.get("symbol", "").endswith("USDT")}
        if out:
            return out
    except Exception:
        pass
    try:
        d = _get("https://api.kraken.com/0/public/Ticker?pair=" +
                 ",".join(PAIRS.values()), timeout=8)
    except Exception:
        return {}
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
        if append_all():
            sys.exit(1)
    else:
        for code in PAIRS:
            backfill_one(code)
            time.sleep(3)
