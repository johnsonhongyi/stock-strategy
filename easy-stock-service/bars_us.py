#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
美股日线底座:复用 bars.py 的库表与"一次打底+每日迭代"架构 (用户定,2026-09-29)。

数据源: 盘后增量优先腾讯美股日K, 东方财富和 Yahoo Finance 为备用。
  - backfill: 每只票最多拉 250 根日K,逐只拉,间隔2秒,不密集。
  - append:   每日美股收盘(16:00 ET)后拉最近5日,必须包含当日才记为成功。
单位: volume=股(美股无"手"概念),amount=美元。与A股(手/人民币)共表,按 market 区分。
表: daily_bars 增加 market 列 ('CN'/'US'),code 存原始 ticker (如 AAPL)。
"""
import market_cache
import data_store
import datetime
import json
import logging
import os
import re
import sqlite3
import sys
import time
import urllib.parse
import urllib.request

SVC = os.path.dirname(os.path.abspath(__file__))
DB = data_store.DB_PATH
WATCHLIST = os.path.join(SVC, "us_watchlist.json")
UA = {"User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36"}
try:
    from zoneinfo import ZoneInfo
    ET = ZoneInfo("America/New_York")
except Exception:
    ET = datetime.timezone(datetime.timedelta(hours=-4))


def db():
    sys.path.insert(0, SVC)
    from bars import db as _db
    c = _db()
    cols = [r[1] for r in c.execute("PRAGMA table_info(daily_bars)").fetchall()]
    if "market" not in cols:
        c.execute("ALTER TABLE daily_bars ADD COLUMN market TEXT DEFAULT 'CN'")
        c.execute("UPDATE daily_bars SET market='CN' WHERE market IS NULL")
        c.commit()
    return c


def get_universe():
    try:
        w = json.load(open(WATCHLIST))
        syms = w.get("symbols", w) if isinstance(w, dict) else w
        legacy = [str(s).upper() for s in syms if re.fullmatch(r"[A-Z.]{1,6}", str(s).upper())]
    except Exception:
        legacy = []
    c = db()
    try:
        for symbol in legacy:
            c.execute("""INSERT OR IGNORE INTO stock_tracking
                (market,code,name,enabled,source) VALUES('US',?,'',1,'auto-discovered')""", (symbol,))
        c.commit()
        rows = c.execute("SELECT code FROM stock_tracking WHERE market='US' AND enabled=1 ORDER BY code").fetchall()
        return [row[0] for row in rows]
    finally:
        c.close()


def yahoo_daily(symbol, range_="1y", force_refresh=False):
    """拉 Yahoo 日K,返回 [(date, open, high, low, close, adjclose, volume)] 按日期升序。"""
    url = ("https://query1.finance.yahoo.com/v8/finance/chart/%s?interval=1d&range=%s"
           % (urllib.request.quote(symbol), range_))
    req = urllib.request.Request(url, headers=UA)
    with market_cache.urlopen(req, timeout=30, force_refresh=force_refresh) as r:
        d = json.loads(r.read().decode("utf-8"))
    res = (d.get("chart") or {}).get("result") or []
    if not res:
        return []
    res = res[0]
    ts = res.get("timestamp") or []
    q = (res.get("indicators") or {}).get("quote") or [{}]
    q = q[0]
    adj = ((res.get("indicators") or {}).get("adjclose") or [{}])[0].get("adjclose") or []
    out = []
    for i, t in enumerate(ts):
        try:
            day = datetime.datetime.fromtimestamp(t, ET).strftime("%Y-%m-%d")
            o, h, l, c = q["open"][i], q["high"][i], q["low"][i], q["close"][i]
            if c is None:
                continue
            v = q["volume"][i] or 0
            a = adj[i] if i < len(adj) and adj[i] else c
            out.append((day, o, h, l, c, a, v))
        except (IndexError, TypeError):
            continue
    return out


def eastmoney_daily(symbol, limit=5):
    """Read US daily bars from Eastmoney's global-market K-line endpoint."""
    params = urllib.parse.urlencode({
        "secid": "105." + symbol.upper(),
        "fields1": "f1,f2,f3,f4,f5,f6",
        "fields2": "f51,f52,f53,f54,f55,f56,f57,f58,f59,f60,f61",
        "klt": "101",
        "fqt": "1",
        "end": "20500101",
        "lmt": str(max(1, min(int(limit), 1000))),
    })
    url = "https://push2his.eastmoney.com/api/qt/stock/kline/get?" + params
    req = urllib.request.Request(url, headers={
        "User-Agent": UA["User-Agent"],
        "Referer": "https://quote.eastmoney.com/",
    })
    with urllib.request.urlopen(req, timeout=30) as response:
        payload = json.loads(response.read().decode("utf-8"))
    if payload.get("rc") != 0:
        raise RuntimeError("Eastmoney K-line rc=%s for %s" % (payload.get("rc"), symbol))
    lines = ((payload.get("data") or {}).get("klines") or [])
    out = []
    for raw in lines:
        fields = raw.split(",")
        if len(fields) < 6:
            continue
        try:
            day = fields[0][:10]
            opened, close, high, low = map(float, fields[1:5])
            volume = float(fields[5])
        except (TypeError, ValueError):
            continue
        if day:
            out.append((day, opened, high, low, close, close, volume))
    if not out:
        raise RuntimeError("Eastmoney returned no daily bars for %s" % symbol)
    return out


