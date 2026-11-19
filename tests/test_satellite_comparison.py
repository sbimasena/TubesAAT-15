import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


SCRIPT = Path(__file__).resolve().parents[1] / "scripts/m2/compare-satellite.py"


def fire(keys, severity="WASPADA"):
    return {
        "source": "SATELIT", "hazard_type": "WILDFIRE",
        "area_name": "Kalimantan Tengah", "severity": severity,
        "latitude": -2.51, "longitude": 113.80, "occurred_at": "2026-11-12T02:43:00Z",
        "attributes": {
            "detection_keys": keys, "detection_count": len(keys),
            "satellites": ["N20", "N21"], "max_confidence": "h", "total_frp_mw": 20,
        },
    }


class SatelliteComparisonTests(unittest.TestCase):
    def compare(self, actual, reference):
        with tempfile.TemporaryDirectory() as directory:
            paths = [Path(directory) / name for name in ("actual.json", "reference.json")]
            for path, payload in zip(paths, (actual, reference)):
                path.write_text(json.dumps(payload), encoding="utf-8")
            return subprocess.run([sys.executable, SCRIPT, *paths], capture_output=True, text=True)

    def test_reordered_membership_matches(self):
        result = self.compare({"partial": False, "next_cursor": None, "items": [fire(["b", "a"])]},
                              [fire(["a", "b"])])
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_reports_missing_extra_and_changed_fires(self):
        result = self.compare([fire(["a", "b"]), fire(["e", "f"])],
                              [fire(["a", "b"], "SIAGA"), fire(["c", "d"])])
        self.assertEqual(result.returncode, 1, result.stderr)
        summary = json.loads(result.stdout)
        self.assertEqual(summary["missing"], [["c", "d"]])
        self.assertEqual(summary["extra"], [["e", "f"]])
        self.assertEqual(summary["mismatches"], [{
            "detection_keys": ["a", "b"],
            "fields": {"severity": {"actual": "WASPADA", "reference": "SIAGA"}},
        }])

    def test_rejects_incomplete_export(self):
        result = self.compare({"partial": False, "items": [], "next_cursor": "next"}, [])
        self.assertEqual(result.returncode, 2)
        self.assertIn("following every Audit cursor", result.stderr)
