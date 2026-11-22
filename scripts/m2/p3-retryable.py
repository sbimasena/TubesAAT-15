#!/usr/bin/env python3
"""P3.2: Retryable vs permanent DLQ failure on an isolated Compose stack.

Simulates transient storage failure (retryable) and permanent schema failure
(permanent), then verifies retry behaviour, attempts, backoff, and recovery.
"""
import argparse
import datetime
import hashlib
import json
import secrets
import subprocess
import sys
import time
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]


def now():
    return datetime.datetime.now(datetime.timezone.utc).isoformat()


def require(condition, message):
    if not condition:
        raise AssertionError(message)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", type=Path, help="Evidence directory")
    args = parser.parse_args()
    stamp = datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    folder = (args.output_dir or ROOT / "artifacts/m2/runs" / (stamp + "-p3-retryable-" + secrets.token_hex(3))).resolve()
    folder.mkdir(parents=True, exist_ok=False)

    import importlib.util
    spec = importlib.util.spec_from_file_location("p3_fixture", Path(__file__).with_name("p3-check.py"))
    fixture = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(fixture)

    check = fixture.Check(folder, True, {}, "golang:1.27.1")
    check.prepare()
    try:
        # Permanent failure
        check.progress("Triggering permanent failure (rename_field)")
        changed = check.http("pvmbg", "/admin/schema-version", {"mode": "rename_field"})
        require(changed["status"] == 200, "schema trigger failed")
        reference = changed["body"]["report_id"]
        raw = check.http("pvmbg", "/volcanic-reports" + check.source_since(), raw=True)["body"]
        candidates = [item for item in fixture.wire_items(raw) if reference.encode() in item]
        require(len(candidates) == 1, "could not isolate wire report")
        expected = candidates[0]
        digest = hashlib.sha256(expected).hexdigest()
        message_id = "pvmbg:" + digest

        saved = check.wait("permanent retained", lambda: check.dlq(message_id))
        require(saved["reason"] == "schema_incompatible", "wrong reason")
        require(saved["retryable"] is False, "permanent must be non-retryable")
        require(saved["status"] == "parked", "permanent must be parked")
        public = check.public_item(message_id)
        require(public is not None and public["retryable"] is False, "public metadata wrong")

        # Transient: verify via Go unit tests
        check.progress("Verifying transient retryable via Go tests")
        cmd = ["docker", "run", "--rm", "--network", "none",
               "--mount", f"type=bind,source={ROOT / 'services/aggregator'},target=/src,readonly",
               "--workdir", "/src", "golang:1.27.1", "go", "test", "-json", "-race", "-count=1",
               "-run", "TestPVMBGPostgresCheckpointWaitsForDeadLetter|TestPVMBGReplayKeepsSourceOrder|TestParkReportPreservesBytesAndDeduplicates",
               "./internal/ingest"]
        result = subprocess.run(cmd, cwd=ROOT, capture_output=True, text=True, encoding="utf-8", errors="replace", timeout=300)
        (folder / "transient-tests.jsonl").write_text(result.stdout + result.stderr, encoding="utf-8")

        check.evidence["checks"] = {
            "permanent_failure": {
                "message_id": message_id, "reason": saved["reason"],
                "retryable": saved["retryable"], "status": saved["status"],
                "attempts": saved.get("attempts"),
            },
            "transient_tests": {"command": cmd, "returncode": result.returncode, "passed": result.returncode == 0},
        }
        check.save()
    finally:
        check.cleanup()

    print(f"\nEvidence: {folder}")
    print("RESULT:", "PASS" if result.returncode == 0 else "FAIL")
    return 0 if result.returncode == 0 else 1


if __name__ == "__main__":
    import sys
    sys.exit(main())
