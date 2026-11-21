import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


SCRIPT = Path(__file__).resolve().parents[1] / "scripts/m2/slo-check.py"


def run(journal, *args):
    return subprocess.run(
        [sys.executable, str(SCRIPT), "--journal", str(journal), *args],
        capture_output=True, text=True, check=False,
    )


class SloCheckTest(unittest.TestCase):
    def journal(self, directory, records):
        path = Path(directory) / "events.jsonl"
        path.write_text("".join(json.dumps(record) + "\n" for record in records), encoding="utf-8")
        return path

    def test_percentile_within_target_passes(self):
        with tempfile.TemporaryDirectory() as directory:
            path = self.journal(directory, [
                {"message_id": f"e{index}", "event_age_ms": 100 + index * 40}
                for index in range(100)
            ])
            result = run(path, "--expected", "100")
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertIn("RESULT: PASS", result.stdout)
            self.assertIn("lost: 0 of 100", result.stdout)

    def test_slow_tail_and_loss_fail(self):
        with tempfile.TemporaryDirectory() as directory:
            records = [{"message_id": f"e{index}", "event_age_ms": 100} for index in range(100)]
            records.append({"message_id": "slow", "event_age_ms": 9000})
            result = run(self.journal(directory, records), "--expected", "102")
            self.assertEqual(result.returncode, 1)
            self.assertIn("RESULT: FAIL", result.stdout)
            self.assertIn("lost: 1 of 102", result.stdout)

    def test_missing_age_is_excluded_and_duplicates_count_once(self):
        with tempfile.TemporaryDirectory() as directory:
            path = self.journal(directory, [
                {"message_id": "e1", "event_age_ms": 250},
                {"message_id": "e1", "event_age_ms": 250},
                {"message_id": "old", "event_age_ms": -1},
            ])
            result = run(path)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertIn("2 unique, 1 measured, 1 without age", result.stdout)

    def test_empty_journal_is_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            result = run(self.journal(directory, []))
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("no event_age_ms samples", result.stderr)


if __name__ == "__main__":
    unittest.main()
