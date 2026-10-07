"""Run with: python3 -B -m unittest discover -s scripts -p test_demo_support.py."""
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import unittest
from unittest.mock import patch


class DemoConfigurationTest(unittest.TestCase):
    def test_effective_ports_shared_flags_and_redacted_failure(self):
        services = {name: {"ports": [{"published": str(port)}], "environment": {}}
                    for name, port in (("bmkg", 18081), ("pvmbg", 18082), ("aggregator", 18083),
                                       ("auth", 18084), ("client-api", 18080), ("message-broker", 25672))}
        services["auth"]["environment"] = {"AUTH_PORT": "8084", "JWT_SIGNING_SECRET": "private-signing-value",
                                            "ACCESS_TOKEN_TTL_SECONDS": "60"}
        result = subprocess.CompletedProcess([], 0, json.dumps({"services": services}), "")
        spec = importlib.util.spec_from_file_location("demo_under_test", Path(__file__).with_name("demo_support.py"))
        module = importlib.util.module_from_spec(spec)
        with patch.object(sys, "argv", ["checker", "--env-file", "local.env", "--project-name", "isolated", "--evidence-dir", "new-evidence", "--output", "evidence.json"]), patch("subprocess.run", return_value=result):
            spec.loader.exec_module(module)
            self.assertEqual(sys.argv[1:], ["--output", "evidence.json"])
        self.assertEqual(module.settings["AUTH_PORT"], "18084")
        self.assertEqual(module.options.evidence_dir, "new-evidence")
        self.assertIn("isolated", module.compose)
        self.assertEqual(module.compose[-4:], ["-f", "docker-compose.yml", "-f", "tests/compose-operator.yml"])
        self.assertEqual(module.redact("60 private-signing-value"), "60 [REDACTED]")
        failure = subprocess.CompletedProcess([], 1, "private-signing-value", "private-signing-value")
        with patch("subprocess.run", return_value=failure), self.assertRaises(subprocess.CalledProcessError) as caught:
            module.run("up")
        self.assertEqual(caught.exception.output, "[REDACTED]")
        self.assertEqual(caught.exception.stderr, "[REDACTED]")
        module.settings.update(FIELD_TEAM_CLIENT_ID="field-team", FIELD_TEAM_CLIENT_PASSWORD="test-password")
        initial = {"access_token": "old-access", "refresh_token": "old-refresh", "expires_in": 60}
        rotated = {"access_token": "new-access", "refresh_token": "new-refresh", "expires_in": 60}
        with patch.object(module, "http_call", side_effect=[{"status": 200, "body": initial}]) as call, patch.object(module.time, "monotonic", return_value=100):
            self.assertEqual(module.login("FIELD_TEAM"), "old-access")
            self.assertEqual(call.call_args.args[1], "/login")
        with patch.object(module, "http_call", side_effect=[{"status": 200, "body": rotated}, {"status": 200}]) as call, patch.object(module.time, "monotonic", return_value=160):
            self.assertEqual(module.client_read("/hazards")["status"], 200)
            self.assertEqual(call.call_args_list[0].args[2], {"refresh_token": "old-refresh"})
            self.assertEqual(call.call_args_list[1].kwargs["token"], "new-access")
