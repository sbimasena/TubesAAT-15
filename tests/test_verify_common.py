"""Pure-helper tests for scripts/m2/verify_common.py; no Docker or cluster."""
import importlib.util
from pathlib import Path
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("verify_common", ROOT / "scripts/m2/verify_common.py")
vc = importlib.util.module_from_spec(spec)
spec.loader.exec_module(vc)


class VerifyCommonTests(unittest.TestCase):
    def test_percentile_nearest_rank(self):
        self.assertEqual(vc.percentile([1, 2, 3, 4], 0.5), 2)
        self.assertEqual(vc.percentile([1, 2, 3, 4], 0.95), 4)
        self.assertEqual(vc.percentile([5], 1.0), 5)

    def test_percentile_empty_raises(self):
        with self.assertRaises(AssertionError):
            vc.percentile([], 0.5)

    def test_summarize_latencies(self):
        summary = vc.summarize_latencies([10, 20, 30, 40, 50], 100)
        self.assertEqual(summary["count"], 5)
        self.assertEqual(summary["p95_ms"], 50.0)
        self.assertTrue(summary["pass"])
        self.assertFalse(vc.summarize_latencies([10, 200], 50)["pass"])

    def test_resolve_token_env(self):
        import os
        os.environ["TEST_TOKEN_VAR"] = "  secret-value  "
        try:
            value, source = vc.resolve_token("TEST_TOKEN_VAR", None, "label")
            self.assertEqual(value, "secret-value")
            self.assertEqual(source, "env:TEST_TOKEN_VAR")
        finally:
            del os.environ["TEST_TOKEN_VAR"]

    def test_resolve_token_file(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "token.txt"
            path.write_text("file-token\n", encoding="utf-8")
            value, source = vc.resolve_token(None, str(path), "label")
            self.assertEqual(value, "file-token")
            self.assertEqual(source, "file")

    def test_resolve_token_requires_exactly_one(self):
        with self.assertRaises(AssertionError):
            vc.resolve_token(None, None, "label")
        with self.assertRaises(AssertionError):
            vc.resolve_token("A", "B", "label")

    def test_resolve_token_rejects_empty(self):
        import os
        os.environ["EMPTY_TOKEN_VAR"] = "   "
        try:
            with self.assertRaises(AssertionError):
                vc.resolve_token("EMPTY_TOKEN_VAR", None, "label")
        finally:
            del os.environ["EMPTY_TOKEN_VAR"]

    def test_summarize_error_http(self):
        import urllib.error
        error = urllib.error.HTTPError("http://x", 503, "boom", {}, None)
        self.assertEqual(vc.summarize_error(error), "http_503")

    def test_critical_alerts_filters_pantai(self):
        records = [
            {"message_id": "m1", "payload": {"source": "SENSOR_PANTAI", "attributes": {"critical": True}}},
            {"message_id": "m2", "payload": {"source": "BMKG", "attributes": {"critical": True}}},
            {"message_id": "m1", "payload": {"source": "SENSOR_PANTAI", "attributes": {"critical": True}}},
            {"message_id": "m3", "payload": {"source": "SENSOR_PANTAI", "attributes": {"critical": False}}},
        ]
        result = vc.critical_alerts(records)
        self.assertEqual(len(result), 1)
        self.assertEqual(result[0]["message_id"], "m1")

    def test_critical_alerts_handles_string_payload(self):
        records = [{"message_id": "m1", "payload": '{"source":"SENSOR_PANTAI","attributes":{"critical":true}}'}]
        result = vc.critical_alerts(records)
        self.assertEqual(len(result), 1)

    def test_reconcile_ids(self):
        result = vc.reconcile_ids({"a", "b"}, {"a", "c"})
        self.assertEqual(result["missing"], ["c"])
        self.assertEqual(result["unexpected"], ["b"])
        self.assertEqual(result["observed"], 2)
        self.assertEqual(result["expected"], 2)

    def test_parse_ids_json_array(self):
        self.assertEqual(vc.parse_ids('["a","b"]'), ["a", "b"])

    def test_parse_ids_json_object(self):
        self.assertEqual(vc.parse_ids('{"message_ids":["x","y"]}'), ["x", "y"])
        self.assertEqual(vc.parse_ids('{"ids":["z"]}'), ["z"])

    def test_parse_ids_lines(self):
        self.assertEqual(vc.parse_ids("a\n# comment\nb"), ["a", "b"])

    def test_parse_ids_empty(self):
        self.assertEqual(vc.parse_ids(""), [])

    def test_parse_window(self):
        self.assertEqual(vc.parse_window("30s"), (30, "30s"))
        self.assertEqual(vc.parse_window("5m"), (300, "5m"))
        self.assertEqual(vc.parse_window("1h"), (3600, "1h"))
        with self.assertRaises(AssertionError):
            vc.parse_window("0s")
        with self.assertRaises(AssertionError):
            vc.parse_window("5x")

    def test_prom_result_states(self):
        self.assertEqual(vc.prom_result({"status": "success", "data": {"result": []}}), {"state": "NO_DATA"})
        self.assertEqual(vc.prom_result({"status": "error", "errorType": "bad"}), {"state": "ERROR", "error": "bad"})
        self.assertEqual(vc.prom_result({"status": "success", "data": {"result": [{"value": [1, "NaN"]}]}}), {"state": "NO_DATA", "raw": "NaN"})
        self.assertEqual(vc.prom_result({"status": "success", "data": {"result": [{"value": [1, "1.5"]}]}}), {"state": "VALUE", "value": 1.5})
        self.assertEqual(vc.prom_result("not-a-dict"), {"state": "ERROR", "error": "invalid_response"})

    def test_chunked(self):
        self.assertEqual(vc.chunked([1, 2, 3, 4, 5], 2), [[1, 2], [3, 4], [5]])

    def test_parse_csv_ids(self):
        self.assertEqual(vc.parse_csv_ids("a, b , ,c"), ["a", "b", "c"])

    def test_message_id_regex(self):
        self.assertTrue(bool(vc.MESSAGE_ID_RE.match("pvmbg:" + "a" * 64)))
        self.assertFalse(bool(vc.MESSAGE_ID_RE.match("pvmbg:" + "A" * 64)))
        self.assertFalse(bool(vc.MESSAGE_ID_RE.match("pvmbg:short")))

    def test_exit_codes(self):
        self.assertEqual(vc.EXIT_CODES["PASS"], 0)
        self.assertEqual(vc.EXIT_CODES["BLOCKED"], 3)

    def test_http_json_does_not_raise(self):
        result = vc.http_json("http://127.0.0.1:1", "/x", timeout=1)
        self.assertIn("error", result)
        self.assertEqual(result["status"], 0)


if __name__ == "__main__":
    unittest.main()
