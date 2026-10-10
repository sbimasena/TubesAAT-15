#!/usr/bin/env python3
"""P2 through Auth/Client API: independent sessions, sustained slow/outage runs, exact PVMBG restore."""
import json
import os
from pathlib import Path
import platform
import subprocess
import tempfile
import time

from demo_support import (options, compose, config, settings, run, now, states, logs, wait_for,
                          http_call, client_read, redact)
from demo_support import progress

folder = Path(options.evidence_dir).resolve()
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
    progress(f"{name}: preparing 50 independent Field Team sessions; k6 log: {log_path}")
    with log_path.open("w") as output:
        process = subprocess.Popen(["k6", "run", "--quiet", "tests/load-client-p2.js"],
                                   env=child_env, stdout=output, stderr=subprocess.STDOUT)
        try:
            deadline = time.monotonic() + 125
            next_update = time.monotonic() + 10
            while "P2_SESSIONS_READY" not in log_path.read_text():
                assert process.poll() is None, "k6 session preparation failed; inspect the sanitized run log"
                assert time.monotonic() < deadline, "k6 session preparation timed out"
                if time.monotonic() >= next_update:
                    progress(f"{name}: still preparing sessions (125-second timeout)")
                    next_update += 10
                time.sleep(0.25)
            measured = time.monotonic()
            record["measurement_started_at"] = now()
            progress(f"{name}: sessions ready; running 50 VUs for 60 seconds against BMKG-only queries")
            for offset in (15, 30, 45):
                time.sleep(max(0, measured + offset - time.monotonic()))
                progress(f"{name}: {offset}/60s; checking TCP connections and stored volcanic data")
                result = subprocess.run(["lsof", "-nP", "-a", "-p", str(process.pid),
                    "-iTCP", "-sTCP:ESTABLISHED", "-Fn"], capture_output=True, text=True, timeout=5)
                connections = sorted(set(line[1:] for line in result.stdout.splitlines()
                    if line.startswith("n") and line.endswith("->127.0.0.1:" + settings["CLIENT_API_PORT"])))
                record["connection_samples"].append({"at": now(), "elapsed_seconds": time.monotonic() - measured,
                    "established_count": len(connections), "connections": connections})
                probe = volcanic()
                record.setdefault("volcanic_probes", []).append({"at": now(), "status": probe["status"],
                    "count": probe["body"]["count"], "source": probe["body"]["sources"]["PVMBG"]})
                pvmbg = probe["body"]["sources"]["PVMBG"]
                progress(f"{name}: {len(connections)} TCP connections; {probe['body']['count']} volcanic records; "
                         f"PVMBG available={pvmbg['available']}, stale={pvmbg['stale']}")
            progress(f"{name}: waiting for the 60-second run and per-VU refresh checks to finish")
            process.wait(timeout=35)
            record["measurement_wall_seconds"] = time.monotonic() - measured
            record["exit_code"] = process.returncode
        finally:
            if process.poll() is None:
                progress(f"{name}: stopping unfinished k6 process before saving its sanitized log")
                process.terminate()
                process.wait(timeout=10)
            record["exit_code"] = process.returncode
            log_path.write_text(redact(log_path.read_text()))
    progress(f"{name}: collecting latency, error, refresh, and upstream-call results")
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
    metrics = record["metrics"]
    progress(f"{name}: {'PASS' if record['pass'] else 'FAIL'}; "
             f"p95={metrics.get('bmkg_elapsed_ms', {}).get('p(95)', 'n/a')} ms (target <300); "
             f"200={metrics.get('responses_200', {}).get('count', 'n/a')}; "
             f"429={metrics.get('responses_429', {}).get('count', 'n/a')}; "
             f"uncontrolled error rate={metrics.get('uncontrolled_errors', {}).get('rate', 'n/a')}; "
             f"refreshes={metrics.get('refresh_successes', {}).get('count', 'n/a')}")
    return record


progress(f"Preflight: project={options.project_name or config.get('name', 'Compose default')}; output={folder}")
progress("Checking k6, container identities, nine healthy services, and access-token TTL")
evidence = {"started_at": now(), "scope": "AUTHENTICATED_CLIENT_API", "runs": {},
            "base_commit": subprocess.check_output(["git", "rev-parse", "HEAD"], text=True).strip(),
            "tested_source": "WORKSPACE", "k6": subprocess.check_output(["k6", "version"], text=True).strip(),
            "host": {"platform": platform.platform(), "cpu_logical": os.cpu_count()}, "states_before": states()}
try:
    status = [json.loads(line) for line in run("ps", "--format", "json").splitlines()]
    healthy_count = sum(s["State"] == "running" and s["Health"] == "healthy" for s in status)
    progress(f"Preflight: found {len(status)}/9 services; {healthy_count} running and healthy")
    assert len(status) == 9 and all(s["State"] == "running" and s["Health"] == "healthy" for s in status), "nine services must be healthy"
    assert int(settings["ACCESS_TOKEN_TTL_SECONDS"]) == 60, "P2 must retain access TTL 60s"
    original = mock_state()
    progress(f"Preflight complete; original PVMBG delay={original['delay_ms']} ms, "
             f"schema={original['schema_version']}, outage={original['outage']}")
