#!/usr/bin/env python3
"""P2 through Auth/Client API: independent sessions, sustained slow/outage runs, exact PVMBG restore."""
import json
import os
from pathlib import Path
import platform
import subprocess
import tempfile
import time

from demo_support import (options, compose, settings, run, now, states, logs, wait_for,
                          http_call, client_read, redact)

folder = Path(options.evidence_dir or "docs/evidence/anggota-c/integrasi/client-p2").resolve()
folder.mkdir(parents=True, exist_ok=True)
base = "http://127.0.0.1:" + settings["CLIENT_API_PORT"]
auth = "http://127.0.0.1:" + settings["AUTH_PORT"]


def admin(path, body):
    response = http_call(settings["PVMBG_PORT"], "/admin/" + path, body, settings["PVMBG_TOKEN"])
    assert response["status"] == 200
    return response["body"]


def mock_state():
    response = http_call(settings["PVMBG_PORT"], "/health")
    assert response["status"] == 200
    return {"delay_ms": response["body"]["simulated_delay_ms"],
            "schema_version": response["body"]["schema_version"], "outage": response["body"]["simulated_outage"]}


def volcanic():
    response = client_read("/hazards?source=PVMBG&hazard_type=VOLCANIC&limit=1000")
    assert response["status"] == 200 and response["body"]["data"]
    return response


def source():
    return volcanic()["body"]["sources"]["PVMBG"]


def load_run(name):
    log_path = folder / (name + ".log")
    summary_path = folder / (name + "-summary.json")
    child_env = dict(os.environ, BASE_URL=base, AUTH_URL=auth, SUMMARY_PATH=str(summary_path), RUN_ID=name,
                     FIELD_TEAM_CLIENT_ID=settings["FIELD_TEAM_CLIENT_ID"],
                     FIELD_TEAM_CLIENT_PASSWORD=settings["FIELD_TEAM_CLIENT_PASSWORD"])
    record = {"started_at": now(), "vus": 50, "duration_seconds": 60, "connection_samples": []}
    evidence["runs"][name] = record
    summary_path.unlink(missing_ok=True)
    with log_path.open("w") as output:
        process = subprocess.Popen(["k6", "run", "--quiet", "tests/load-client-p2.js"],
                                   env=child_env, stdout=output, stderr=subprocess.STDOUT)
        try:
            deadline = time.monotonic() + 125
            while "P2_SESSIONS_READY" not in log_path.read_text():
                assert process.poll() is None, "k6 session preparation failed; inspect the sanitized run log"
                assert time.monotonic() < deadline, "k6 session preparation timed out"
                time.sleep(0.25)
            measured = time.monotonic()
            record["measurement_started_at"] = now()
            for offset in (15, 30, 45):
                time.sleep(max(0, measured + offset - time.monotonic()))
                result = subprocess.run(["lsof", "-nP", "-a", "-p", str(process.pid),
                    "-iTCP", "-sTCP:ESTABLISHED", "-Fn"], capture_output=True, text=True, timeout=5)
                connections = sorted(set(line[1:] for line in result.stdout.splitlines()
                    if line.startswith("n") and line.endswith("->127.0.0.1:" + settings["CLIENT_API_PORT"])))
                record["connection_samples"].append({"at": now(), "elapsed_seconds": time.monotonic() - measured,
                    "established_count": len(connections), "connections": connections})
                probe = volcanic()
                record.setdefault("volcanic_probes", []).append({"at": now(), "status": probe["status"],
                    "count": probe["body"]["count"], "source": probe["body"]["sources"]["PVMBG"]})
            process.wait(timeout=35)
            record["measurement_wall_seconds"] = time.monotonic() - measured
            record["exit_code"] = process.returncode
        finally:
            if process.poll() is None:
                process.terminate()
                process.wait(timeout=10)
            record["exit_code"] = process.returncode
            log_path.write_text(redact(log_path.read_text()))
    summary = json.loads(summary_path.read_text())
    record["k6_duration_ms"] = summary["state"]["testRunDurationMs"]
    record["metrics"] = {key: metric["values"] for key, metric in summary["metrics"].items()
                         if key in ("query_requests", "responses_200", "responses_429", "throttled_queries",
                                    "bmkg_elapsed_ms", "bmkg_served_elapsed_ms", "uncontrolled_errors",
                                    "refresh_successes", "refresh_failures")}
    record["thresholds"] = {key: metric["thresholds"] for key, metric in summary["metrics"].items() if "thresholds" in metric}
    record["observed_query_throughput_per_second"] = record["metrics"]["query_requests"]["count"] / record["measurement_wall_seconds"]
    record["served_query_throughput_per_second"] = record["metrics"]["responses_200"]["count"] / record["measurement_wall_seconds"]
    record["upstream_calls"] = [r for r in logs("aggregator", record["measurement_started_at"])
                                if r.get("target") in ("bmkg", "pvmbg")]
    record["pass"] = process.returncode == 0 and record["k6_duration_ms"] >= 60000 and all(
        s["established_count"] >= 50 for s in record["connection_samples"])
    record["finished_at"] = now()
    evidence["runs"][name] = record
    (folder / "p2-client-check.json").write_text(redact(json.dumps(evidence, indent=2)) + "\n")
    print(name + ": " + ("PASS" if record["pass"] else "FAIL"), flush=True)
    return record


