#!/usr/bin/env python3
"""Run isolated Sensor Pantai adapter/resilience tests with Go's race detector.

Faults use local HTTP fixtures and controlled clocks; this is not a P5 game day.
"""
import argparse
import datetime
import json
import secrets
import subprocess
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", type=Path, help="New evidence directory")
    args = parser.parse_args()
    stamp = datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    folder = (args.output_dir or ROOT / "artifacts/m2/runs" / (stamp + "-pantai-resilience-" + secrets.token_hex(3))).resolve()
    folder.mkdir(parents=True, exist_ok=False)
    name = "tubesaat-pantai-resilience-" + secrets.token_hex(5)
    command = ["docker", "run", "--rm", "--name", name, "--network", "none", "--mount",
               "type=bind,source=" + str(ROOT / "services/aggregator") + ",target=/src,readonly",
               "--workdir", "/src", "golang:1.27.1", "go", "test", "-json", "-race", "-count=1",
               "-run", "^Test(HTTP|Config|Observation|BadObservation|Envelope|Station|Resilience)",
               "./internal/source/pantai"]
    evidence = {"scope": "ADAPTER_AND_RESILIENCE_FAULT_TESTS", "p5_acceptance": False,
                "tested_source": "WORKSPACE", "command": command, "result": "RUNNING"}
    try:
        evidence["source_commit"] = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip()
        print("+ " + " ".join(command), flush=True)
        result = subprocess.run(command, cwd=ROOT, capture_output=True, text=True, encoding="utf-8", errors="replace", timeout=300)
        (folder / "tests.jsonl").write_text(result.stdout + result.stderr, encoding="utf-8")
        passed, skipped = [], []
        for line in result.stdout.splitlines():
            try:
                event = json.loads(line)
            except ValueError:
                continue
            if event.get("Action") == "pass" and event.get("Test"):
                passed.append(event["Test"])
            if event.get("Action") == "skip":
                skipped.append(event.get("Test", event.get("Package")))
        required = {"TestResilienceOpensRejectsAndRecoversWithSingleProbe",
                    "TestResilienceBulkheadAndCanceledHalfOpen",
                    "TestResilienceRetryBudgetSharedAcrossEndpointsAndRefills",
                    "TestResilienceHonorsLongRetryAfterWithoutWaiting",
                    "TestResilienceDoesNotRetryPermanentOrDataFailures",
                    "TestResilienceRealHTTPOutageDoesNotBlockOtherSource"}
        evidence.update(exit_code=result.returncode, passed_tests=passed, skipped_tests=skipped,
                        result="PASS" if result.returncode == 0 and required <= set(passed) and not skipped else "FAIL")
    except (OSError, subprocess.SubprocessError) as error:
        evidence.update(result="FAIL", failure=type(error).__name__)
    finally:
        # --rm normally removes it; force-removal also covers a timed-out test.
        try:
            subprocess.run(["docker", "rm", "--force", name], capture_output=True, timeout=30)
            inspect = subprocess.run(["docker", "container", "ls", "--all", "--quiet", "--filter", "name=^/" + name + "$"],
                                     capture_output=True, text=True, timeout=30)
            evidence["cleanup"] = "PASS" if inspect.returncode == 0 and not inspect.stdout.strip() else "FAIL"
        except (OSError, subprocess.SubprocessError):
            evidence["cleanup"] = "FAIL"
        if evidence["cleanup"] != "PASS":
            evidence["result"] = "FAIL"
        evidence["finished_at"] = datetime.datetime.now(datetime.timezone.utc).isoformat()
        (folder / "pantai-resilience.json").write_text(json.dumps(evidence, indent=2) + "\n", encoding="utf-8")
    print(evidence["result"] + ": " + str(folder / "pantai-resilience.json"))
    return 0 if evidence["result"] == "PASS" else 1


if __name__ == "__main__":
    raise SystemExit(main())
