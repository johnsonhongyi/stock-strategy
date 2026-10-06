"""Persistent local-first cache for read-only market-data HTTP requests.

Cache files live under ``data/`` (which the deployment maps to the 4 TB disk).
Fresh cache entries are always preferred; expired entries are refreshed on
demand, with a bounded stale fallback when the upstream is unavailable.
"""

import hashlib
import io
import json
import logging
import os
import sqlite3
import threading
import time
import urllib.parse
import urllib.request
import data_store


SVC = os.path.dirname(os.path.abspath(__file__))
DB_PATH = data_store.DB_PATH
STALE_MAX_AGE = 7 * 24 * 60 * 60
PRUNE_AFTER = 30 * 24 * 60 * 60
_INIT_LOCK = threading.Lock()
_INITIALIZED = False
_LAST_PRUNE = 0.0

_API_PREFIXES = (
    "/api/v1/quotes/",
    "/api/v1/market/",
    "/api/v1/stocks/",
    "/api/v1/themes/overview",
    "/api/v1/short-term/limit-up-ladder",
)
_MARKET_HOSTS = {
    "api.kraken.com",
    "data.10jqka.com.cn",
    "eq.10jqka.com.cn",
    "flash-api.xuangubao.cn",
    "hq.sinajs.cn",
    "push2.eastmoney.com",
    "qt.gtimg.cn",
    "query1.finance.yahoo.com",
    "www.55188.com",
}
_SECRET_QUERY_KEYS = {
    "access_token", "api_key", "token", "x-a-stock-token", "authorization"
}
_VOLATILE_QUERY_KEYS = {"_", "nonce", "requestid", "rn", "timestamp"}


class CachedResponse(io.BytesIO):
    """Small urllib-compatible response for cached bodies."""

    def __init__(self, body, url, status=200, headers=None, cache_state="HIT"):
        super().__init__(body)
        self.url = url
        self.status = status
        self.code = status
        self.headers = {}
        for key, value in (headers or {}).items():
            self.headers[key] = ", ".join(value) if isinstance(value, list) else str(value)
        self.headers.setdefault("Content-Type", "application/octet-stream")
        self.headers["X-Stock-Cache"] = cache_state
        self.headers["X-Local-Market-Cache"] = cache_state

    def getcode(self):
        return self.code

    def geturl(self):
        return self.url

    def info(self):
        return self.headers

    def __enter__(self):
        return self

    def __exit__(self, *_exc):
        self.close()
        return False


def _is_cacheable(url, method):
    if method.upper() != "GET":
        return False
    parsed = urllib.parse.urlsplit(url)
    if parsed.username or parsed.password:
        return False
    if any(key.lower() in _SECRET_QUERY_KEYS for key, _value in urllib.parse.parse_qsl(parsed.query, keep_blank_values=True)):
        return False
    if any(parsed.path.startswith(prefix) for prefix in _API_PREFIXES):
        return True
    return (parsed.hostname or "").lower() in _MARKET_HOSTS


def _canonical_url(url):
    parsed = urllib.parse.urlsplit(url)
    query = []
    for key, value in urllib.parse.parse_qsl(parsed.query, keep_blank_values=True):
        lower_key = key.lower()
        if lower_key in _SECRET_QUERY_KEYS:
            return ""
        if lower_key in _VOLATILE_QUERY_KEYS or lower_key == "_t" or lower_key == "_ts":
            continue
        query.append((key, value))
    query.sort(key=lambda item: item[0])
    return urllib.parse.urlunsplit((
        parsed.scheme.lower(),
        (parsed.netloc or "").lower(),
        parsed.path,
        urllib.parse.urlencode(query, doseq=True),
        "",
    ))


def _cache_key(url, method="GET"):
    canonical = _canonical_url(url)
    if not canonical:
        return ""
    value = "\n".join((method.upper(), canonical, ""))
    return hashlib.sha256(value.encode("utf-8")).hexdigest()