evidence = {"started_at": now(), "scope": "AUTHENTICATED_CLIENT_API", "runs": {},
            "base_commit": subprocess.check_output(["git", "rev-parse", "HEAD"], text=True).strip(),
            "tested_source": "WORKSPACE", "k6": subprocess.check_output(["k6", "version"], text=True).strip(),
            "host": {"platform": platform.platform(), "cpu_logical": os.cpu_count()}, "states_before": states()}
try:
    status = [json.loads(line) for line in run("ps", "--format", "json").splitlines()]
    assert len(status) == 9 and all(s["State"] == "running" and s["Health"] == "healthy" for s in status), "nine services must be healthy"
    assert int(settings["ACCESS_TOKEN_TTL_SECONDS"]) == 60, "P2 must retain access TTL 60s"
    original = mock_state()
except Exception as error:
    evidence.update(result="FAIL", failure=redact(str(error)), cleanup="NOT_NEEDED_NO_MUTATION", finished_at=now())
    (folder / "p2-client-check.json").write_text(redact(json.dumps(evidence, indent=2)) + "\n")
    raise
evidence["original_pvmbg"] = original
slow = compose + ["-f", "tests/compose-load.yml"]
try:
    result = subprocess.run(slow + ["up", "-d", "--no-deps", "--wait", "pvmbg"], capture_output=True, text=True)
    assert result.returncode == 0, "slow PVMBG recreation failed"
    admin("schema-version", {"version": original["schema_version"]})
    admin("outage", {"enabled": False})
    wait_for(lambda: source()["available"] and not source()["stale"], "slow PVMBG must finish ingestion")
    slow_result = load_run("slow")
    evidence["slow_upstream_observed"] = any(r.get("target") == "pvmbg" and r.get("status") == 200
        and r.get("latency_ms", 0) >= 3000 for r in slow_result["upstream_calls"])
    evidence["outage_enabled_at"] = now()
    admin("outage", {"enabled": True})
    wait_for(lambda: not source()["available"] and source()["stale"] and source()["stale_since"],
             "failed PVMBG ingestion must become stale")
    baseline = volcanic()
    evidence["outage_baseline"] = baseline
    bmkg_before = baseline["body"]["sources"]["BMKG"]["last_ingested_at"]
    outage_result = load_run("outage")
    after = volcanic()
    evidence["after_outage_load"] = after
    assert after["body"]["data"] == baseline["body"]["data"], "last-known volcanic snapshots must remain readable"
    status = after["body"]["sources"]["PVMBG"]
    assert not status["available"] and status["stale"] and status["stale_since"]
    assert status["last_ingested_at"] == baseline["body"]["sources"]["PVMBG"]["last_ingested_at"]
    assert after["body"]["sources"]["BMKG"]["last_ingested_at"] > bmkg_before
    assert all(p["source"]["stale"] and not p["source"]["available"] for p in outage_result["volcanic_probes"])
    assert any(r.get("target") == "pvmbg" and r.get("status") == 503 for r in outage_result["upstream_calls"])
    admin("outage", {"enabled": False})
    report_id = admin("schema-version", {"version": 2})["report_id"]
    evidence["recovery_report_id"] = report_id
    wait_for(lambda: source()["available"] and not source()["stale"] and any(
        r["source_ref_id"] == report_id for r in volcanic()["body"]["data"]), "new report must persist after recovery")
    evidence["after_recovery"] = volcanic()
    assert source()["last_ingested_at"] > status["last_ingested_at"]
    assert slow_result["pass"] and outage_result["pass"], "P2 thresholds/socket proof failed; inspect both run summaries"
    assert evidence["slow_upstream_observed"], "no actual 3-second PVMBG fetch observed during load"
    evidence["result"] = "PASS"
except Exception as error:
    evidence["result"] = "FAIL"
    evidence["failure"] = redact(str(error))
    raise
finally:
    try:
        # Preserve the effective runtime delay even when the caller used another overlay.
        with tempfile.TemporaryDirectory(prefix="tubesaat-p2-restore-") as temporary:
            override = Path(temporary) / "restore.json"
            override.write_text(json.dumps({"services": {"pvmbg": {"environment": {"PVMBG_DELAY_MS": str(original["delay_ms"])}}}}))
            result = subprocess.run(compose + ["-f", str(override), "up", "-d", "--no-deps", "--wait", "pvmbg"],
                                    capture_output=True, text=True)
            assert result.returncode == 0, "PVMBG delay restore failed"
        admin("schema-version", {"version": original["schema_version"]})
        admin("outage", {"enabled": original["outage"]})
        assert mock_state() == original, "restore must retain original delay/schema/outage"
        evidence["restored_pvmbg"] = mock_state()
        evidence["states_after"] = states()
        assert all(evidence["states_after"][name] == state for name, state in evidence["states_before"].items() if name != "pvmbg")
        evidence["cleanup"] = "PASS"
    except Exception as error:
        evidence["result"] = "FAIL"
        evidence["cleanup"] = "FAIL"
        evidence["restoration_failure"] = redact(str(error))
        raise
    finally:
        evidence["finished_at"] = now()
        (folder / "p2-client-check.json").write_text(redact(json.dumps(evidence, indent=2)) + "\n")
print("PASS: authenticated 50-VU/60s slow and outage runs; per-VU refresh; last-known data; recovery; exact runtime restore")
print(folder / "p2-client-check.json")
