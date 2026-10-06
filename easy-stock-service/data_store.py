"""One SQLite path and connection policy for strategy market data."""

import os
import sqlite3
import threading


SERVICE_DIR = os.path.dirname(os.path.abspath(__file__))
DB_PATH = os.environ.get("EASY_STOCK_DATA_DB") or os.path.join(SERVICE_DIR, "data", "bars.db")
_WAL_LOCK = threading.Lock()
_WAL_READY = False


def connect(timeout=10):
    global _WAL_READY
    parent = os.path.dirname(DB_PATH)
    if parent:
        os.makedirs(parent, exist_ok=True)
    connection = sqlite3.connect(DB_PATH, timeout=timeout)
    connection.execute("PRAGMA busy_timeout=10000")
    connection.execute("PRAGMA synchronous=NORMAL")
    if not _WAL_READY:
        with _WAL_LOCK:
            if not _WAL_READY:
                connection.execute("PRAGMA journal_mode=WAL")
                _WAL_READY = True
    return connection


def ensure_daily_bars_schema(connection):
    """Keep one market-aware, idempotent key for all daily bars."""
    connection.execute("""CREATE TABLE IF NOT EXISTS daily_bars(
        code TEXT NOT NULL, date TEXT NOT NULL, open REAL, high REAL, low REAL,
        close REAL, prev_close REAL, volume REAL, amount REAL, source TEXT,
        market TEXT NOT NULL DEFAULT 'CN', PRIMARY KEY(market,code,date))""")
    columns = {row[1] for row in connection.execute("PRAGMA table_info(daily_bars)")}
    if "market" not in columns:
        connection.execute("ALTER TABLE daily_bars ADD COLUMN market TEXT DEFAULT 'CN'")

    primary_key = [row[1] for row in sorted(
        (row for row in connection.execute("PRAGMA table_info(daily_bars)") if row[5]),
        key=lambda row: row[5],
    )]
    if set(primary_key) != {"market", "code", "date"}:
        if connection.in_transaction:
            connection.commit()
        connection.execute("BEGIN IMMEDIATE")
        try:
            primary_key = [row[1] for row in sorted(
                (row for row in connection.execute("PRAGMA table_info(daily_bars)") if row[5]),
                key=lambda row: row[5],
            )]
            if set(primary_key) != {"market", "code", "date"}:
                connection.execute("""CREATE TABLE daily_bars_market_key_v2(
                    code TEXT NOT NULL, date TEXT NOT NULL, open REAL, high REAL, low REAL,
                    close REAL, prev_close REAL, volume REAL, amount REAL, source TEXT,
                    market TEXT NOT NULL DEFAULT 'CN', PRIMARY KEY(market,code,date))""")
                connection.execute("""INSERT OR REPLACE INTO daily_bars_market_key_v2
                    (code,date,open,high,low,close,prev_close,volume,amount,source,market)
                    SELECT UPPER(TRIM(COALESCE(code,''))),COALESCE(date,''),open,high,low,close,
                        prev_close,volume,amount,source,
                        UPPER(COALESCE(NULLIF(TRIM(market),''),'CN'))
                    FROM daily_bars ORDER BY rowid""")
                connection.execute("DROP TABLE daily_bars")
                connection.execute("ALTER TABLE daily_bars_market_key_v2 RENAME TO daily_bars")
            connection.commit()
        except Exception:
            connection.rollback()
            raise

    connection.execute("CREATE INDEX IF NOT EXISTS idx_bars_code ON daily_bars(code)")
    connection.execute("""CREATE INDEX IF NOT EXISTS idx_bars_market_code_date
        ON daily_bars(UPPER(code),UPPER(COALESCE(NULLIF(market,''),'CN')),date DESC)""")
