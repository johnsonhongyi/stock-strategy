import io
import json
import unittest
import urllib.error
from unittest.mock import patch
import crypto_universe as universe

class UniverseRefreshTests(unittest.TestCase):
    def test_transient_connection_retries_once_and_requires_fresh_response(self):
        response = io.BytesIO(json.dumps([{"symbol": "BTCUSDT"}]).encode())
        with patch.object(universe.market_cache, "urlopen", side_effect=[urllib.error.URLError("temporary TLS failure"), response]) as read, patch.object(universe.time, "sleep") as sleep:
            self.assertEqual(universe._get("https://data-api.binance.vision/api/v3/ticker/24hr"), [{"symbol": "BTCUSDT"}])
            self.assertEqual(read.call_count, 2)
            self.assertTrue(all(call.kwargs["force_refresh"] for call in read.call_args_list))
            sleep.assert_called_once_with(1)
        self.assertTrue(response.closed)

    def test_throttling_does_not_retry(self):
        error = urllib.error.HTTPError("https://fixture", 429, "limited", {}, None)
        with patch.object(universe.market_cache, "urlopen", side_effect=error) as read, patch.object(universe.time, "sleep") as sleep:
            with self.assertRaises(urllib.error.HTTPError): universe._get("https://fixture")
            read.assert_called_once(); sleep.assert_not_called()

    def test_persistent_transport_failure_stops_after_two_attempts(self):
        with patch.object(universe.market_cache, "urlopen", side_effect=OSError("TLS EOF")) as read, patch.object(universe.time, "sleep"):
            with self.assertRaises(OSError): universe._get("https://fixture")
            self.assertEqual(read.call_count, 2)

if __name__ == "__main__": unittest.main()
