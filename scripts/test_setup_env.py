import importlib.util
from pathlib import Path
import stat
import tempfile
import unittest


class SetupEnvironmentTest(unittest.TestCase):
    def test_private_consistent_credentials_and_no_overwrite(self):
        script = Path(__file__).with_name("setup-env.py")
        spec = importlib.util.spec_from_file_location("setup_env", script)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / ".env"
            self.assertTrue(module.create_environment(script.parent.parent / ".env.example", target))
            original = target.read_text()
            values = dict(line.split("=", 1) for line in original.splitlines()
                          if line and not line.startswith("#") and "=" in line)
            self.assertGreaterEqual(len(values["JWT_SIGNING_SECRET"]), 32)
            self.assertNotEqual(values["JWT_SIGNING_SECRET"], values["AUTH_INTERNAL_SECRET"])
            self.assertNotEqual(values["BMKG_API_KEY"], values["PVMBG_TOKEN"])
            for role in ("MEDIA", "FIELD_TEAM", "INTERNAL_OPS"):
                self.assertTrue(values[role + "_CLIENT_PASSWORD"])
            self.assertIn(":" + values["POSTGRES_PASSWORD"] + "@", values["DATABASE_URL"])
            self.assertIn(":" + values["RABBITMQ_DEFAULT_PASS"] + "@", values["BROKER_URL"])
            self.assertEqual(stat.S_IMODE(target.stat().st_mode), 0o600)
            self.assertFalse(module.create_environment(script.parent.parent / ".env.example", target))
            self.assertEqual(target.read_text(), original)
