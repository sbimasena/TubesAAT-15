#!/usr/bin/env python3
"""Stage 5 HTTP checkpoint; sustained P2 load and full-stack fanout are separate checks.

Requires an already-running, explicitly named Compose checkpoint project.
With --exercise-outages, toggle PVMBG outage and stop/start Auth and PostgreSQL
in the named test project. Restore those services and the original PVMBG flag.
Leaves the stack running and preserves volumes. Evidence omits credentials, tokens, and hazard payloads.
"""
import argparse
from collections import Counter
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime, timezone
import json
from pathlib import Path
import subprocess
import threading
import time
import urllib.error
import urllib.request
import uuid
from checker_progress import progress, heartbeat


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--env-file", default=".env")
    parser.add_argument("--project-name", required=True, help="Explicit Compose checkpoint project")
    parser.add_argument("--exercise-outages", action="store_true")
    parser.add_argument("--burst-connections", type=int, default=0, help="Optional short burst, not sustained P2 load (2-256)")
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    progress("Starting configuration and prerequisite checks")
    assert args.burst_connections == 0 or 2 <= args.burst_connections <= 256, "Burst size must be 2-256"
    env = {}
    for line in Path(args.env_file).read_text(encoding="utf-8-sig").splitlines():
        line = line.strip()
        if line and not line.startswith("#") and "=" in line:
            key, value = line.split("=", 1)
            env[key.strip()] = value.strip().strip("\"'")
    root = Path(__file__).resolve().parent.parent
    command = ["docker", "compose", "--env-file", str(Path(args.env_file).resolve()),
               "-p", args.project_name, "-f", str(root / "docker-compose.yml")]

    def compose(*parts):
        if parts and parts[0] in ("up", "stop", "start", "restart", "build"):
            progress("Compose " + parts[0] + ": running selected lifecycle operation")
        result = subprocess.run(command + list(parts), capture_output=True, text=True, timeout=60)
        if result.returncode:
            raise RuntimeError("Compose checkpoint operation failed")
        return result.stdout

    ports = {"client": env.get("CLIENT_API_PORT", "8080"), "auth": env.get("AUTH_PORT", "8084"),
             "pvmbg": env.get("PVMBG_PORT", "8082")}
    prefix = "member-b-resilience-" + uuid.uuid4().hex
    started_at = datetime.now(timezone.utc).isoformat()
    evidence = {"stage": "Member B Stage 5", "checked_at": started_at, "correlation_prefix": prefix,
                "sustained_p2_load_checked": False, "full_stack_fanout_checked": False, "checks": []}
    sensitive = [env.get(key, "") for key in ("JWT_SIGNING_SECRET", "AUTH_INTERNAL_SECRET",
                 "MEDIA_CLIENT_PASSWORD", "FIELD_TEAM_CLIENT_PASSWORD", "INTERNAL_OPS_CLIENT_PASSWORD")]
    source_fields = {"available", "last_ingested_at", "stale", "stale_since", "stale_after_seconds"}

    def call(service, path, body=None, access=None, correlation=None):
        correlation = correlation or prefix + "-" + uuid.uuid4().hex[:8]
        headers = {"X-Correlation-ID": correlation}
        if access:
            headers["Authorization"] = "Bearer " + access
        if body is not None:
            headers["Content-Type"] = "application/json"
        request = urllib.request.Request("http://127.0.0.1:" + ports[service] + path,
                                         data=None if body is None else json.dumps(body).encode(), headers=headers)
        try:
            response = urllib.request.urlopen(request, timeout=15)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            data = json.load(response)
            if service in ("auth", "client"):
                assert response.headers.get("X-Correlation-ID") == correlation, "Correlation ID changed"
                assert response.headers.get("Cache-Control") == "no-store", "Cache policy missing"
            if service == "client":
                assert response.headers.get("Content-Type") == "application/json", "Client response is not JSON"
                if response.status >= 400:
                    assert set(data) == {"error", "correlation_id"} and data["correlation_id"] == correlation, "Invalid error envelope"
                    assert set(data["error"]) == {"code", "message"}, "Invalid error detail"
                    if response.status == 429:
                        assert data["error"]["code"] == "concurrency_limit" and response.headers.get("Retry-After") == "1", "Invalid 429 contract"
            return response.status, data

    def login():
        status, pair = call("auth", "/login", {"client_id": env.get("MEDIA_CLIENT_ID", "media"),
                                                "password": env["MEDIA_CLIENT_PASSWORD"]})
        assert status == 200, "Media login failed"
        sensitive.extend((pair["access_token"], pair["refresh_token"]))
        return pair

    def hazards(access, query="", correlation=None):
        status, payload = call("client", "/hazards" + query, access=access, correlation=correlation)
        if status == 200:
            assert set(payload) == {"data", "count", "sources"} and payload["count"] == len(payload["data"]), "Invalid success envelope"
            assert set(payload["sources"]) == {"BMKG", "PVMBG"}, "Missing source metadata"
            for metadata in payload["sources"].values():
                assert set(metadata) == source_fields, "Private source diagnostics leaked"
                assert type(metadata["available"]) is bool and type(metadata["stale"]) is bool, "Invalid source flags"
                assert metadata["stale_after_seconds"] > 0, "Invalid freshness threshold"
                for key in ("last_ingested_at", "stale_since"):
                    if metadata[key] is not None:
                        datetime.fromisoformat(metadata[key].replace("Z", "+00:00"))
            summary = {"hazard_id", "source", "hazard_type", "severity", "area_name", "occurred_at", "ingested_at"}
            assert all(set(row) == summary for row in payload["data"]), "Media raw fields leaked"
        return status, payload

    def wait_for(check, label):
        deadline = time.monotonic() + 45
        attempt = 0
        while time.monotonic() < deadline:
            heartbeat(label, attempt, every=10)
            attempt += 1
            value = check()
            if value:
                progress("Ready: " + label)
                return value
            time.sleep(1)
        raise AssertionError(label)

    stopped = set()
    pvmbg_original = None
    progress("Checking source projection, filters and error contracts")
    try:
        pair = login()
        access = pair["access_token"]
        trace_id = prefix + "-hazards"
        assert hazards(access, "?source=BMKG", trace_id)[0] == 200, "Protected read failed"
        wait_for(lambda: all(not s["stale"] and s["last_ingested_at"] for s in hazards(access)[1]["sources"].values()),
                 "Both sources must complete ingestion before this checkpoint")
        status, empty = hazards(access, "?source=PVMBG&since=2999-01-01T00:00:00Z")
        assert status == 200 and empty["data"] == [] and empty["count"] == 0, "Empty filter became an availability error"
        status, failure = hazards(access, "?source=UNKNOWN")
        assert status == 400 and failure["error"]["code"] == "invalid_query", "Aggregator 400 mapping failed"
        status, failure = hazards(access, "?include_raw=true")
        assert status == 403 and failure["error"]["code"] == "forbidden", "Raw request was accepted"
        evidence["checks"].extend(("safe metadata and Media projection", "empty filter -> 200", "invalid filter -> JSON 400", "raw -> JSON 403"))
        status, pair = call("auth", "/refresh", {"refresh_token": pair["refresh_token"]})
        assert status == 200, "Refresh failed"
        sensitive.extend((pair["access_token"], pair["refresh_token"]))
        access = pair["access_token"]

        progress("Checking optional connection burst")
        if args.burst_connections:
            barrier = threading.Barrier(args.burst_connections)
            def query(_):
                barrier.wait(timeout=15)
                return hazards(access)[0]
            with ThreadPoolExecutor(max_workers=args.burst_connections) as pool:
                counts = Counter(pool.map(query, range(args.burst_connections)))
            assert set(counts) <= {200, 429} and counts[200] > 0, "Burst produced an uncontrolled failure"
            evidence["burst"] = {"connections": args.burst_connections, "status_counts": dict(counts),
                                 "controlled_429_observed": counts[429] > 0}

        progress("Checking optional dependency outages and recovery")
        if args.exercise_outages:
            _, health = call("pvmbg", "/health")
            pvmbg_original = health["simulated_outage"]
            before = hazards(access, "?source=PVMBG&limit=5")[1]
            before_ids = {row["hazard_id"] for row in before["data"]}
            assert before_ids, "No stored volcanic data before outage"
            call("pvmbg", "/admin/outage", {"enabled": True}, env["PVMBG_TOKEN"])
            def stale():
                status, payload = hazards(access, "?source=PVMBG&limit=5")
                metadata = payload["sources"]["PVMBG"]
                return payload if status == 200 and not metadata["available"] and metadata["stale"] else None
            degraded = wait_for(stale, "PVMBG outage did not propagate")
            assert degraded["sources"]["PVMBG"]["stale_since"] and before_ids & {row["hazard_id"] for row in degraded["data"]}, "Last-known data or stale timestamp lost"
            assert hazards(access, "?source=BMKG")[0] == 200, "Seismic read broken by PVMBG outage"
            call("pvmbg", "/admin/outage", {"enabled": pvmbg_original}, env["PVMBG_TOKEN"])
            wait_for(lambda: not hazards(access)[1]["sources"]["PVMBG"]["stale"], "PVMBG did not recover without restart")
            evidence["checks"].append("real PVMBG outage -> stored data 200/stale; BMKG readable; recovery without restart")

            for service, code in (("canonical-store", "aggregator_unavailable"), ("auth", "auth_unavailable")):
                stopped.add(service)
                compose("stop", service)
                started = time.monotonic()
                status, failure = hazards(access)
                elapsed = round((time.monotonic() - started) * 1000, 2)
                assert status == 503 and failure["error"]["code"] == code, "Dependency outage mapping failed"
                assert call("client", "/health")[0] == 200, "Client health depended on the stopped service"
                compose("up", "-d", "--no-deps", "--wait", "--wait-timeout", "30", service)
                stopped.remove(service)
                if service == "auth":
                    access = login()["access_token"]
                wait_for(lambda: hazards(access)[0] == 200, "Read did not recover after dependency restart")
                evidence["checks"].append(f"{service} outage -> JSON 503 in {elapsed}ms; health 200; recovery")

        logs = {}
        for service in ("auth", "client-api", "aggregator"):
            raw = compose("logs", "--no-log-prefix", "--since", started_at, service)
            if service != "aggregator":
                assert not any(value and value in raw for value in sensitive), "Secret/token appeared in Member B logs"
            records = []
            for line in raw.splitlines():
                try:
                    record = json.loads(line)
                except json.JSONDecodeError:
                    continue
                if isinstance(record, dict):
                    records.append(record)
            logs[service] = records
        for service in logs:
            assert any(r.get("correlation_id") == trace_id and isinstance(r.get("latency_ms"), (int, float)) for r in logs[service]), "Cross-service correlation/latency trace missing"
        for operation in ("/login", "/refresh"):
            assert any(r.get("operation") == operation and r.get("status") == 200 and r.get("client_id") == env.get("MEDIA_CLIENT_ID", "media") and r.get("scope") == "media" for r in logs["auth"]), "Auth verified identity trace missing"
        for target in ("auth", "aggregator"):
            assert any(r.get("target") == target and r.get("correlation_id") == trace_id for r in logs["client-api"]), "Outbound dependency trace missing"
        if args.burst_connections and evidence["burst"]["controlled_429_observed"]:
            assert any(r.get("status") == 429 and r.get("error_kind") == "concurrency_limit" for r in logs["client-api"]), "429 log missing"
        evidence["checks"].append("verified login/refresh identity, cross-service correlation/latency, token/secret redaction")
        evidence["status"] = "PASS"
    finally:
        progress("Restoring stopped services and original PVMBG outage flag")
        for service in sorted(stopped):
            compose("up", "-d", "--no-deps", "--wait", "--wait-timeout", "30", service)
        if pvmbg_original is not None:
            call("pvmbg", "/admin/outage", {"enabled": pvmbg_original}, env["PVMBG_TOKEN"])
    if args.output:
        args.output.write_text(json.dumps(evidence, indent=2) + "\n", encoding="utf-8")
        progress("Result file saved")
    progress("PASS: resilience checks")
    print(json.dumps(evidence, indent=2))


if __name__ == "__main__":
    try:
        main()
    except (AssertionError, OSError, ValueError, KeyError, RuntimeError, subprocess.SubprocessError) as error:
        detail = str(error) if isinstance(error, AssertionError) else type(error).__name__
        print(f"FAIL: {detail}; inspect the named checkpoint project and local configuration.")
        raise SystemExit(1)
