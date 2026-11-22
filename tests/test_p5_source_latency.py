"""Pure-helper tests for scripts/m2/p5-source-latency.py."""
import importlib.util
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("p5_source_latency", ROOT / "scripts/m2/p5-source-latency.py")
psl = importlib.util.module_from_spec(spec)
spec.loader.exec_module(psl)


class P5SourceLatencyTests(unittest.TestCase):
    def test_summarize_phase_pass(self):
        samples = [{"status": 200, "latency_ms": 10.0}, {"status": 200, "latency_ms": 20.0}]
        summary = psl.summarize_phase(samples, 100)
        self.assertTrue(summary["pass"])
        self.assertEqual(summary["samples"], 2)

    def test_summarize_phase_fail_on_http_error(self):
        samples = [{"status": 200, "latency_ms": 10.0}, {"status": 500, "latency_ms": 20.0}]
        summary = psl.summarize_phase(samples, 100)
        self.assertFalse(summary["pass"])
        self.assertEqual(summary["status_counts"]["500"], 1)

    def test_summarize_phase_empty(self):
        summary = psl.summarize_phase([], 100)
        self.assertFalse(summary["pass"])
        self.assertEqual(summary["samples"], 0)

    def test_coastal_snapshot(self):
        body = {"coastal": {"source": {"available": True, "stale": False},
                            "breaker": {"state": "closed", "rejected": 0}}}
        snapshot = psl.coastal_snapshot(body)
        self.assertEqual(snapshot["breaker_state"], "closed")
        self.assertTrue(snapshot["available"])

    def test_coastal_snapshot_missing(self):
        self.assertEqual(psl.coastal_snapshot({}), {})

    def test_evaluate_blocked_without_outage(self):
        result = psl.evaluate({}, False)
        self.assertEqual(result["result"], "BLOCKED")

    def test_evaluate_pass(self):
        phases = {
            "baseline": {"samples": 2, "pass": True, "p95_ms": 10.0},
            "during_outage": {"samples": 2, "pass": True, "p95_ms": 20.0},
            "after_recovery": {"samples": 2, "pass": True, "p95_ms": 15.0},
        }
        result = psl.evaluate(phases, True)
        self.assertEqual(result["result"], "PASS")

    def test_evaluate_fail(self):
        phases = {
            "baseline": {"samples": 2, "pass": True, "p95_ms": 10.0},
            "during_outage": {"samples": 2, "pass": False, "p95_ms": 400.0},
            "after_recovery": {"samples": 2, "pass": True, "p95_ms": 15.0},
        }
        result = psl.evaluate(phases, True)
        self.assertEqual(result["result"], "FAIL")


if __name__ == "__main__":
    unittest.main()
