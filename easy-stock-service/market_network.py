"""Shared outbound market proxy; local API calls always bypass it."""
import ipaddress
import json
import os
import urllib.parse
import urllib.request
import data_store


def urlopen(request, timeout=30):
    target = request.full_url if isinstance(request, urllib.request.Request) else request
    host = urllib.parse.urlsplit(target).hostname or ""
    local = host == "localhost"
    try:
        local = local or ipaddress.ip_address(host).is_loopback
    except ValueError:
        pass
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
    return opener.open(request, timeout=timeout)
