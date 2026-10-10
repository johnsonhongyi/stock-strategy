import io
import json
import os
import tempfile
import unittest
from unittest.mock import patch
import urllib.error
import urllib.request
import market_cache
import market_network

class MarketNetworkTests(unittest.TestCase):
    def test_shared_proxy_and_loopback_bypass(self):
        with tempfile.TemporaryDirectory() as folder:
            path = os.path.join(folder, "market-network.json")
            with open(path, "w", encoding="utf-8") as handle:
                json.dump({"proxy_url": "http://proxy.example:7890"}, handle)
            with patch.object(market_network.data_store, "DB_PATH", os.path.join(folder, "bars.db")), patch("urllib.request.build_opener") as builder:
                market_network.urlopen("https://api.kraken.com/0/public/OHLC")
                self.assertEqual(builder.call_args.args[0].proxies, {"http": "http://proxy.example:7890", "https": "http://proxy.example:7890"})
                market_network.urlopen("http://127.0.0.1:20081/api/health")
                self.assertEqual(builder.call_args.args[0].proxies, {})
                market_network.urlopen("http://172.18.0.9:20081/api/health")
                self.assertEqual(builder.call_args.args[0].proxies, {})
                with open(path, "w", encoding="utf-8") as handle:
                    json.dump({"proxy_url": ""}, handle)
                market_network.urlopen("https://api.kraken.com/0/public/OHLC")
                self.assertEqual(builder.call_args.args[0].proxies, {})

    def test_provider_429_cooldown_is_shared_and_adaptive(self):
        with tempfile.TemporaryDirectory() as folder, patch.object(market_network.data_store, "DB_PATH", os.path.join(folder, "bars.db")), patch.object(market_network.time, "time", return_value=1_000_000):
            market_network._record_provider_block("10jqka", 429)
            first_failures, first_until = market_network._provider_block("10jqka")
            market_network._record_provider_block("10jqka", 429)
            second_failures, second_until = market_network._provider_block("10jqka")
            self.assertEqual((first_failures, second_failures), (1, 2))
            self.assertEqual(first_until - 1_000_000, 1800)
            self.assertEqual(second_until - 1_000_000, 3600)
            market_network._clear_provider_block("10jqka")
            self.assertEqual(market_network._provider_block("10jqka"), (2, second_until))
            with patch.object(market_network.time, "time", return_value=second_until + 1):
                market_network._clear_provider_block("10jqka")
                self.assertEqual(market_network._provider_block("10jqka"), (0, 0))

    def test_successful_hot_fetch_blocks_forced_refresh_for_thirty_minutes(self):
        url = "https://eq.10jqka.com.cn/open/api/hot_list/v1/hot_stock/a/hour/data.txt"
        with tempfile.TemporaryDirectory() as folder, patch.object(market_network.data_store, "DB_PATH", os.path.join(folder, "bars.db")), patch.object(market_network.time, "time", return_value=1_000_000), patch.dict(market_network._PROVIDER_BLOCKS, {}, clear=True), patch("urllib.request.build_opener") as builder:
            response = builder.return_value.open.return_value
            response.getcode.return_value = 200
            market_network.urlopen(urllib.request.Request(url))
            scope = market_network._provider_scope(url, "10jqka")
            self.assertEqual(market_network._provider_block("10jqka", scope)[1] - 1_000_000, 1800)
            with self.assertRaises(urllib.error.HTTPError) as limited:
                market_network.urlopen(urllib.request.Request(url))
            self.assertEqual(limited.exception.code, 429)
            builder.return_value.open.assert_called_once()

    def test_retry_after_http_date_is_honored(self):
        with tempfile.TemporaryDirectory() as folder, patch.object(market_network.data_store, "DB_PATH", os.path.join(folder, "bars.db")), patch.object(market_network.time, "time", return_value=1_000_000):
            retry_at = market_network.email.utils.formatdate(1_004_000, usegmt=True)
            market_network._record_provider_block("eastmoney", 429, retry_at)
            self.assertEqual(market_network._provider_block("eastmoney")[1] - 1_000_000, 4000)

    def test_board_cooldown_is_scoped_per_board_resource(self):
        first = "https://push2.eastmoney.com/api/qt/clist/get?fs=b%3ABK001"
        second = "https://push2.eastmoney.com/api/qt/clist/get?fs=b%3ABK002"
        self.assertNotEqual(market_network._provider_scope(first, "eastmoney"), market_network._provider_scope(second, "eastmoney"))
        self.assertTrue(market_network._is_heat_or_sector_request(first))

    def test_hot_and_sector_cache_ttl_is_thirty_minutes(self):
        self.assertEqual(market_cache._ttl_for("https://eq.10jqka.com.cn/open/api/hot_stock"), 1800)
        self.assertEqual(market_cache._ttl_for("https://push2.eastmoney.com/api/qt/clist/sector"), 1800)
        self.assertEqual(market_cache._ttl_for("https://data.10jqka.com.cn/notices/detail"), 300)
        self.assertEqual(market_cache._ttl_for("https://push2.eastmoney.com/api/qt/clist/get?fs=b%3ABK001"), 1800)

    def test_force_failure_does_not_return_stale(self):
        cached = (200, {}, b"local", 1000, 1001)
        with patch.object(market_cache, "_read_cached", return_value=cached), patch.object(market_cache.time, "time", return_value=1002), patch.object(market_network, "urlopen", side_effect=OSError("connection refused")):
            with self.assertRaisesRegex(OSError, "connection refused"):
                market_cache.urlopen("https://api.kraken.com/0/public/OHLC?pair=XBTUSD", force_refresh=True)
            with market_cache.urlopen("https://api.kraken.com/0/public/OHLC?pair=XBTUSD") as response:
                self.assertEqual(response.read(), b"local")
                self.assertEqual(response.headers["X-Stock-Cache"], "STALE")


    def test_us_upsert_rejects_partial_and_invalid_bars(self):
        import sqlite3
        import bars_us
        import trading_calendar
        from unittest.mock import MagicMock
        connection = sqlite3.connect(":memory:")
        connection.execute("CREATE TABLE daily_bars(code,date,open,high,low,close,prev_close,volume,amount,source,market)")
        wrapped = MagicMock(wraps=connection)
        wrapped.close.side_effect = lambda: None
        valid = ("2026-10-07", 100, 120, 90, 110, 110, 10)
        invalid = ("2026-10-07", 100, 95, 90, 110, 110, 10)
        partial = ("2026-10-08", 100, 120, 90, 110, 110, 10)
        try:
            with patch.object(bars_us, "db", return_value=wrapped), patch.object(trading_calendar, "today_str", return_value="2026-10-08"), patch.object(trading_calendar, "us_market_closed", return_value=False), patch.object(trading_calendar, "prev_trading_day", return_value="2026-10-07"):
                self.assertEqual(bars_us._upsert("AAPL", [valid, invalid, partial], "test"), 1)
                self.assertEqual(connection.execute("SELECT date,close FROM daily_bars").fetchall(), [("2026-10-07", 110)])
                wrapped.close.assert_called_once()
                with self.assertRaisesRegex(RuntimeError, "no valid completed"):
                    bars_us._upsert("AAPL", [partial], "test")
        finally:
            connection.close()

if __name__ == "__main__":
    unittest.main()