except Exception as error:
    evidence.update(result="FAIL", failure=redact(str(error)), cleanup="NOT_NEEDED_NO_MUTATION", finished_at=now())
    (folder / "p2-client-check.json").write_text(redact(json.dumps(evidence, indent=2)) + "\n")
    progress(f"FAIL before source changes; results: {folder / 'p2-client-check.json'}")
    raise
evidence["original_pvmbg"] = original
slow = compose + ["-f", "tests/compose-load.yml"]
try:
    progress("Slow scenario: recreating only PVMBG with a 3000 ms delay")
    result = subprocess.run(slow + ["up", "-d", "--no-deps", "--wait", "pvmbg"], capture_output=True, text=True)
    assert result.returncode == 0, "slow PVMBG recreation failed"
    admin("schema-version", {"version": original["schema_version"]})
    admin("outage", {"enabled": False})
    progress("Slow scenario: waiting for fresh committed PVMBG ingestion")
    wait_for(lambda: source()["available"] and not source()["stale"], "slow PVMBG must finish ingestion")
    slow_result = load_run("slow")
    evidence["slow_upstream_observed"] = any(r.get("target") == "pvmbg" and r.get("status") == 200
        and r.get("latency_ms", 0) >= 3000 for r in slow_result["upstream_calls"])
    evidence["outage_enabled_at"] = now()
    progress("Outage scenario: enabling PVMBG outage and waiting for stale-source metadata")
    admin("outage", {"enabled": True})
    wait_for(lambda: not source()["available"] and source()["stale"] and source()["stale_since"],
             "failed PVMBG ingestion must become stale")
    baseline = volcanic()
    evidence["outage_baseline"] = baseline
    bmkg_before = baseline["body"]["sources"]["BMKG"]["last_ingested_at"]
    progress("Outage scenario: saved last-known volcanic data; starting the second load run")
    outage_result = load_run("outage")
    after = volcanic()
    evidence["after_outage_load"] = after
    progress("Outage scenario: verifying retained volcanic data, continued BMKG ingestion, and upstream 503s")
    assert after["body"]["data"] == baseline["body"]["data"], "last-known volcanic snapshots must remain readable"
    status = after["body"]["sources"]["PVMBG"]
    assert not status["available"] and status["stale"] and status["stale_since"]
    assert status["last_ingested_at"] == baseline["body"]["sources"]["PVMBG"]["last_ingested_at"]
    assert after["body"]["sources"]["BMKG"]["last_ingested_at"] > bmkg_before
    assert all(p["source"]["stale"] and not p["source"]["available"] for p in outage_result["volcanic_probes"])
    assert any(r.get("target") == "pvmbg" and r.get("status") == 503 for r in outage_result["upstream_calls"])
    progress("Recovery: disabling outage and creating a schema-v2 report")
    admin("outage", {"enabled": False})
    report_id = admin("schema-version", {"version": 2})["report_id"]
    evidence["recovery_report_id"] = report_id
    progress("Recovery: waiting for the new report and fresh-source metadata without restarting core services")
    wait_for(lambda: source()["available"] and not source()["stale"] and any(
        r["source_ref_id"] == report_id for r in volcanic()["body"]["data"]), "new report must persist after recovery")
    evidence["after_recovery"] = volcanic()
    assert source()["last_ingested_at"] > status["last_ingested_at"]
    assert slow_result["pass"] and outage_result["pass"], "P2 thresholds/socket proof failed; inspect both run summaries"
    assert evidence["slow_upstream_observed"], "no actual 3-second PVMBG fetch observed during load"
    evidence["result"] = "PASS"
    progress("Scenario checks complete; verifying restoration before the overall result")
except Exception as error:
    evidence["result"] = "FAIL"
    evidence["failure"] = redact(str(error))
    progress("FAIL: scenario check failed; restoring the original PVMBG settings (see result file)")
    raise
finally:
    try:
        progress("Cleanup: restoring original PVMBG delay, schema, and outage state")
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
        progress("Cleanup: PVMBG restored; other service identities unchanged")
    except Exception as error:
        evidence["result"] = "FAIL"
        evidence["cleanup"] = "FAIL"
        evidence["restoration_failure"] = redact(str(error))
        progress("Cleanup FAIL: original settings could not be fully restored; inspect locally")
        raise
    finally:
        evidence["finished_at"] = now()
        (folder / "p2-client-check.json").write_text(redact(json.dumps(evidence, indent=2)) + "\n")
        progress(f"Results saved: {folder / 'p2-client-check.json'}")
print("PASS: authenticated 50-VU/60s slow and outage runs; per-VU refresh; last-known data; recovery; exact runtime restore")
print(folder / "p2-client-check.json")
