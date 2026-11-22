"""Pure-helper tests for scripts/m2/p5-critical-alert.py."""
import importlib.util
from pathlib import Path
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("p5_critical_alert", ROOT / "scripts/m2/p5-critical-alert.py")
pca = importlib.util.module_from_spec(spec)
spec.loader.exec_module(pca)


class P5CriticalAlertTests(unittest.TestCase):
    def test_evaluate_blocked_low_alerts(self):
        evaluation = pca.evaluate(5, 5, 20, 1.0, 1.0, None)
        self.assertEqual(evaluation["result"], "BLOCKED")

    def test_evaluate_blocked_low_samples(self):
        evaluation = pca.evaluate(20, 5, 20, 1.0, 1.0, None)
        self.assertEqual(evaluation["result"], "BLOCKED")

    def test_evaluate_fail_ratio(self):
        evaluation = pca.evaluate(20, 20, 20, 0.9, 1.0, None)
        self.assertEqual(evaluation["result"], "FAIL")

    def test_evaluate_fail_p95(self):
        evaluation = pca.evaluate(20, 20, 20, 1.0, 6.0, None)
        self.assertEqual(evaluation["result"], "FAIL")

    def test_evaluate_partial_without_baseline(self):
        evaluation = pca.evaluate(20, 20, 20, 1.0, 1.0, None)
        self.assertEqual(evaluation["result"], "PARTIAL")

    def test_evaluate_pass_with_baseline(self):
        reconciliation = {"missing": [], "unexpected": [], "observed": 20, "expected": 20, "kind": "message_id"}
        evaluation = pca.evaluate(20, 20, 20, 1.0, 1.0, reconciliation)
        self.assertEqual(evaluation["result"], "PASS")

    def test_evaluate_fail_missing_expected(self):
        reconciliation = {"missing": ["x"], "unexpected": [], "observed": 19, "expected": 20, "kind": "message_id"}
        evaluation = pca.evaluate(20, 20, 20, 1.0, 1.0, reconciliation)
        self.assertEqual(evaluation["result"], "FAIL")

    def test_reconcile_expected_auto_hazard(self):
        critical = [{"message_id": "m1", "hazard_id": "haz_abc"}]
        reconciliation = pca.reconcile_expected(critical, ["haz_abc"], "auto")
        self.assertEqual(reconciliation["kind"], "hazard_id")
        self.assertEqual(reconciliation["missing"], [])

    def test_reconcile_expected_auto_message(self):
        critical = [{"message_id": "m1", "hazard_id": "haz_abc"}]
        reconciliation = pca.reconcile_expected(critical, ["m1"], "auto")
        self.assertEqual(reconciliation["kind"], "message_id")
        self.assertEqual(reconciliation["missing"], [])

    def test_reconcile_expected_none(self):
        self.assertIsNone(pca.reconcile_expected([], [], "auto"))

    def test_metric_queries_contains_window(self):
        queries = pca.metric_queries("15m")
        self.assertIn("15m", queries["ack_total"])
        self.assertIn("age_seconds", queries["age_samples"])

    def test_journal_window_filters(self):
        import datetime as dt
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "events.jsonl"
            path.write_text("", encoding="utf-8")
            records, critical = pca.journal_window(str(path), dt.datetime.now(dt.timezone.utc), dt.datetime.now(dt.timezone.utc))
            self.assertEqual(records, [])
            self.assertEqual(critical, [])


if __name__ == "__main__":
    unittest.main()
