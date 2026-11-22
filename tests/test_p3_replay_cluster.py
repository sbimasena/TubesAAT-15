"""Pure-helper tests for scripts/m2/p3-replay-cluster.py."""
import importlib.util
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("p3_replay_cluster", ROOT / "scripts/m2/p3-replay-cluster.py")
prc = importlib.util.module_from_spec(spec)
# Avoid executing main(); module has no top-level main call, so exec is safe.
spec.loader.exec_module(prc)


class P3ReplayClusterTests(unittest.TestCase):
    def test_validate_message_ids_rejects_bad(self):
        with self.assertRaises(AssertionError):
            prc.validate_message_ids(["not-a-pvmbg-id"])
        with self.assertRaises(AssertionError):
            prc.validate_message_ids(["pvmbg:" + "A" * 64])
        valid = "pvmbg:" + "a" * 64
        self.assertEqual(prc.validate_message_ids([valid, valid]), [valid])

    def test_index_events_detects_duplicates(self):
        events = [
            {"source_ref_id": "r1", "hazard_id": "h1", "occurred_at": "2026-01-01T00:00:00Z"},
            {"source_ref_id": "r1", "hazard_id": "h2", "occurred_at": "2026-01-01T00:00:01Z"},
        ]
        by_ref, violations = prc.index_events(events)
        self.assertEqual(len(by_ref), 1)
        self.assertTrue(any(v["violation"] == "duplicate_source_ref_id" for v in violations))

    def test_verify_ordering_detects_decrease(self):
        before = [{"source_ref_id": "r1", "hazard_id": "h1", "occurred_at": "2026-01-01T00:00:05Z"}]
        after = [{"source_ref_id": "r1", "hazard_id": "h1", "occurred_at": "2026-01-01T00:00:01Z"}]
        result = prc.verify_ordering(before, after)
        self.assertTrue(any(v["violation"] == "occurred_at_decreased" for v in result["violations"]))

    def test_verify_ordering_detects_hazard_change(self):
        before = [{"source_ref_id": "r1", "hazard_id": "h1", "occurred_at": "2026-01-01T00:00:01Z"}]
        after = [{"source_ref_id": "r1", "hazard_id": "h2", "occurred_at": "2026-01-01T00:00:02Z"}]
        result = prc.verify_ordering(before, after)
        self.assertTrue(any(v["violation"] == "hazard_id_changed" for v in result["violations"]))

    def test_verify_ordering_accepts_stable(self):
        before = [{"source_ref_id": "r1", "hazard_id": "h1", "occurred_at": "2026-01-01T00:00:01Z"}]
        after = [{"source_ref_id": "r1", "hazard_id": "h1", "occurred_at": "2026-01-01T00:00:02Z"}]
        result = prc.verify_ordering(before, after)
        self.assertEqual(result["violations"], [])
        self.assertEqual(result["new_events"], [])

    def test_verify_ordering_reports_new_events(self):
        before = []
        after = [{"source_ref_id": "r2", "hazard_id": "h2", "occurred_at": "2026-01-01T00:00:02Z"}]
        result = prc.verify_ordering(before, after)
        self.assertEqual(len(result["new_events"]), 1)

    def test_summarize_inventory(self):
        items = [
            {"message_id": "m1", "status": "parked", "reason": "schema_incompatible"},
            {"message_id": "m2", "status": "retrying", "reason": "store_unavailable"},
            {"message_id": "m3", "status": "parked", "reason": "schema_incompatible"},
        ]
        summary = prc.summarize_inventory(items)
        self.assertEqual(summary["total"], 3)
        self.assertEqual(summary["by_status"]["parked"], 2)
        self.assertEqual(summary["by_reason"]["schema_incompatible"], 2)


if __name__ == "__main__":
    unittest.main()
