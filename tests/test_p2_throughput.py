import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[1]


class ThroughputRunnerTests(unittest.TestCase):
    def run_throughput(self, target, three_rate=250, failed_run=False):
        with tempfile.TemporaryDirectory() as directory:
            checkout = Path(directory)
            for path in ("scripts/k8s/lib.sh", "scripts/m2/p2-load.sh", "tests/k6/m2-aggregator.js"):
                destination = checkout / path
                destination.parent.mkdir(parents=True, exist_ok=True)
                shutil.copyfile(ROOT / path, destination)
            kubectl = checkout / "kubectl"
            kubectl.write_text(
                '#!/usr/bin/env bash\n'
                'case "$*" in\n'
                '  "config current-context") echo fixture ;;\n'
                '  *"get deployment aggregator"*) echo 4 ;;\n'
                '  *"scale deployment aggregator"*) echo "${*: -1}" >> "$FIXTURE_SCALES" ;;\n'
                '  *"wait deployment/aggregator"*) ;;\n'
                '  *"rollout status deployment/aggregator"*) ;;\n'
                '  *) echo "unexpected kubectl command: $*" >&2; exit 1 ;;\n'
                'esac\n'
            )
            k6 = checkout / "k6"
            k6.write_text(
                '#!/usr/bin/env bash\n'
                '[[ "$1" == run && "$2" == --summary-export && "$TARGET_URL" == "$FIXTURE_TARGET" ]] || exit 20\n'
                'case "$3" in\n'
                '  *-1.json) rate=100 ;;\n'
                '  *-3.json) [[ "$FIXTURE_FAIL" == 0 ]] || exit 21; rate="$FIXTURE_THREE_RATE" ;;\n'
                '  *) exit 22 ;;\n'
                'esac\n'
                'printf \'{"metrics":{"responses_200":{"rate":%s}}}\\n\' "$rate" > "$3"\n'
            )
            for executable in (kubectl, k6):
                executable.chmod(0o700)
            scales = checkout / "scales"
            env = dict(os.environ, PATH=f"{checkout}:{os.environ['PATH']}", YES="1", TARGET_URL=target,
                       FIXTURE_TARGET=target, FIXTURE_SCALES=str(scales), FIXTURE_THREE_RATE=str(three_rate),
                       FIXTURE_FAIL=str(int(failed_run)))
            result = subprocess.run(
                ["bash", str(checkout / "scripts/m2/p2-load.sh"), "throughput"],
                env=env, text=True, capture_output=True, timeout=10,
            )
            reports = [json.loads(path.read_text()) for path in checkout.glob("artifacts/m2/runs/*.json")]
            return result, scales.read_text().splitlines() if scales.exists() else [], reports

    def test_requires_target_before_changing_replicas(self):
        result, scales, _ = self.run_throughput("")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Set TARGET_URL", result.stderr)
        self.assertEqual(scales, [])

    def test_reports_successful_throughput_and_restores_replicas(self):
        result, scales, reports = self.run_throughput("http://fixture:8083")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("ratio 2.500×", result.stdout)
        self.assertEqual(scales, ["--replicas=1", "--replicas=3", "--replicas=4"])
        self.assertEqual(len(reports), 2)

    def test_fails_below_target_and_restores_replicas(self):
        result, scales, _ = self.run_throughput("http://fixture:8083", three_rate=199)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("ratio 1.990×", result.stdout)
        self.assertEqual(scales[-1], "--replicas=4")

    def test_fails_when_k6_cannot_complete_and_restores_replicas(self):
        result, scales, _ = self.run_throughput("http://fixture:8083", failed_run=True)
        self.assertEqual(result.returncode, 21, result.stderr)
        self.assertEqual(scales[-1], "--replicas=4")