def tencent_daily(symbol, expected_date, limit=10):
    """Fetch recent US daily bars from Tencent; its US endpoint is current-day capable."""
    target = datetime.date.fromisoformat(expected_date)
    start = target - datetime.timedelta(days=14)
    end = target + datetime.timedelta(days=1)
    params = urllib.parse.urlencode({
        "param": ",".join(("us" + symbol.upper(), "day", start.isoformat(),
                            end.isoformat(), str(max(1, limit)), "qfq")),
    })
    url = "https://web.ifzq.gtimg.cn/appstock/app/fqkline/get?" + params
    req = urllib.request.Request(url, headers={
        "User-Agent": UA["User-Agent"],
        "Referer": "https://gu.qq.com/",
    })
    with urllib.request.urlopen(req, timeout=15) as response:
        payload = json.loads(response.read().decode("utf-8"))
    if payload.get("code") != 0:
        raise RuntimeError("Tencent K-line code=%s for %s" % (payload.get("code"), symbol))
    stock = ((payload.get("data") or {}).get("us" + symbol.upper()) or {})
    rows = stock.get("qfqday") or stock.get("day") or []
    out = []
    for row in rows:
        if len(row) < 6:
            continue
        try:
            day = str(row[0])[:10]
            opened, close, high, low, volume = map(float, row[1:6])
        except (TypeError, ValueError):
            continue
        if day:
            out.append((day, opened, high, low, close, close, volume))
    if not out:
        raise RuntimeError("Tencent returned no daily bars for %s" % symbol)
    return sorted(out, key=lambda bar: bar[0])[-max(1, limit):]


def fetch_daily(symbol, range_="5d", expected_date=None):
    """Use Tencent for the target session, with Eastmoney/Yahoo fallbacks."""
    errors = []
    limit = 250 if range_ == "1y" else 5
    providers = []
    if expected_date:
        providers.append(("tencent", lambda: tencent_daily(symbol, expected_date)))
    providers.extend((
        ("eastmoney", lambda: eastmoney_daily(symbol, limit)),
        ("yahoo", lambda: yahoo_daily(symbol, range_, force_refresh=True)),
    ))
    for provider, fetch in providers:
        try:
            bars = fetch()
        except Exception as exc:
            errors.append("%s: %s" % (provider, str(exc)[:120]))
            continue
        if not bars:
            errors.append("%s: no daily bars" % provider)
            continue
        if expected_date and bars[-1][0] != expected_date:
            errors.append("%s: latest=%s expected=%s" % (provider, bars[-1][0], expected_date))
            continue
        return bars, provider
    raise RuntimeError("US daily providers failed: " + "; ".join(errors))


