import importlib.util
import io
from email.message import Message
from pathlib import Path
from http.server import BaseHTTPRequestHandler, HTTPServer
from threading import Thread
from urllib.error import HTTPError
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
            with patch.object(sample, "open_feed", side_effect=[Response(b'{"latest_offset":8}'), Response(raw, "text/event-stream")]) as call:
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
            with patch.object(sample, "open_feed", return_value=Response(b'{}')):
                with self.assertRaises(FileExistsError):
                    sample.capture("https://example.test", "secret", destination, 1, 0)
            self.assertEqual((destination / "session.json").read_text(), "keep")

    def test_wrong_content_type_is_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            with patch.object(sample, "open_feed", side_effect=[Response(b'{}'), Response(b'error')]):
                with self.assertRaises(ValueError):
                    sample.capture("https://example.test", "secret", Path(directory), 1, 0)


    def test_sample_blocks_redirect_before_forwarding_authorization(self):
        received = []

        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                received.append(self.path)
                self.send_response(302)
                self.send_header("Location", "/unexpected")
                self.end_headers()

            def log_message(self, *args):
                pass

        with tempfile.TemporaryDirectory() as directory, HTTPServer(("127.0.0.1", 0), Handler) as server:
            thread = Thread(target=server.serve_forever)
            thread.start()
            try:
                with self.assertRaises(HTTPError) as error:
                    sample.capture(f"http://127.0.0.1:{server.server_port}", "secret", Path(directory), 1, 0)
                error.exception.close()
                self.assertEqual(error.exception.code, 302)
            finally:
                server.shutdown()
                thread.join()
        self.assertEqual(received, ["/v1/session"])


if __name__ == "__main__":
    unittest.main()
