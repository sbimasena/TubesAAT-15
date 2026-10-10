import importlib.util
from pathlib import Path
from types import ModuleType
import unittest
from unittest.mock import Mock, patch


class BrokerLifecycleTest(unittest.TestCase):
    def test_restores_only_originally_running_consumers_even_on_failure(self):
        helpers = ModuleType("demo_support")
        for name in ("options", "aggregator_read", "broker", "healthy", "now", "run", "sql",
                     "states", "wait_for", "progress", "heartbeat"):
            setattr(helpers, name, Mock())
        helpers.states.return_value = {"notification-consumer": {"running": True},
                                       "dashboard-consumer": {"running": False}}
        spec = importlib.util.spec_from_file_location("broker_check", Path(__file__).with_name("check-stage-2.py"))
        module = importlib.util.module_from_spec(spec)
        with patch.dict("sys.modules", {"demo_support": helpers}):
            spec.loader.exec_module(module)
        with patch.object(module, "check", side_effect=AssertionError("scenario failed")):
            with self.assertRaisesRegex(AssertionError, "scenario failed"):
                module.main()
        self.assertEqual(helpers.run.call_args_list, [unittest.mock.call("stop", "notification-consumer"),
                                                     unittest.mock.call("start", "notification-consumer")])
        helpers.wait_for.assert_called_once()
        self.assertTrue(helpers.wait_for.call_args.args[0]())
