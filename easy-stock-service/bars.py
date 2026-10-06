#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
日线底座:一次打底 + 每日新浪全推快照迭代 (用户架构,2026-09-29)

  1. backfill: 对宇宙内每只票拉一次全量日K写入本地 bars.db,之后永不再拉历史
  2. append:   每日收盘后,一次新浪全推快照(所有code拼一个list=请求)拼出当日K线并追加

原则:一次多个code,不做密集连发。sina volume单位=股,入库统一转成手(=东财口径)。
"""
import market_cache
import data_store
import json, os, re, sqlite3, sys, time, urllib.request, datetime

SVC = os.path.dirname(os.path.abspath(__file__))
DB = data_store.DB_PATH
UA = {"User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36",
      "Referer": "https://finance.sina.com.cn/"}
# 上游批量节流:同花顺/东财批量历史拉取后,至少间隔30分钟(用户铁律,2026-09-29)
BULK_COOLDOWN = 1800
THROTTLE_FILE = os.path.join(SVC, "logs", "upstream_throttle.json")

def bulk_allowed(source):
    """批量拉取前检查冷却,True=可跑并打标,False=未冷却"""
    try:
        d = json.load(open(THROTTLE_FILE))
    except Exception:
        d = {}
    last = (d.get(source) or {}).get("last", 0)
    if time.time() - last < BULK_COOLDOWN:
        wait = int(BULK_COOLDOWN - (time.time() - last))
        print("bulk %s cooling down, wait %ds" % (source, wait))
        return False
    d[source] = {"last": time.time(), "note": "bars backfill"}
    json.dump(d, open(THROTTLE_FILE, "w"), ensure_ascii=False, indent=1)
    return True

def db():
    c = data_store.connect()
    c.execute("""CREATE TABLE IF NOT EXISTS daily_bars(
        code TEXT, date TEXT, open REAL, high REAL, low REAL, close REAL,
        prev_close REAL, volume REAL, amount REAL, source TEXT, market TEXT DEFAULT 'CN',
        PRIMARY KEY(code, date))""")
    columns = {row[1] for row in c.execute("PRAGMA table_info(daily_bars)").fetchall()}
    if "market" not in columns:
        c.execute("ALTER TABLE daily_bars ADD COLUMN market TEXT DEFAULT 'CN'")
    c.execute("CREATE INDEX IF NOT EXISTS idx_bars_code ON daily_bars(code)")
    c.execute("""CREATE TABLE IF NOT EXISTS stock_tracking(
        market TEXT NOT NULL, code TEXT NOT NULL, name TEXT NOT NULL DEFAULT '',
        enabled INTEGER NOT NULL DEFAULT 1, source TEXT NOT NULL DEFAULT 'manual',
        updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, PRIMARY KEY(market,code))""")
    c.execute("CREATE INDEX IF NOT EXISTS idx_stock_tracking_enabled ON stock_tracking(market,enabled,code)")
    c.execute("""CREATE TABLE IF NOT EXISTS meta(
        key TEXT PRIMARY KEY, value TEXT)""")
    if not c.execute("SELECT 1 FROM meta WHERE key='daily_bars_market_default_v1'").fetchone():
        c.execute("UPDATE daily_bars SET market='CN' WHERE market IS NULL")
        c.execute("INSERT OR IGNORE INTO meta(key,value) VALUES('daily_bars_market_default_v1','done')")
        c.commit()
    return c

def seeded(c, code):
    return c.execute("SELECT 1 FROM meta WHERE key=?",
                     ("seeded_" + code,)).fetchone() is not None

def mark_seeded(c, code):
    c.execute("INSERT OR REPLACE INTO meta(key,value) VALUES(?,?)",
              ("seeded_" + code, datetime.date.today().isoformat()))
    c.commit()

def to_sina(code):
    c = code.strip()
    if c[:2] in ("60", "68", "90") or c.startswith("51") or c.startswith("58"):
        return "sh" + c
    if c[0] in ("0", "3") or c[:2] == "20":
        return "sz" + c
    return "bj" + c  # 北交 43/83/87/92

def get_universe():
    u = set()
    # 模拟仓持仓:模拟盘的数据自动进底座(用户定,2026-09-29)
    try:
        pos = json.load(open(os.path.join(SVC, "logs", "paper_ledger.json"))).get("positions", [])
        if isinstance(pos, dict):
            u.update(pos.keys())
        else:  # list of {code,...}
            for p in pos:
                if isinstance(p, dict) and p.get("code"):
                    u.add(str(p["code"]))
    except Exception: pass
    try:
        u.update(json.load(open(os.path.join(SVC, "watchlist.json")))["symbols"])
    except Exception: pass
    try:
        u.update(json.load(open(os.path.join(SVC, "positions_real.json"))).keys())
    except Exception: pass
    # 最新复盘候选池
    try:
        revs = sorted(f for f in os.listdir(os.path.join(SVC, "reviews")) if f.endswith(".json"))
        if revs:
            r = json.load(open(os.path.join(SVC, "reviews", revs[-1])))
            for c in r.get("candidates", []) or []:
                code = (c.get("code") or c.get("symbol")) if isinstance(c, dict) else c
                if code: u.add(str(code))
    except Exception: pass
    legacy = sorted(c for c in u if re.fullmatch(r"\d{6}", str(c)))
    c = db()
    try:
        for code in legacy:
            c.execute("""INSERT OR IGNORE INTO stock_tracking
                (market,code,name,enabled,source) VALUES('CN',?,'',1,'auto-discovered')""", (code,))
        c.commit()
        rows = c.execute("SELECT code FROM stock_tracking WHERE market='CN' AND enabled=1 ORDER BY code").fetchall()
        return [row[0] for row in rows]
    finally:
        c.close()

def token():
    m = re.search(r"A_STOCK_TOKEN=(\S+)", open(os.path.join(SVC, ".env")).read())
    return m.group(1) if m else ""

def api(path, force_refresh=False):
    req = urllib.request.Request("http://127.0.0.1:20081" + path, headers=UA)
    with market_cache.urlopen(req, timeout=30, force_refresh=force_refresh) as r:
        return json.loads(r.read().decode("utf-8", "ignore"))

def _fetch_history(codes):
    """东财批量拉历史日K写入库(30只/批,批间隔3秒)。返回写入根数。"""
    c = db()
    tk = token()
    total = 0
    for i in range(0, len(codes), 30):
        batch = codes[i:i+30]
        syms = ",".join(to_sina(x) for x in batch)
        try:
            d = api("/api/v1/quotes/kline/batch?symbols=%s&period=day&limit=240&token=%s" % (syms, tk),
                    force_refresh=True)
            n = 0
            for code6, bars in d.get("data", {}).items():
                code = re.sub(r"\D", "", code6)[:6]
                for b in bars or []:
                    day = (b.get("time") or "")[:10]
                    if not day: continue
                    c.execute("""INSERT OR IGNORE INTO daily_bars
                        (code,date,open,high,low,close,prev_close,volume,amount,source)
                        VALUES(?,?,?,?,?,?,?,?,?,?)""",
                        (code, day, b.get("open"), b.get("high"), b.get("low"),
                         b.get("close"), b.get("previous_close"), b.get("volume"),
                         b.get("amount"), "backfill"))
                    n += 1
            c.commit()
            print("batch %d-%d ok bars=%d" % (i, i+len(batch), n), flush=True)
            total += n
            for x in batch:
                mark_seeded(c, x)
        except Exception as e:
            print("batch %d FAIL %s" % (i, str(e)[:100]), flush=True)
        time.sleep(3)  # 降频,不密集
    c.close()
    return total

def backfill():
    """一次打底:宇宙内无数据的新票拉全量日K"""
    uni = get_universe()
    if not uni:
        print("universe empty"); return
    c = db()
    todo = [x for x in uni if not seeded(c, x)]
    c.close()
    print("universe=%d todo=%d" % (len(uni), len(todo)))
    if not todo:
        return
    if not bulk_allowed("eastmoney_bulk"):
        return
    _fetch_history(todo)

# ---------------- 读接口:回测/策略统一从这里取数,不直连上游 ----------------

def get_bars(code, start=None, end=None):
    """读本地日K,[{date,open,high,low,close,prev_close,volume,amount}] 按日期升序。
    首次缺数据时补齐并落库;已有数据只读本地,不按读取次数请求上游。"""
    ensure_bars(code, start=start, end=end)
    c = db()
    q = "SELECT date,open,high,low,close,prev_close,volume,amount FROM daily_bars WHERE code=?"
    args = [code]
    if start:
        q += " AND date>=?"; args.append(start)
    if end:
        q += " AND date<=?"; args.append(end)
    q += " ORDER BY date"
    rows = c.execute(q, args).fetchall()
    c.close()
    keys = ("date","open","high","low","close","prev_close","volume","amount")
    return [dict(zip(keys, r)) for r in rows]

def coverage(code):
    """返回 (最早日,最晚日,根数),无数据返回 (None,None,0)"""
    c = db()
    r = c.execute("SELECT MIN(date),MAX(date),COUNT(*) FROM daily_bars WHERE code=?",
                  (code,)).fetchone()
    c.close()
    return r if r[2] else (None, None, 0)

def ensure_bars(codes, start=None, end=None):
    """读一次就对齐一次(用户定,2026-09-29):
    检查每只票在[start,end]的覆盖,缺的只拉一次写入库,以后永不重复拉。
    返回 {code: (min,max,count)}。冷却中拉不了则如实返回缺口,不硬拉。"""
    if isinstance(codes, str):
        codes = [codes]
    c = db()
    missing = []
    for code in codes:
        mn, mx, n = coverage(code)
        need = False
        if n == 0:
            need = True
        else:
            if start and (mn is None or mn > start):
                need = True
            if end and (mx is None or mx < end):
                need = True
        if need:
            missing.append(code)
    c.close()
    if missing:
        if not bulk_allowed("eastmoney_bulk"):
            print("cooling, still missing:", missing)
        else:
            _fetch_history(missing)
    return {code: coverage(code) for code in codes}

def sina_snapshot(codes, force_refresh=False):
    """一次全推:所有code拼一个list=请求,返回 {code: bar}

    兼容两种返回格式:
      var hq_str_sh600733="北汽蓝谷,4.87,..."   (默认)
      sh600733=北汽蓝谷,4.87,...                (?format=text)
    字段序号两种格式一致:f[30]=日期 f[31]=ticktime。
    """
    qs = ",".join(to_sina(x) for x in codes)
    req = urllib.request.Request(
        "http://hq.sinajs.cn/?format=text&list=" + qs, headers=UA)
    with market_cache.urlopen(req, timeout=30, force_refresh=force_refresh) as r:
        text = r.read().decode("gbk", "ignore")
    out = {}
    for line in text.replace(";", "\n").splitlines():
        line = line.strip()
        m = re.match(r'(?:var\s+hq_str_)?([a-z]{2}\d{6})\s*=\s*"?([^"]*)"?$', line, re.I)
        if not m:
            continue
        scode, body = m.group(1), m.group(2).rstrip('";')
        f = body.split(",")
        if len(f) < 32:
            continue
        code = re.sub(r"\D", "", scode)[-6:]
        try:
            out[code] = {
                "date": f[30], "ticktime": f[30] + " " + f[31],
                "open": float(f[1]), "prev_close": float(f[2]),
                "close": float(f[3]), "high": float(f[4]), "low": float(f[5]),
                "volume": float(f[8]) / 100,  # 股->手,与东财口径统一
                "amount": float(f[9]),
            }
        except ValueError:
            continue
    return out

def append_today():
    """每日迭代:收盘后用全推快照补当日K线(幂等,已存在跳过)

    数据终值口径(用户定,2026-09-29):A股盘后固定价格交易到15:30,15:00后还有小单,
    必须15:30后取快照才准。以sina ticktime验证。
    """
    from trading_calendar import guard_trading_day
    guard_trading_day("bars_append")
    # 终值门控按北京时间(Asia/Shanghai)判,本机VM时钟是UTC,naive now()会把15:35判成07:35
    from zoneinfo import ZoneInfo
    _now_sh = datetime.datetime.now(ZoneInfo("Asia/Shanghai"))
    now = _now_sh.strftime("%H:%M")
    if now < "15:30":
        print("too early (%s), wait until after 15:30 for final bars" % now)
        return
    today = _now_sh.date().isoformat()
    c = db()
    uni = get_universe()
    todo = [x for x in uni if not c.execute(
        "SELECT 1 FROM daily_bars WHERE code=? AND date=?", (x, today)).fetchone()]
    if not todo:
        print(today, "all done"); c.close(); return
    # 新票首次出现:先给它打底一次历史(幂等:seeded标记防重复)
    new_codes = [x for x in todo if not seeded(c, x)]
    if new_codes:
        print("new codes, backfill first:", new_codes)
        c.close()
        backfill()
        c = db()
        todo = [x for x in uni if not c.execute(
            "SELECT 1 FROM daily_bars WHERE code=? AND date=?", (x, today)).fetchone()]
    snap = sina_snapshot(todo, force_refresh=True)
    n = 0
    ticks = []
    for code in todo:
        b = snap.get(code)
        if not b or b["date"] != today or b["close"] <= 0:
            continue  # 快照日期不对/停牌就不写,明天再说
        ticks.append(b["ticktime"])
        c.execute("""INSERT OR IGNORE INTO daily_bars
            (code,date,open,high,low,close,prev_close,volume,amount,source)
            VALUES(?,?,?,?,?,?,?,?,?,?)""",
            (code, today, b["open"], b["high"], b["low"], b["close"],
             b["prev_close"], b["volume"], b["amount"], "sina_push"))
        n += 1
    c.commit(); c.close()
    tick_range = ("tick %s~%s" % (min(ticks), max(ticks))) if ticks else "no ticks"
    print(today, "appended bars=%d/%d %s" % (n, len(todo), tick_range))

if __name__ == "__main__":
    if "--backfill" in sys.argv: backfill()
    elif "--append" in sys.argv: append_today()
    elif "--universe" in sys.argv: print(get_universe())
    elif "--get" in sys.argv:
        i = sys.argv.index("--get")
        code = sys.argv[i+1]
        start = sys.argv[i+2] if len(sys.argv) > i+2 else None
        end = sys.argv[i+3] if len(sys.argv) > i+3 else None
        bars = get_bars(code, start, end)
        print("%s bars=%d %s~%s" % (code, len(bars),
              bars[0]["date"] if bars else "-", bars[-1]["date"] if bars else "-"))
        for b in bars[-5:]:
            print(b)
    elif "--ensure" in sys.argv:
        i = sys.argv.index("--ensure")
        codes = sys.argv[i+1].split(",")
        start = sys.argv[i+2] if len(sys.argv) > i+2 else None
        end = sys.argv[i+3] if len(sys.argv) > i+3 else None
        for code, (mn, mx, n) in ensure_bars(codes, start, end).items():
            print(code, n, mn, mx)
    else: print("usage: bars.py --backfill | --append | --universe | --get CODE [start] [end] | --ensure CODES [start] [end]")