def _upsert(code, bars, source):
    c = db()
    n = 0
    for day, o, h, l, cl, a, v in bars:
        amount = (cl or 0) * (v or 0)  # 美元成交额
        c.execute("""INSERT OR REPLACE INTO daily_bars
            (code,date,open,high,low,close,prev_close,volume,amount,source,market)
            VALUES(?,?,?,?,?,?,?,?,?,?,?)""",
            (code, day, o, h, l, cl, None, v, amount, source, "US"))
        n += 1
    # prev_close 回填:按日期顺序用前一日收盘
    rows = c.execute(
        "SELECT date,close FROM daily_bars WHERE code=? AND market='US' ORDER BY date",
        (code,)).fetchall()
    for i in range(1, len(rows)):
        c.execute("UPDATE daily_bars SET prev_close=? WHERE code=? AND date=? AND market='US'",
                  (rows[i-1][1], code, rows[i][0]))
    c.commit()
    c.close()
    return n


def _seeded(code):
    c = db()
    r = c.execute("SELECT 1 FROM meta WHERE key=?", ("useeded_" + code,)).fetchone()
    c.close()
    return r is not None


def _mark_seeded(code):
    c = db()
    c.execute("INSERT OR REPLACE INTO meta(key,value) VALUES(?,?)",
              ("useeded_" + code, datetime.date.today().isoformat()))
    c.commit()
    c.close()


def backfill_one(symbol):
    """单个票打底(热度发现的新标的用):拉 1 年日K入库并标记 seeded。"""
    s = symbol.upper()
    if _seeded(s) and _read_bars_local(s):
        return 0
    bars, provider = fetch_daily(s, "1y")
    n = _upsert(s, bars, provider + "_backfill")
    if n > 0:
        _mark_seeded(s)
    time.sleep(2)  # 不密集
    return n


def backfill():
    """一次打底:宇宙内未 seeded 的票拉 1 年日K。"""
    uni = get_universe()
    todo = [s for s in uni if not _seeded(s) or not _read_bars_local(s)]
    print("us universe=%d todo=%d" % (len(uni), len(todo)))
    for s in todo:
        try:
            bars, provider = fetch_daily(s, "1y")
            n = _upsert(s, bars, provider + "_backfill")
            if n > 0:
                _mark_seeded(s)
            print("%s backfill bars=%d %s~%s" % (
                s, n, bars[0][0] if bars else "-", bars[-1][0] if bars else "-"), flush=True)
        except Exception as e:
            print("%s FAIL %s" % (s, str(e)[:120]), flush=True)
        time.sleep(2)  # 不密集


def append_today():
    """After the US close, upsert recent bars and require the current session date."""
    sys.path.insert(0, SVC)
    from trading_calendar import guard_trading_day, us_market_closed, today_str
    guard_trading_day("bars_us_append", "US")
    if not us_market_closed():
        print("US market not closed yet (16:00 ET), skip")
        return
    us_today = today_str("US")
    uni = get_universe()
    new = [s for s in uni if not _seeded(s) or not _read_bars_local(s)]
    if new:
        print("new us codes, backfill first:", new)
        backfill()
    failures = 0
    for s in uni:
        try:
            bars, provider = fetch_daily(s, "5d", expected_date=us_today)
            n = _upsert(s, bars, provider + "_push")
            last = bars[-1][0] if bars else "-"
            print("%s append bars=%d last=%s source=%s (us_today=%s)" % (
                s, n, last, provider, us_today), flush=True)
        except Exception as e:
            failures += 1
            print("%s FAIL %s" % (s, str(e)[:120]), flush=True)
        time.sleep(2)
    if failures:
        print("US append incomplete: %d/%d symbols failed" % (failures, len(uni)), flush=True)
    return failures


