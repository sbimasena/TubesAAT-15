"""Verdict tests for scripts/m2/audit-run.py; no cluster."""
import importlib.util
import sys
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts/m2"))
spec = importlib.util.spec_from_file_location("audit_run", ROOT / "scripts/m2/audit-run.py")
ar = importlib.util.module_from_spec(spec)
spec.loader.exec_module(ar)

OK_LOC = {"locations": [{"node": "node-a"}, {"node": "node-c"}], "partial": False}


def sums(count, partial=False):
    return {s: {"count": count, "partial": partial} for s in ar.SOURCES}


class VerdictTests(unittest.TestCase):
    def test_pass(self):
        self.assertEqual(ar.verdict(sums(10), sums(12), ["a", "b"], {"x": OK_LOC}), (True, []))

    def test_fail_reasons(self):
        one_node = {"locations": [{"node": "node-a"}, {"node": "node-a"}], "partial": False}
        passed, reasons = ar.verdict(sums(10), sums(9, partial=True), ["a", "a"], {"x": one_node})
        self.assertFalse(passed)
        self.assertEqual(len(reasons), 2 * len(ar.SOURCES) + 2)


if __name__ == "__main__":
    unittest.main()
