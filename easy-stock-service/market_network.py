"""Shared outbound market proxy; local API calls always bypass it."""
import email.utils
import ipaddress
import json
import logging
import time
import threading
import urllib.error
import os
import urllib.parse
import urllib.request
import data_store

_PROVIDER_BLOCKS = {}
_PROVIDER_BLOCKS_LOCK = threading.Lock()


def _provider_key(host):
    host = (host or "").lower().rstrip(".")
    if host == "eastmoney.com" or host.endswith(".eastmoney.com"):
        return "eastmoney"
    if host == "10jqka.com.cn" or host.endswith(".10jqka.com.cn"):
        return "10jqka"
    if host == "duanxianxia.com" or host.endswith(".duanxianxia.com"):
        return "duanxianxia"
    if host == "xuangubao.cn" or host.endswith(".xuangubao.cn"):
        return "xuangubao"
    return ""


def _is_heat_or_sector_request(url):
    parsed = urllib.parse.urlsplit(url)
    path = parsed.path.lower()
    if any(key in path for key in ("hot", "rank", "theme", "sector", "concept", "board", "surge")):
        return True
    if path.endswith("/api/qt/clist/get"):
        fs = (urllib.parse.parse_qs(parsed.query).get("fs") or [""])[0].lower()
        return "b:" in fs or "m:90" in fs
    return False


def _provider_scope(url, provider):
    if not provider or not _is_heat_or_sector_request(url):
        return provider
    parsed = urllib.parse.urlsplit(url)
    scope = provider + ":heat:" + parsed.path.lower()
    fs = (urllib.parse.parse_qs(parsed.query).get("fs") or [""])[0].strip().lower()
    return scope + (":fs=" + fs if fs else "")


def _provider_block(provider, scope=None):
    if not provider:
        return 0, 0
    connection = data_store.connect()
    try:
        connection.execute("""CREATE TABLE IF NOT EXISTS market_provider_rate_limits (
            provider TEXT PRIMARY KEY, failure_count INTEGER NOT NULL DEFAULT 0,
            blocked_until INTEGER NOT NULL, last_status INTEGER NOT NULL, updated_at INTEGER NOT NULL)""")
        keys = (provider, scope) if scope and scope != provider else (provider,)
        placeholders = ",".join("?" for _ in keys)
        row = connection.execute(
            "SELECT COALESCE(MAX(failure_count),0),COALESCE(MAX(blocked_until),0) "
            "FROM market_provider_rate_limits WHERE provider IN (" + placeholders + ")", keys,
        ).fetchone()
        connection.commit()
    finally:
        connection.close()
    with _PROVIDER_BLOCKS_LOCK:
        memory = [_PROVIDER_BLOCKS.get(key, (0, 0)) for key in keys]
    return max(row[0], *(state[0] for state in memory)), max(row[1], *(state[1] for state in memory))

def _record_provider_block(provider, status, retry_after=""):
    if not provider:
        return
    base_seconds = (60 if status == 403 else 30) * 60
    now = int(time.time())
    wait = base_seconds
    try:
        wait = max(wait, min(int(retry_after), 24 * 60 * 60))
    except (TypeError, ValueError):
        try:
            retry_at = email.utils.parsedate_to_datetime(retry_after)
            wait = max(wait, min(int(retry_at.timestamp() - now), 24 * 60 * 60))
        except (TypeError, ValueError, OverflowError):
            pass
    wait = min(wait, 24 * 60 * 60)
    with _PROVIDER_BLOCKS_LOCK:
        previous = _PROVIDER_BLOCKS.get(provider, (0, 0))
        failures = previous[0] + 1
        adaptive_wait = min(24 * 60 * 60, base_seconds * (2 ** min(failures - 1, 5)))
        _PROVIDER_BLOCKS[provider] = (failures, max(now + wait, now + adaptive_wait))
    connection = data_store.connect()
    try:
        connection.execute("""CREATE TABLE IF NOT EXISTS market_provider_rate_limits (
            provider TEXT PRIMARY KEY, failure_count INTEGER NOT NULL DEFAULT 0,
            blocked_until INTEGER NOT NULL, last_status INTEGER NOT NULL, updated_at INTEGER NOT NULL)""")
        connection.execute("""INSERT INTO market_provider_rate_limits
            (provider,failure_count,blocked_until,last_status,updated_at) VALUES(?,1,?,?,?)
            ON CONFLICT(provider) DO UPDATE SET
            failure_count=market_provider_rate_limits.failure_count+1,
            blocked_until=MAX(excluded.blocked_until,market_provider_rate_limits.blocked_until,
                excluded.updated_at+MIN(86400,?*(1 << MIN(market_provider_rate_limits.failure_count,5)))),
            last_status=excluded.last_status,updated_at=excluded.updated_at""",
            (provider, now + wait, status, now, base_seconds),
        )
        connection.commit()
    finally:
        connection.close()

def _clear_provider_block(provider):
    if not provider:
        return
    connection = data_store.connect()
    try:
        connection.execute(
            "DELETE FROM market_provider_rate_limits WHERE provider=? AND blocked_until<=?",
            (provider, int(time.time())),
        )
        connection.commit()
    finally:
        connection.close()
    with _PROVIDER_BLOCKS_LOCK:
        state = _PROVIDER_BLOCKS.get(provider)
        if state and state[1] <= time.time():
            _PROVIDER_BLOCKS.pop(provider, None)

