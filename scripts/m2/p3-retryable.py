#!/usr/bin/env python3
"""P3.2: Retryable vs permanent DLQ failure on an isolated Compose stack.

Pauses the Aggregator, stages one valid PVMBG report, installs a CHECK
constraint that rejects only that report's source_ref_id, unpauses, and verifies
that the transient storage failure is queued as retrying with backoff. After the
constraint is removed, the same report must recover exactly once. A second
incompatible report is parked permanently and the source must keep progressing.
"""
import argparse
import datetime
import importlib.util
import json
import secrets
import subprocess
import sys
import tempfile
import time
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
CONSTRAINT = "p3_retryable_reject_report"
EXIT_CODES = {"PASS": 0, "FAIL": 1, "BLOCKED": 3}


def load_check_module():
    spec = importlib.util.spec_from_file_location("p3_check_module", ROOT / "scripts/m2/p3-check.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def docker_available():
    try:
        result = subprocess.run(["docker", "info", "--format", "{{.ServerVersion}}"],
                                capture_output=True, text=True, timeout=30)
        return result.returncode == 0
    except (OSError, subprocess.TimeoutExpired):
        return False


def rows(checker, module, where):
    query = ("SELECT coalesce(json_agg(row_to_json(t)), '[]'::json) FROM ("
             "SELECT message_id, source_ref_id, reason, status, attempts, next_retry_at, "
             "encode(sha256(raw),'hex') AS sha256, octet_length(raw) AS bytes, received_at "
             "FROM unprocessed_messages WHERE " + where + " ORDER BY received_at) t")
    value = checker.sql(query)
    return json.loads(value) if value else []


def find_report(checker, module, report_id):
    matches = rows(checker, module, "source='PVMBG' AND source_ref_id=" + module.literal(report_id))
    return matches[0] if matches else None


def canonical_count(checker, module, report_id):
    return int(checker.sql("SELECT count(*) FROM hazard_events WHERE source='PVMBG' AND source_ref_id="
                           + module.literal(report_id)))


def cursor_value(checker):
    return checker.sql("SELECT cursor_value FROM ingest_cursor WHERE source='PVMBG'")


def stable_cursor(checker, seconds=15):
    deadline = time.monotonic() + seconds
    previous = cursor_value(checker)
    stable_since = time.monotonic()
    while time.monotonic() < deadline:
        time.sleep(1)
        current = cursor_value(checker)
        if current != previous:
            previous = current
            stable_since = time.monotonic()
        elif time.monotonic() - stable_since >= 2:
            return current
    return previous


def wait_for(checker, label, probe, seconds=90):
    deadline = time.monotonic() + seconds
    last = None
    while time.monotonic() < deadline:
        try:
            value = probe()
            if value:
                checker.progress("PASS: " + label)
                return value
        except Exception as error:  # noqa: BLE001 - probe failures are retried
            last = type(error).__name__ + ": " + str(error)
        time.sleep(0.5)
    raise AssertionError(label + (" (last: " + last + ")" if last else " (timeout)"))


def parse_timestamp(value):
    if not value:
        return None
    text = str(value).strip()
    if text.endswith("Z"):
        text = text[:-1] + "+00:00"
    try:
        stamp = datetime.datetime.fromisoformat(text)
    except ValueError:
        return None
    if stamp.tzinfo is None:
        stamp = stamp.replace(tzinfo=datetime.timezone.utc)
    return stamp.astimezone(datetime.timezone.utc)


def safe_cleanup(checker):
    if getattr(checker, "compose_args", None) is None:
        return
    try:
        checker.compose("unpause", "aggregator", check=False)
    except Exception:
        pass
    try:
        checker.sql("ALTER TABLE hazard_events DROP CONSTRAINT IF EXISTS " + CONSTRAINT)
    except Exception:
        pass


def transient_case(checker, module):
    case = {"name": "transient_store_failure"}
    checker.evidence["checks"]["transient"] = case
    baseline_cursor = stable_cursor(checker)
    case["cursor_before"] = baseline_cursor

    checker.progress("Pausing aggregator to stage one valid PVMBG report")
    checker.compose("pause", "aggregator")
    try:
        response = checker.http("pvmbg", "/admin/schema-version", {"version": 2})
        module.require(response["status"] == 200, "valid report trigger failed")
        report_id = response["body"]["report_id"]
        module.require(report_id, "mock did not return a report_id")
        case["report_id"] = report_id
        checker.sql("ALTER TABLE hazard_events DROP CONSTRAINT IF EXISTS " + CONSTRAINT)
        checker.sql("ALTER TABLE hazard_events ADD CONSTRAINT " + CONSTRAINT +
                    " CHECK (source_ref_id <> " + module.literal(report_id) + ") NOT VALID")
        case["constraint"] = CONSTRAINT
    finally:
        checker.compose("unpause", "aggregator")

    first = wait_for(checker, "transient failure is queued for retry",
                     lambda: (lambda row: row if row and row["status"] == "retrying" else None)(
                         find_report(checker, module, report_id)), seconds=90)
    case["first_attempt"] = first
    module.require(first["reason"] == "store_unavailable",
                   "transient failure was not classified as store_unavailable")
    module.require(first["attempts"] >= 1, "attempts were not recorded")
    next_retry = parse_timestamp(first["next_retry_at"])
    module.require(next_retry is not None and next_retry > datetime.datetime.now(datetime.timezone.utc),
                   "no future backoff was scheduled")
    module.require(canonical_count(checker, module, report_id) == 0,
                   "transient failure reached canonical storage")
    module.require(cursor_value(checker) == baseline_cursor,
                   "checkpoint advanced before the DLQ write succeeded")

    retried = wait_for(checker, "retry increments attempts with backoff",
                       lambda: (lambda row: row if row and row["status"] == "retrying"
                                                and row["attempts"] >= first["attempts"] + 1 else None)(
                           find_report(checker, module, report_id)), seconds=90)
    case["retry_attempt"] = retried

    safe_cleanup(checker)
    recovered = wait_for(checker, "transient message recovers after the fault is removed",
                         lambda: (lambda row: row if row and row["status"] == "recovered" else None)(
                             find_report(checker, module, report_id)), seconds=120)
    case["recovered"] = recovered
    module.require(canonical_count(checker, module, report_id) == 1,
                   "recovery did not write exactly one canonical event")
    module.require(cursor_value(checker) != baseline_cursor,
                   "checkpoint did not advance after successful recovery")

    message_id = recovered["message_id"]
    response = checker.http("client", "/internal/audit/unprocessed/replay",
                            {"message_ids": [message_id]}, role="internal-ops")
    module.require(response["status"] == 202, "replay after recovery was not accepted")
    time.sleep(3)
    module.require(canonical_count(checker, module, report_id) == 1,
                   "replay after recovery duplicated a canonical event")
    case["replay_after_recovery"] = {"status": response["status"], "canonical_count": 1}
    case["result"] = "PASS"
    checker.save()


def permanent_case(checker, module):
    case = {"name": "permanent_schema_failure"}
    checker.evidence["checks"]["permanent"] = case
    response = checker.http("pvmbg", "/admin/schema-version", {"mode": "rename_field"})
    module.require(response["status"] == 200, "schema mode trigger failed")
    report_id = response["body"]["report_id"]
    module.require(report_id, "mock did not return a report_id")
    case["report_id"] = report_id

    try:
        parked = wait_for(checker, "permanent failure is parked",
                          lambda: (lambda row: row if row and row["status"] == "parked" else None)(
                              find_report(checker, module, report_id)), seconds=90)
        case["parked"] = parked
        module.require(parked["reason"] == "schema_incompatible",
                       "permanent failure was not classified as schema_incompatible")
        module.require(parked["next_retry_at"] is None, "permanent failure was scheduled for retry")
        module.require(canonical_count(checker, module, report_id) == 0,
                       "permanent failure reached canonical storage")
        public = checker.public_item(parked["message_id"])
        module.require(public is not None and public["retryable"] is False,
                       "permanent failure was exposed as retryable")
        case["public_metadata"] = public
    finally:
        checker.http("pvmbg", "/admin/schema-version", {"version": 1})

    sibling_response = checker.http("pvmbg", "/admin/schema-version", {"version": 2})
    module.require(sibling_response["status"] == 200, "valid sibling trigger failed")
    sibling_id = sibling_response["body"]["report_id"]
    module.require(sibling_id, "mock did not return a sibling report_id")
    case["sibling_report_id"] = sibling_id
    wait_for(checker, "source resumes after the permanent failure",
             lambda: canonical_count(checker, module, sibling_id) == 1, seconds=120)
    case["sibling_canonical"] = 1
    case["result"] = "PASS"
    checker.save()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", type=Path, help="Evidence directory")
    parser.add_argument("--go-image", default="golang:1.27.1", help="Go integration-test image")
    args = parser.parse_args()
    if any(character.isspace() for character in args.go_image):
        parser.error("--go-image must be one image reference without whitespace")

    stamp = datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    folder = (args.output_dir or ROOT / "artifacts/m2/runs" /
              (stamp + "-p3-retryable-" + secrets.token_hex(3))).resolve()
    folder.mkdir(parents=True, exist_ok=False)

    module = load_check_module()
    evidence = {"started_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
                "result": "RUNNING", "scope": "ISOLATED_COMPOSE_SINGLE_NODE",
                "physical_cluster_acceptance": False, "checks": {}}

    if not docker_available():
        evidence.update(result="BLOCKED", reasons=["Docker engine is not available"])
        (folder / "p3-retryable.json").write_text(json.dumps(evidence, indent=2) + "\n", encoding="utf-8")
        print("Evidence:", folder)
        print("RESULT: BLOCKED; Docker engine is not available")
        return EXIT_CODES["BLOCKED"]

    with tempfile.TemporaryDirectory(prefix="tubesaat-p3-retryable-") as temporary:
        checker = module.Check(folder, Path(temporary), 1, args.go_image)
        checker.evidence.update(evidence)
        checker.evidence.setdefault("checks", {})
        started = False
        try:
            checker.start()
            started = True
            checker.wait("live canonical reads are ready",
                         lambda: checker.http("client", "/hazards?source=PVMBG&limit=1", role="field-team")["status"] == 200)
            transient_case(checker, module)
            permanent_case(checker, module)
            checker.evidence["result"] = "PASS"
        except Exception as error:
            checker.evidence.update(result="FAIL", failure=checker.redact(str(error)))
            if getattr(checker, "compose_args", None):
                checker.compose("logs", "--no-log-prefix", "--tail", "200", log="diagnostics.log", check=False)
        finally:
            safe_cleanup(checker)
            if getattr(checker, "compose_args", None):
                try:
                    checker.stop()
                except Exception as error:
                    checker.evidence.setdefault("cleanup_failure", checker.redact(str(error)))
                    if checker.evidence.get("result") == "PASS":
                        checker.evidence["result"] = "FAIL"
        (folder / "p3-retryable.json").write_text(
            checker.redact(json.dumps(checker.evidence, indent=2)) + "\n", encoding="utf-8")
        checker.save()

    result = checker.evidence.get("result", "FAIL")
    print("\nEvidence:", folder)
    print("RESULT:", result)
    for name, case in checker.evidence.get("checks", {}).items():
        print(" ", name, "->", case.get("result"))
    return EXIT_CODES.get(result, 1)


if __name__ == "__main__":
    raise SystemExit(main())
