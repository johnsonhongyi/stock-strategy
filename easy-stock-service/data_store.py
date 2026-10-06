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
