#!/usr/bin/env python3
"""C's P2 check: 50 VUs/60s, slow PVMBG, cached reads during outage, recovery.

Requires native k6/lsof and seven live services using .env.example. Finally restores
PVMBG's default delay, schema v2, and outage=false. Never restart Aggregator.
"""
import json
import os
from pathlib import Path
import platform
import subprocess
import time
import urllib.request
from demo_support import compose, healthy, logs, now, run, settings, sql, states, wait_for

folder = Path("docs/evidence/anggota-c/stage-5")
folder.mkdir(parents=True, exist_ok=True)
base = "http://127.0.0.1:" + settings["AGGREGATOR_PORT"]
pvmbg = "http://127.0.0.1:" + settings["PVMBG_PORT"]


def request(url, body=None):
    correlation = "p2-probe-" + str(time.time_ns())
    headers = {"X-Correlation-ID": correlation}
    if body is not None:
        headers.update({"Content-Type": "application/json", "Authorization": "Bearer " + settings["PVMBG_TOKEN"]})
    req = urllib.request.Request(url, data=None if body is None else json.dumps(body).encode(), headers=headers)
    started = time.monotonic()
    with urllib.request.urlopen(req, timeout=5) as response:
        result = {"status": response.status, "correlation_id": response.headers.get("X-Correlation-ID"),
                  "body": json.load(response), "elapsed_ms": (time.monotonic() - started) * 1000}
    assert result["correlation_id"] == correlation
    return result


def volcanic():
    return request(base + "/internal/v1/hazards?source=PVMBG&hazard_type=VOLCANIC&limit=100")


def source():
    return request(base + "/health")["body"]["sources"]["PVMBG"]


evidence = {"started_at": now(), "scope": "AGGREGATOR_DIRECT; AUTH_CLIENT_API_PENDING_B",
            "tool": subprocess.check_output(["k6", "version"], text=True).strip(),
            "host": {"platform": platform.platform(), "cpu_logical": os.cpu_count()},
            "states_before": states(), "default_delay_ms": int(settings["PVMBG_DELAY_MS"])}
