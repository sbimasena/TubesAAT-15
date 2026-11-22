"""Pure-helper tests for scripts/m2/p5-slo-fetch.py."""
import importlib.util
from pathlib import Path
import tempfile
import json
import unittest

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("p5_slo_fetch", ROOT / "scripts/m2/p5-slo-fetch.py")
psf = importlib.util.module_from_spec(spec)
spec.loader.exec_module(psf)


class P5SloFetchTests(unittest.TestCase):
    def test_evaluate_panels_blocked_missing(self):
        results = {"ratio_within_5s": {"state": "NO_DATA"}}
        evaluation = psf.evaluate_panels(results)
        self.assertEqual(evaluation["result"], "BLOCKED")

    def test_evaluate_panels_pass(self):
        results = {
            "ratio_within_5s": {"state": "VALUE", "value": 0.995},
            "p95_ack_seconds": {"state": "VALUE", "value": 2.0},
            "error_budget_remaining_1h": {"state": "VALUE", "value": 0.5},
            "burn_rate_5m": {"state": "VALUE", "value": 0.5},
        }
        evaluation = psf.evaluate_panels(results)
        self.assertEqual(evaluation["result"], "PASS")

    def test_evaluate_panels_fail_ratio(self):
        results = {
            "ratio_within_5s": {"state": "VALUE", "value": 0.9},
            "p95_ack_seconds": {"state": "VALUE", "value": 2.0},
            "error_budget_remaining_1h": {"state": "VALUE", "value": 0.5},
            "burn_rate_5m": {"state": "VALUE", "value": 0.5},
        }
        evaluation = psf.evaluate_panels(results)
        self.assertEqual(evaluation["result"], "FAIL")

    def test_evaluate_panels_fail_p95(self):
        results = {
            "ratio_within_5s": {"state": "VALUE", "value": 0.995},
            "p95_ack_seconds": {"state": "VALUE", "value": 6.0},
            "error_budget_remaining_1h": {"state": "VALUE", "value": 0.5},
            "burn_rate_5m": {"state": "VALUE", "value": 0.5},
        }
        evaluation = psf.evaluate_panels(results)
        self.assertEqual(evaluation["result"], "FAIL")

    def test_journal_cross_check(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "events.jsonl"
            records = [
                {"message_id": "m1", "payload": {"source": "SENSOR_PANTAI", "attributes": {"critical": True}},
                 "event_age_ms": 1000},
                {"message_id": "m2", "payload": {"source": "BMKG", "attributes": {"critical": True}},
                 "event_age_ms": 2000},
            ]
            path.write_text("".join(json.dumps(r) + "\n" for r in records), encoding="utf-8")
            result = psf.journal_cross_check(str(path))
            self.assertEqual(result["critical_alerts"], 1)
            self.assertEqual(result["within_5s_pre_append"], 1)
            self.assertEqual(result["pre_append_p95_ms"], 1000.0)

    def test_journal_cross_check_empty(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "events.jsonl"
            path.write_text("", encoding="utf-8")
            result = psf.journal_cross_check(str(path))
            self.assertEqual(result["critical_alerts"], 0)
            self.assertIsNone(result["pre_append_p95_ms"])


if __name__ == "__main__":
    unittest.main()