def _read_bars_local(code, start=None, end=None):
    c = db()
    q = ("SELECT date,open,high,low,close,prev_close,volume,amount FROM daily_bars "
         "WHERE code=? AND market='US'")
    args = [code.upper()]
    if start:
        q += " AND date>=?"
        args.append(start)
    if end:
        q += " AND date<=?"
        args.append(end)
    q += " ORDER BY date"
    try:
        rows = c.execute(q, args).fetchall()
    finally:
        c.close()
    keys = ("date", "open", "high", "low", "close", "prev_close", "volume", "amount")
    return [dict(zip(keys, r)) for r in rows]


def get_bars(code, start=None, end=None):
    """读本地美股日K;首次缺少该股票时回填,之后重复读取本地。"""
    rows = _read_bars_local(code, start, end)
    if not rows:
        try:
            backfill_one(code)
        except Exception as exc:
            logging.warning("US bars cache miss and upstream fetch failed: %s", exc)
        rows = _read_bars_local(code, start, end)
    return rows


def yahoo_daily2(symbol, timeout=15):
    """Yahoo 日K(轻量,情绪/指数用):返回最近N根 {date,close}。不入库。"""
    url = ("https://query1.finance.yahoo.com/v8/finance/chart/%s"
           "?interval=1d&range=10d" % symbol.upper())
    req = urllib.request.Request(url, headers={"User-Agent": "Mozilla/5.0"})
    d = json.load(market_cache.urlopen(req, timeout=timeout))["chart"]["result"][0]
    from zoneinfo import ZoneInfo
    tz = d["meta"].get("exchangeTimezoneName") or "America/New_York"
    zi = ZoneInfo(tz)
    ts = d["timestamp"] or []
    q = d["indicators"]["quote"][0]
    out = []
    for i, t in enumerate(ts):
        cl = q["close"][i]
        if cl is None:
            continue
        out.append({"date": datetime.datetime.fromtimestamp(t, zi).strftime("%Y-%m-%d"),
                    "close": cl})
    return out


def yahoo_intraday(symbol, interval="5m", range_="5d", timeout=15):
    """Yahoo 分钟K(美股盘中结构/早盘识别用,直连)。
    返回 [{time,open,high,low,close,volume,meta}], time 为美东 ISO,
    volume 单位=股(meta.source=yahoo),无 amount(VWAP 用典型价估算)。"""
    from zoneinfo import ZoneInfo
    url = ("https://query1.finance.yahoo.com/v8/finance/chart/%s"
           "?interval=%s&range=%s" % (symbol.upper(), interval, range_))
    req = urllib.request.Request(url, headers={"User-Agent": "Mozilla/5.0"})
    d = json.load(market_cache.urlopen(req, timeout=timeout))["chart"]["result"][0]
    tz = d["meta"].get("exchangeTimezoneName") or "America/New_York"
    zi = ZoneInfo(tz)
    ts = d["timestamp"] or []
    q = d["indicators"]["quote"][0]
    out = []
    for i, t in enumerate(ts):
        cl = q["close"][i]
        if cl is None:
            continue
        dt = datetime.datetime.fromtimestamp(t, zi).strftime("%Y-%m-%dT%H:%M:%S")
        out.append({"time": dt,
                    "open": q["open"][i], "high": q["high"][i],
                    "low": q["low"][i], "close": cl,
                    "volume": q["volume"][i] or 0,
                    "meta": {"source": "yahoo"}})
    return out


if __name__ == "__main__":
    if "--backfill" in sys.argv:
        backfill()
    elif "--append" in sys.argv:
        if append_today():
            sys.exit(1)
    elif "--universe" in sys.argv:
        print(get_universe())
    elif "--get" in sys.argv:
        i = sys.argv.index("--get")
        code = sys.argv[i + 1]
        bars = get_bars(code)
        print("%s bars=%d %s~%s" % (code, len(bars),
                                    bars[0]["date"] if bars else "-",
                                    bars[-1]["date"] if bars else "-"))
        for b in bars[-3:]:
            print(b)
    else:
        print("usage: bars_us.py --backfill | --append | --universe | --get SYM")