def _claim_provider_scope(scope):
    if not scope:
        return 0, False
    now = int(time.time())
    blocked_until = now + 30 * 60
    connection = data_store.connect()
    try:
        connection.execute("""CREATE TABLE IF NOT EXISTS market_provider_rate_limits (
            provider TEXT PRIMARY KEY, failure_count INTEGER NOT NULL DEFAULT 0,
            blocked_until INTEGER NOT NULL, last_status INTEGER NOT NULL, updated_at INTEGER NOT NULL)""")
        cursor = connection.execute("""INSERT INTO market_provider_rate_limits
            (provider,failure_count,blocked_until,last_status,updated_at) VALUES(?,0,?,102,?)
            ON CONFLICT(provider) DO UPDATE SET failure_count=0,
            blocked_until=excluded.blocked_until,last_status=excluded.last_status,updated_at=excluded.updated_at
            WHERE market_provider_rate_limits.blocked_until<=excluded.updated_at""",
            (scope, blocked_until, now))
        connection.commit()
        if cursor.rowcount:
            claimed = True
            until = blocked_until
        else:
            row = connection.execute(
                "SELECT blocked_until FROM market_provider_rate_limits WHERE provider=?", (scope,)
            ).fetchone()
            claimed = False
            until = row[0] if row else 0
    finally:
        connection.close()
    if claimed:
        with _PROVIDER_BLOCKS_LOCK:
            previous = _PROVIDER_BLOCKS.get(scope, (0, 0))
            _PROVIDER_BLOCKS[scope] = (0, max(previous[1], until))
    return until, claimed


def _record_provider_success(scope):
    if not scope:
        return
    now = int(time.time())
    blocked_until = now + 30 * 60
    with _PROVIDER_BLOCKS_LOCK:
        previous = _PROVIDER_BLOCKS.get(scope, (0, 0))
        _PROVIDER_BLOCKS[scope] = (0, max(previous[1], blocked_until))
    connection = data_store.connect()
    try:
        connection.execute("""CREATE TABLE IF NOT EXISTS market_provider_rate_limits (
            provider TEXT PRIMARY KEY, failure_count INTEGER NOT NULL DEFAULT 0,
            blocked_until INTEGER NOT NULL, last_status INTEGER NOT NULL, updated_at INTEGER NOT NULL)""")
        connection.execute("""INSERT INTO market_provider_rate_limits
            (provider,failure_count,blocked_until,last_status,updated_at) VALUES(?,0,?,200,?)
            ON CONFLICT(provider) DO UPDATE SET failure_count=0,
            blocked_until=MAX(market_provider_rate_limits.blocked_until,excluded.blocked_until),
            last_status=excluded.last_status,updated_at=excluded.updated_at""",
            (scope, blocked_until, now))
        connection.commit()
    finally:
        connection.close()


def urlopen(request, timeout=30):
    target = request.full_url if isinstance(request, urllib.request.Request) else request
    host = urllib.parse.urlsplit(target).hostname or ""
    local = host.lower() == "localhost"
    try:
        address = ipaddress.ip_address(host)
        local = local or address.is_loopback or address.is_private or address.is_link_local
    except ValueError:
        pass
    provider = _provider_key(host)
    scope = _provider_scope(target, provider)
    if provider:
        try:
            _failures, blocked_until = _provider_block(provider, scope)
            if blocked_until > time.time():
                wait = max(1, blocked_until - int(time.time()))
                raise urllib.error.HTTPError(target, 429, "provider cooling down",
                    {"Retry-After": str(wait), "X-Stock-Provider-Cooldown": str(blocked_until)}, None)
            if scope != provider:
                blocked_until, claimed = _claim_provider_scope(scope)
                if not claimed:
                    wait = max(1, blocked_until - int(time.time()))
                    raise urllib.error.HTTPError(target, 429, "provider resource cooling down",
                        {"Retry-After": str(wait), "X-Stock-Provider-Cooldown": str(blocked_until)}, None)
        except urllib.error.HTTPError:
            raise
        except Exception as exc:
            logging.warning("market provider cooldown read failed; suppressing outbound request: %s", exc)
            raise urllib.error.URLError("market provider cooldown state unavailable") from exc
    proxies = {}
    path = os.path.join(os.path.dirname(data_store.DB_PATH), "market-network.json")
    if not local:
        try:
            with open(path, encoding="utf-8") as handle:
                value = json.load(handle).get("proxy_url", "").strip()
        except FileNotFoundError:
            value = ""
        if value:
            parsed = urllib.parse.urlsplit(value)
            if parsed.scheme not in ("http", "https") or not parsed.hostname or parsed.username or parsed.password:
                raise ValueError("行情代理配置无效")
            proxies = {"http": value, "https": value}
    opener = urllib.request.build_opener(urllib.request.ProxyHandler(proxies))
    try:
        response = opener.open(request, timeout=timeout)
    except urllib.error.HTTPError as exc:
        if provider and exc.code in (403, 429):
            try:
                _record_provider_block(provider, exc.code, exc.headers.get("Retry-After", ""))
            except Exception as state_error:
                logging.warning("market provider cooldown write failed: %s", state_error)
        raise
    if provider and 200 <= (response.getcode() or 0) < 300:
        try:
            _clear_provider_block(provider)
            if scope != provider:
                _record_provider_success(scope)
        except Exception as exc:
            logging.warning("market provider cooldown reset failed: %s", exc)
    return response