def _ttl_for(url):
    parsed = urllib.parse.urlsplit(url)
    path = parsed.path.lower()
    host = (parsed.hostname or "").lower()
    query = {k.lower(): v for k, v in urllib.parse.parse_qsl(parsed.query, keep_blank_values=True)}

    if path.endswith("/quotes/realtime") or path.endswith("/ticker") or host in {"hq.sinajs.cn", "qt.gtimg.cn"}:
        return 15
    if "kline" in path or query.get("klt") or query.get("scale") or path.endswith("/ohlc") or path.endswith("/chart"):
        period = (query.get("period") or query.get("interval") or query.get("klt") or query.get("scale") or "").lower()
        return 45 if period in {"1", "5", "15", "30", "60", "1m", "2m", "5m", "15m", "30m", "60m", "1h"} else 5 * 60
    if path.endswith("/stocks/directory") or path.endswith("/assets"):
        return 24 * 60 * 60
    if "limit_up_pool" in path and query.get("date"):
        return 30 * 24 * 60 * 60
    if "10jqka.com.cn" in host or "stockrank" in path:
        return 30
    if "news" in path or "telegraph" in path:
        return 2 * 60
    return 5 * 60


def _stale_limit(url):
    parsed = urllib.parse.urlsplit(url)
    path = parsed.path.lower()
    host = (parsed.hostname or "").lower()
    query = {k.lower(): v for k, v in urllib.parse.parse_qsl(parsed.query, keep_blank_values=True)}
    if path.endswith("/quotes/realtime") or path.endswith("/ticker") or host in {"hq.sinajs.cn", "qt.gtimg.cn"}:
        return 2 * 60 * 60
    if "10jqka.com.cn" in host or "stockrank" in path:
        return 12 * 60 * 60
    if "kline" in path or path.endswith("/ohlc") or path.endswith("/chart") or query.get("klt") or query.get("scale"):
        return 30 * 24 * 60 * 60
    if "news" in path or "telegraph" in path:
        return 24 * 60 * 60
    return STALE_MAX_AGE


def _connection():
    global _INITIALIZED
    conn = data_store.connect()
    if not _INITIALIZED:
        with _INIT_LOCK:
            if not _INITIALIZED:
                conn.execute("""CREATE TABLE IF NOT EXISTS market_provider_http_cache (
                    cache_key TEXT PRIMARY KEY,
                    status_code INTEGER NOT NULL,
                    headers_json TEXT NOT NULL,
                    body BLOB NOT NULL,
                    fetched_at INTEGER NOT NULL,
                    expires_at INTEGER NOT NULL
                )""")
                conn.execute("CREATE INDEX IF NOT EXISTS market_provider_http_cache_fetched_at ON market_provider_http_cache(fetched_at)")
                _migrate_legacy_http_cache(conn)
                conn.commit()
                _INITIALIZED = True
    return conn


def _migrate_legacy_http_cache(conn):
    exists = conn.execute("SELECT 1 FROM sqlite_master WHERE type='table' AND name='http_cache'").fetchone()
    if not exists:
        return
    rows = conn.execute(
        "SELECT safe_url,status,content_type,body,fetched_at,expires_at FROM http_cache"
    ).fetchall()
    for url, status, content_type, body, fetched_at, expires_at in rows:
        key = _cache_key(url)
        if not key:
            continue
        headers = json.dumps({"Content-Type": [content_type]}, separators=(",", ":"))
        conn.execute(
            "INSERT OR IGNORE INTO market_provider_http_cache "
            "(cache_key,status_code,headers_json,body,fetched_at,expires_at) VALUES(?,?,?,?,?,?)",
            (key, status, headers, body, int(fetched_at * 1_000_000_000), int(expires_at * 1_000_000_000)),
        )
    conn.execute("DROP TABLE http_cache")


def _read_cached(url):
    conn = _connection()
    try:
        row = conn.execute(
            "SELECT status_code,headers_json,body,fetched_at,expires_at "
            "FROM market_provider_http_cache WHERE cache_key=?",
            (_cache_key(url),),
        ).fetchone()
        if not row:
            return None
        status, headers_json, body, fetched_at, expires_at = row
        return status, json.loads(headers_json), body, fetched_at / 1_000_000_000, expires_at / 1_000_000_000
    finally:
        conn.close()


