import importlib.util
import io
import json
from email.message import Message
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("sample", Path(__file__).resolve().parents[1] / "scripts/m2/satellite-sample.py")
sample = importlib.util.module_from_spec(spec)
spec.loader.exec_module(sample)


class Response(io.BytesIO):
    def __init__(self, body, content_type="application/json"):
        super().__init__(body)
        self.headers = Message()
        self.headers["Content-Type"] = content_type


class SatelliteSampleTest(unittest.TestCase):
    def test_capture_preserves_raw_stream_and_authenticates(self):
        raw = b'event: detection\nid: 7\ndata: {"latitude":-2.51230,"acq_time":243}\n\nevent: heartbeat\nid: 8\ndata: {}\n\n'
        with tempfile.TemporaryDirectory() as directory:
            with patch.object(sample.urllib.request, "urlopen", side_effect=[Response(b'{"latest_offset":8}'), Response(raw, "text/event-stream")]) as call:
                result = sample.capture("https://example.test", "secret", Path(directory), 1, 7)
            self.assertEqual((Path(directory) / "stream.sse").read_bytes(), raw)
            self.assertEqual(result["events"], {"detection": 1, "heartbeat": 1})
            self.assertEqual((result["first_id"], result["last_id"]), ("7", "8"))
            for args in call.call_args_list:
                self.assertEqual(args.args[0].get_header("Authorization"), "Bearer secret")
            self.assertEqual(call.call_args_list[1].args[0].full_url, "https://example.test/v1/stream?from=7")
            self.assertNotIn("secret", (Path(directory) / "summary.json").read_text())

    def test_existing_capture_is_not_overwritten(self):
        with tempfile.TemporaryDirectory() as directory:
            destination = Path(directory)
            (destination / "session.json").write_text("keep")
            with patch.object(sample.urllib.request, "urlopen", return_value=Response(b'{}')):
                with self.assertRaises(FileExistsError):
                    sample.capture("https://example.test", "secret", destination, 1, 0)
            self.assertEqual((destination / "session.json").read_text(), "keep")

    def test_wrong_content_type_is_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            with patch.object(sample.urllib.request, "urlopen", side_effect=[Response(b'{}'), Response(b'error')]):
                with self.assertRaises(ValueError):
                    sample.capture("https://example.test", "secret", Path(directory), 1, 0)


if __name__ == "__main__":
    unittest.main()
