"""Verify console route boundaries, credential redaction, and real relay status handling."""
import importlib.util
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
import threading
import unittest
import urllib.error
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("downstream_console", ROOT / "scripts/serve-downstream-ui.py")
console = importlib.util.module_from_spec(spec)
spec.loader.exec_module(console)


class Upstream(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_GET(self):
        self.server.paths.append(self.path)
        self.send_response(302 if self.path == "/redirect" else 503 if self.path == "/failure" else 200)
        self.send_header("Content-Type", "application/json")
        self.send_header("X-Correlation-ID", self.headers.get("X-Correlation-ID", ""))
        self.send_header("Cache-Control", "no-store")
        if self.path == "/redirect":
            self.send_header("Location", "/forbidden-redirect-target")
        self.end_headers()
        self.wfile.write(b'{"status":"fixture"}')


class ConsoleTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.upstream = ThreadingHTTPServer(("127.0.0.1", 0), Upstream)
        cls.upstream.paths = []
        cls.upstream_url = f"http://127.0.0.1:{cls.upstream.server_port}"
        cls.server = ThreadingHTTPServer(("127.0.0.1", 0), console.Handler)
        cls.server.settings = {"env": {"MEDIA_CLIENT_PASSWORD": "private-fixture-password"},
                               "auth": cls.upstream_url, "client": cls.upstream_url}
        cls.server.operation_lock = threading.Lock()
        cls.url = f"http://127.0.0.1:{cls.server.server_port}"
        for server in (cls.upstream, cls.server):
            threading.Thread(target=server.serve_forever, daemon=True).start()

    @classmethod
    def tearDownClass(cls):
        for server in (cls.server, cls.upstream):
            server.shutdown()
            server.server_close()

    def request(self, path, body=None, origin=None):
        headers = {}
        if body is not None:
            headers.update({"Content-Type": "application/json", "X-Tool-Request": "1"})
        if origin:
            headers["Origin"] = origin
        request = urllib.request.Request(self.url + path, json.dumps(body).encode() if body is not None else None, headers)
        try:
            response = urllib.request.urlopen(request, timeout=3)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            return response.status, json.loads(response.read())

    def test_targets_and_route_allowlist_preserve_boundaries(self):
        for target in ("https://example.com", "http://127.0.0.1/path", "http://user:pass@localhost", "http://127.0.0.1/?target=secret"):
            with self.assertRaises(ValueError):
                console.local_origin(target)
        for target, path in (("aggregator", "/hazards"), ("auth", "/internal/v1/introspect"),
                             ("client", "http://example.com/hazards"), ("client", "/../health")):
            status, _ = self.request("/tool/call", {"target": target, "path": path})
            self.assertEqual(status, 400)

    def test_environment_password_is_not_exposed_and_cross_origin_is_rejected(self):
        status, data = self.request("/tool/info")
        self.assertEqual(status, 200)
        self.assertNotIn("private-fixture-password", json.dumps(data))
        self.assertTrue(data["roles"]["media"]["local_password_available"])
        status, _ = self.request("/tool/unit-tests", {}, origin="http://example.com")
        self.assertEqual(status, 403)

    def test_relay_preserves_http_errors_and_correlation_without_following_redirect(self):
        response = console.relay(self.upstream_url, "/failure", correlation="fixture-correlation")
        self.assertEqual(response["status"], 503)
        self.assertEqual(response["headers"]["X-Correlation-ID"], "fixture-correlation")
        response = console.relay(self.upstream_url, "/redirect")
        self.assertEqual(response["status"], 302)
        self.assertNotIn("/forbidden-redirect-target", self.upstream.paths)

    def test_burst_limits_and_evidence_do_not_contain_access_token(self):
        for count in (1, 129, True, "64"):
            with self.assertRaises(ValueError):
                console.burst(self.upstream_url, "private-fixture-token", count)
        result = console.burst(self.upstream_url, "private-fixture-token", 8)
        self.assertEqual(result["status_counts"], {"200": 8})
        self.assertFalse(result["sustained_p2_load_checked"])
        self.assertNotIn("private-fixture-token", json.dumps(result))


if __name__ == "__main__":
    unittest.main()
