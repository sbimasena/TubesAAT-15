"""Pure-helper tests for scripts/m2/p5-breaker-timeline.py."""
import importlib.util
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("p5_breaker_timeline", ROOT / "scripts/m2/p5-breaker-timeline.py")
pbt = importlib.util.module_from_spec(spec)
spec.loader.exec_module(pbt)


class P5BreakerTimelineTests(unittest.TestCase):
    def test_breaker_from_status_reads_nested(self):
        body = {"coastal": {"breaker": {"state": "open", "rejected": 3}}}
        self.assertEqual(pbt.breaker_from_status(body)["state"], "open")

    def test_breaker_from_status_missing(self):
        self.assertIsNone(pbt.breaker_from_status({}))
        self.assertIsNone(pbt.breaker_from_status({"coastal": "bad"}))

    def test_source_from_status(self):
        body = {"coastal": {"source": {"available": True, "stale": False}}}
        self.assertTrue(pbt.source_from_status(body)["available"])

    def test_summarize_timeline_pass(self):
        timeline = [
            {"label": "baseline", "breaker": {"state": "closed", "rejected": 0, "attempts": 0, "retries": 0},
             "client_api_status": 200, "client_api_latency_ms": 10.0},
            {"label": "outage", "breaker": {"state": "open", "rejected": 2, "attempts": 2, "retries": 1},
             "client_api_status": 200, "client_api_latency_ms": 20.0},
            {"label": "recovery", "breaker": {"state": "closed", "rejected": 2, "attempts": 2, "retries": 1},
             "client_api_status": 200, "client_api_latency_ms": 15.0},
        ]
        summary = pbt.summarize_timeline(timeline, 1000.0)
        self.assertEqual(summary["result"], "PASS")
        self.assertTrue(summary["opened"])
        self.assertTrue(summary["recovered"])
        self.assertEqual(summary["rejected_delta"], 2)

    def test_late_sensor_503_after_recovery_still_counts_as_recovered(self):
        timeline = [
            {"label": "baseline", "breaker": {"state": "open", "rejected": 0}},  # left over from a previous run
            {"label": "outage", "breaker": {"state": "open", "rejected": 2}},
            {"label": "recovery", "breaker": {"state": "closed", "rejected": 2}},
            {"label": "recovery", "breaker": {"state": "open", "rejected": 3}},
        ]
        summary = pbt.summarize_timeline(timeline, 1000.0)
        self.assertTrue(summary["recovered"])
        self.assertEqual(summary["result"], "PARTIAL")

    def test_open_only_in_baseline_is_not_an_outage_open(self):
        timeline = [
            {"label": "baseline", "breaker": {"state": "open", "rejected": 0}},
            {"label": "outage", "breaker": {"state": "closed", "rejected": 1}},
            {"label": "recovery", "breaker": {"state": "closed", "rejected": 1}},
        ]
        self.assertFalse(pbt.summarize_timeline(timeline, 1000.0)["opened"])

    def test_summarize_timeline_fail_not_opened(self):
        timeline = [
            {"breaker": {"state": "closed", "rejected": 0, "attempts": 0, "retries": 0}},
        ]
        summary = pbt.summarize_timeline(timeline, 1000.0)
        self.assertEqual(summary["result"], "FAIL")
        self.assertFalse(summary["opened"])

    def test_summarize_timeline_partial_no_client(self):
        timeline = [
            {"label": "baseline", "breaker": {"state": "closed", "rejected": 0, "attempts": 0, "retries": 0}},
            {"label": "outage", "breaker": {"state": "open", "rejected": 2, "attempts": 2, "retries": 1}},
            {"label": "recovery", "breaker": {"state": "closed", "rejected": 2, "attempts": 2, "retries": 1}},
        ]
        summary = pbt.summarize_timeline(timeline, 1000.0)
        self.assertEqual(summary["result"], "PARTIAL")
        self.assertIn("Client API was not measured", summary["reasons"])

    def test_summarize_timeline_client_failure(self):
        timeline = [
            {"label": "baseline", "breaker": {"state": "closed", "rejected": 0, "attempts": 0, "retries": 0},
             "client_api_status": 200, "client_api_latency_ms": 10.0},
            {"label": "outage", "breaker": {"state": "open", "rejected": 2, "attempts": 2, "retries": 1},
             "client_api_status": 500, "client_api_latency_ms": 20.0},
            {"label": "recovery", "breaker": {"state": "closed", "rejected": 2, "attempts": 2, "retries": 1},
             "client_api_status": 200, "client_api_latency_ms": 15.0},
        ]
        summary = pbt.summarize_timeline(timeline, 1000.0)
        self.assertEqual(summary["result"], "FAIL")


if __name__ == "__main__":
    unittest.main()
