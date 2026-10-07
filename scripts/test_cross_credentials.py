"""Exercise the credential checker against isolated HTTP trust domains."""
import importlib.util
import json
from pathlib import Path
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

spec = importlib.util.spec_from_file_location("cross_credentials", Path(__file__).with_name("check-cross-credentials.py"))
checker = importlib.util.module_from_spec(spec)
spec.loader.exec_module(checker)


class CredentialIsolationTest(unittest.TestCase):
    def setUp(self):
        self.services = {}
        self.accept_foreign = False
        for name, variable, header, credential in (
            ("bmkg", "BMKG_API_KEY", "X-BMKG-Key", "test-bmkg-key"),
            ("pvmbg", "PVMBG_TOKEN", "Authorization", "test-pvmbg-token"),
        ):
            expected = "Bearer " + credential if name == "pvmbg" else credential
            handler = self.handler(header, expected)
            server = ThreadingHTTPServer(("127.0.0.1", 0), handler)
            thread = threading.Thread(target=server.serve_forever, daemon=True)
            thread.start()
            self.addCleanup(thread.join)
            self.addCleanup(server.server_close)
            self.addCleanup(server.shutdown)
            self.services[name] = {"environment": {variable: credential},
                                   "ports": [{"published": server.server_port}]}

    def handler(self, header, expected):
        owner = self

        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                allowed = self.headers.get(header) == expected or owner.accept_foreign
                self.send_response(200 if allowed else 401)
                self.end_headers()
                self.wfile.write(b"[]" if allowed else b"Unauthorized")

            def log_message(self, *args):
                pass

        return Handler

    def test_both_domains_and_bmkg_endpoints(self):
        checks = checker.check_upstreams(self.services)
        self.assertEqual(len(checks), 12)
        saved = json.dumps(checks)
        self.assertNotIn("test-bmkg-key", saved)
        self.assertNotIn("test-pvmbg-token", saved)

    def test_permissive_server_fails_and_preserves_statuses(self):
        self.accept_foreign = True
        checks = []
        with self.assertRaisesRegex(ValueError, "foreign credential"):
            checker.check_upstreams(self.services, checks)
        self.assertEqual(checks[-1]["status"], 200)
        self.assertEqual(len(checks), 2)

    def test_identical_credentials_fail_before_requests(self):
        self.services["pvmbg"]["environment"]["PVMBG_TOKEN"] = "test-bmkg-key"
        with self.assertRaisesRegex(ValueError, "distinct"):
            checker.check_upstreams(self.services)


if __name__ == "__main__":
    unittest.main()