def _write_cached(url, status, headers, body, ttl, now):
    global _LAST_PRUNE
    key = _cache_key(url)
    if not key:
        return
    headers_json = json.dumps(headers, separators=(",", ":"))
    conn = _connection()
    try:
        conn.execute(
            "INSERT INTO market_provider_http_cache "
            "(cache_key,status_code,headers_json,body,fetched_at,expires_at) VALUES(?,?,?,?,?,?) "
            "ON CONFLICT(cache_key) DO UPDATE SET status_code=excluded.status_code,"
            "headers_json=excluded.headers_json,body=excluded.body,"
            "fetched_at=excluded.fetched_at,expires_at=excluded.expires_at",
            (key, status, headers_json, sqlite3.Binary(body), int(now * 1_000_000_000), int((now + ttl) * 1_000_000_000)),
        )
        if now - _LAST_PRUNE >= 3600:
            conn.execute(
                "DELETE FROM market_provider_http_cache WHERE fetched_at < ?",
                (int((now - PRUNE_AFTER) * 1_000_000_000),),
            )
            _LAST_PRUNE = now
        conn.commit()
    finally:
        conn.close()


def urlopen(url, timeout=30, *, force_refresh=False):
    """Read a market GET from SQLite first; fetch and persist on miss/expiry.

    ``force_refresh=True`` bypasses the local entry. Non-GET requests and
    non-market hosts retain normal urllib behavior.
    """
    request = url if isinstance(url, urllib.request.Request) else urllib.request.Request(url)
    target_url = request.full_url
    method = request.get_method()
    if (
        not _is_cacheable(target_url, method)
        or request.data is not None
        or request.headers.get("Authorization")
        or request.headers.get("Cookie")
        or request.headers.get("Range")
        or request.headers.get("If-None-Match")
        or request.headers.get("If-Modified-Since")
        or "no-cache" in request.headers.get("Cache-Control", "").lower()
        or "no-store" in request.headers.get("Cache-Control", "").lower()
    ):
        return urllib.request.urlopen(request, timeout=timeout)

    now = time.time()
    try:
        cached = _read_cached(target_url)
    except (sqlite3.Error, OSError) as exc:
        logging.warning("market cache read failed: %s", exc)
        cached = None
    if cached:
        status, cached_headers, body, fetched_at, expires_at = cached
        if not force_refresh and expires_at > now:
            return CachedResponse(body, target_url, status, cached_headers, "HIT")

    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            body = response.read()
            status = response.getcode() or 200
            headers = {key: [value] for key, value in response.info().items()}
    except Exception:
        if cached:
            status, cached_headers, body, fetched_at, _expires_at = cached
            age = max(0.0, now - fetched_at)
            if age <= _stale_limit(target_url):
                logging.warning("market upstream unavailable; using local cache age=%ds url=%s",
                                int(age), _canonical_url(target_url))
                return CachedResponse(body, target_url, status, cached_headers, "STALE")
        raise
    if status == 200 and body:
        try:
            response_headers = {key.lower(): value for key, value in headers.items()}
            if (
                "no-store" not in response_headers.get("cache-control", [""])[0].lower()
                and "set-cookie" not in response_headers
                and response_headers.get("vary", [""])[0] != "*"
            ):
                _write_cached(target_url, status, headers, body, _ttl_for(target_url), now)
        except (sqlite3.Error, OSError) as exc:
            logging.warning("market cache write failed: %s", exc)
    return CachedResponse(body, target_url, status, headers, "MISS")


def cache_stats():
    if not os.path.exists(DB_PATH):
        return {"path": DB_PATH, "entries": 0, "bytes": 0}
    conn = _connection()
    try:
        count, size, oldest, newest = conn.execute(
            "SELECT COUNT(*),COALESCE(SUM(length(body)),0),MIN(fetched_at),MAX(fetched_at) "
            "FROM market_provider_http_cache"
        ).fetchone()
        return {"path": DB_PATH, "entries": count, "bytes": size,
                "oldest": (oldest / 1_000_000_000) if oldest is not None else None,
                "newest": (newest / 1_000_000_000) if newest is not None else None}
    finally:
        conn.close()


if __name__ == "__main__":
    import json
    print(json.dumps(cache_stats(), ensure_ascii=False, indent=2))
