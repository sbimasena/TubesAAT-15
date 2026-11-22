"""Pure-helper tests for scripts/m2/p3-retryable.py."""
import importlib.util
from pathlib import Path
import datetime
import unittest

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("p3_retryable", ROOT / "scripts/m2/p3-retryable.py")
pr = importlib.util.module_from_spec(spec)
spec.loader.exec_module(pr)


class P3RetryableTests(unittest.TestCase):
    def test_parse_timestamp_z(self):
        stamp = pr.parse_timestamp("2026-01-01T00:00:00Z")
        self.assertEqual(stamp, datetime.datetime(2026, 1, 1, tzinfo=datetime.timezone.utc))

    def test_parse_timestamp_offset(self):
        stamp = pr.parse_timestamp("2026-01-01T02:00:00+02:00")
        self.assertEqual(stamp, datetime.datetime(2026, 1, 1, tzinfo=datetime.timezone.utc))

    def test_parse_timestamp_none(self):
        self.assertIsNone(pr.parse_timestamp(None))
        self.assertIsNone(pr.parse_timestamp("not-a-date"))

    def test_exit_codes(self):
        self.assertEqual(pr.EXIT_CODES["PASS"], 0)
        self.assertEqual(pr.EXIT_CODES["BLOCKED"], 3)

    def test_constraint_constant(self):
        self.assertEqual(pr.CONSTRAINT, "p3_retryable_reject_report")


if __name__ == "__main__":
    unittest.main()
