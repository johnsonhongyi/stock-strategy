import io
import json
import os
import tempfile
import unittest
from unittest.mock import patch
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
                with open(path, "w", encoding="utf-8") as handle:
                    json.dump({"proxy_url": ""}, handle)
                market_network.urlopen("https://api.kraken.com/0/public/OHLC")
                self.assertEqual(builder.call_args.args[0].proxies, {})

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