assert all(healthy(s) for s in ("aggregator", "notification-consumer", "dashboard-consumer"))
assert request(base + "/internal/v1/hazards?source=BMKG&limit=1")["body"]["count"] > 0
slow = compose + ["-f", "docker-compose.yml", "-f", "tests/compose-load.yml"]
try:
    subprocess.check_call(slow + ["up", "-d", "--no-deps", "pvmbg"])
    wait_for(lambda: request(pvmbg + "/health")["status"] == 200, "PVMBG slow mock should start")
    request(pvmbg + "/admin/schema-version", {"version": 2})
    wait_for(lambda: source()["available"], "slow PVMBG should still be available")
    evidence["canonical_counts_before_load"] = json.loads(sql(
        "SELECT json_object_agg(source,n) FROM (SELECT source,count(*) n FROM hazard_events GROUP BY source) t"))
    evidence["load_started_at"] = now()
    started = time.monotonic()
    with (folder / "load.log").open("w") as output:
        load = subprocess.Popen(["k6", "run", "--quiet", "--env", "BASE_URL=" + base,
            "--env", "SUMMARY_PATH=" + str(folder / "load-summary.json"), "tests/load-p2.js"],
            stdout=output, stderr=subprocess.STDOUT)
        try:
            evidence["connection_samples"] = []
            for _ in range(3):
                time.sleep(15)
                sockets = subprocess.check_output(["lsof", "-nP", "-a", "-p", str(load.pid),
                    "-iTCP", "-sTCP:ESTABLISHED", "-Fn"], text=True, timeout=5)
                connections = sorted(set(line[1:] for line in sockets.splitlines()
                    if line.startswith("n") and line.endswith("->127.0.0.1:" + settings["AGGREGATOR_PORT"])))
                evidence["connection_samples"].append({"at": now(), "elapsed_seconds": time.monotonic() - started,
                    "established_count": len(connections), "connections": connections})
            load.wait(timeout=30)
        finally:
            if load.poll() is None:
                load.terminate()
                load.wait(timeout=10)
    evidence["load_finished_at"] = now()
    evidence["load_wall_seconds"] = time.monotonic() - started
    evidence["load_exit_code"] = load.returncode
    # Keep only upstream calls, not thousands of request/store logs.
    evidence["upstream_during_load"] = [r for r in logs("aggregator", evidence["load_started_at"])
                                       if r.get("target") in ("bmkg", "pvmbg")]
    assert load.returncode == 0, "k6 thresholds failed; see load-summary.json/load.log"
    assert evidence["load_wall_seconds"] >= 60
    assert all(s["established_count"] >= 50 for s in evidence["connection_samples"]), "prove 50 actual established TCP connections"
    assert any(r.get("target") == "pvmbg" and r.get("status") == 200 and r.get("latency_ms", 0) >= 3000
               for r in evidence["upstream_during_load"]), "prove actual 3-second upstream calls during load"
    # Outage is an authenticated mock 503, not a container restart.
    baseline = volcanic()
    evidence["before_outage"] = baseline
    assert baseline["body"]["count"] > 0
    evidence["outage_enabled_at"] = now()
    evidence["outage_admin"] = request(pvmbg + "/admin/outage", {"enabled": True})
    wait_for(lambda: not source()["available"], "poller should report PVMBG unavailable")
    evidence["during_outage"] = volcanic()
    status = evidence["during_outage"]["body"]["sources"]["PVMBG"]
    assert not status["available"] and status["last_success_at"] and status["last_error"]
    # Capture AFTER an in-flight successful fetch can finish, then compare stable stored results.
    stable = evidence["during_outage"]["body"]["data"]
    bmkg_before = request(base + "/health")["body"]["sources"]["BMKG"]["last_success_at"]
    evidence["bmkg_during_outage"] = []
    for _ in range(5):
        seismic = request(base + "/internal/v1/hazards?source=BMKG&hazard_type=SEISMIC&limit=100")
        assert seismic["body"]["count"] > 0 and seismic["elapsed_ms"] < 300
        evidence["bmkg_during_outage"].append({k: seismic[k] for k in ("status", "elapsed_ms", "correlation_id")})
        time.sleep(3)
    evidence["after_outage_reads"] = volcanic()
    assert evidence["after_outage_reads"]["body"]["data"] == stable, "stored volcanic data must remain readable and unchanged"
    assert not evidence["after_outage_reads"]["body"]["sources"]["PVMBG"]["available"]
    assert evidence["after_outage_reads"]["body"]["sources"]["PVMBG"]["last_success_at"] == status["last_success_at"]
    evidence["bmkg_source_during_outage"] = request(base + "/health")["body"]["sources"]["BMKG"]
    assert evidence["bmkg_source_during_outage"]["available"]
    assert evidence["bmkg_source_during_outage"]["last_success_at"] > bmkg_before, "BMKG polling must keep running"
    evidence["outage_disabled_at"] = now()
    evidence["recovery_admin"] = request(pvmbg + "/admin/outage", {"enabled": False})
    new_report = request(pvmbg + "/admin/schema-version", {"version": 2})["body"]["report_id"]
    evidence["recovery_report_id"] = new_report
    wait_for(lambda: source()["available"] and any(r["source_ref_id"] == new_report for r in volcanic()["body"]["data"]),
             "recover and persist a new volcanic report without restarting Aggregator")
    evidence["after_recovery"] = volcanic()
    evidence["upstream_during_outage_recovery"] = [r for r in logs("aggregator", evidence["outage_enabled_at"])
                                                if r.get("target") in ("bmkg", "pvmbg")]
    assert any(r.get("target") == "pvmbg" and r.get("status") == 503 for r in evidence["upstream_during_outage_recovery"])
    evidence["result"] = "PASS_C_SCOPE; AUTH_CLIENT_API_PENDING_B"
except Exception as error:
    evidence["result"] = "FAIL"
    evidence["failure"] = str(error)
    raise
finally:
    try:
        run("up", "-d", "--no-deps", "pvmbg")
        wait_for(lambda: request(pvmbg + "/health")["status"] == 200, "restore default PVMBG")
        request(pvmbg + "/admin/outage", {"enabled": False})
        request(pvmbg + "/admin/schema-version", {"version": 2})
        wait_for(lambda: source()["available"], "restore normal PVMBG polling")
        evidence["states_after"] = states()
        for service, state in evidence["states_before"].items():
            if service != "pvmbg" and state["running"]:
                assert evidence["states_after"][service] == state, service + " must not restart"
        evidence["restored"] = {"delay_ms": int(settings["PVMBG_DELAY_MS"]), "schema_version": 2, "outage": False}
    except Exception as error:
        evidence["result"] = "FAIL"
        evidence["restoration_failure"] = str(error)
        raise
    finally:
        evidence["finished_at"] = now()
        (folder / "p2-check.json").write_text(json.dumps(evidence, indent=2) + "\n")
print("PASS C: 50 VUs/60s, slow PVMBG, last-known reads during outage, recovery without Aggregator restart")
print("PENDING B: authenticated Client API load and downstream freshness")
print(folder / "p2-check.json")
