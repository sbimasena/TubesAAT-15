"""Operator-side P3 wire handling and evidence safety; no Docker required."""
import importlib.util
from pathlib import Path
import tempfile
import unittest


spec = importlib.util.spec_from_file_location("p3_check", Path(__file__).resolve().parents[1] / "scripts/m2/p3-check.py")
p3 = importlib.util.module_from_spec(spec)
spec.loader.exec_module(p3)


class P3CheckTests(unittest.TestCase):
    def test_wire_items_preserve_invalid_bytes_and_quoted_delimiters(self):
        first = b'{"report_id":"a","note":"comma, and bracket ] and quote \\\""}'
        second = b'{"report_id":"b","note":"\xff\xfe"}'
        self.assertEqual(p3.wire_items(b"[" + first + b", " + second + b"]"), [first, second])

    def test_wire_items_do_not_decode_deep_json(self):
        item = b'{"x":' + b"[" * 20000 + b"0" + b"]" * 20000 + b"}"
        self.assertEqual(p3.wire_items(b"[" + item + b"]"), [item])

    def test_incomplete_wire_is_not_reported_as_a_complete_item(self):
        for raw in (b'[{"a":1}', b'[{"a":"unfinished}]', b'[{"a":1]]'):
            with self.subTest(raw=raw), self.assertRaises(AssertionError):
                p3.wire_items(raw)

    def test_evidence_redacts_tokens_and_credentials(self):
        with tempfile.TemporaryDirectory() as directory:
            folder = Path(directory)
            check = p3.Check(folder, folder, 5, "golang:1.27.1")
            check.sensitive = ["test-password-value", "test-access-token-value"]
            check.evidence["failure"] = "test-password-value test-access-token-value"
            check.save()
            saved = (folder / "p3-check.json").read_text()
            self.assertNotIn("test-password-value", saved)
            self.assertNotIn("test-access-token-value", saved)
            self.assertIn("[REDACTED]", saved)


if __name__ == "__main__":
    unittest.main()
