"""Verify Member B health, storage isolation, and the protected Aggregator read path.

Requires local credentials and the opt-in legacy hazard alias in the B Compose overlay.
--start builds/starts the stack; --check-lifecycle stops/starts only Auth and Client API.
Leaves the stack running and preserves volumes. Full P2/P3 acceptance is checked separately.
"""
import argparse
import datetime
import json
from pathlib import Path
import subprocess
import time
import urllib.request
import uuid


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--env-file", default=".env", help="Local, uncommitted configuration")
    parser.add_argument("--project-name", help="Use an existing or isolated Compose project")
    parser.add_argument("--start", action="store_true", help="Build/start all nine services")
    parser.add_argument("--check-lifecycle", action="store_true", help="Stop/start only Auth and Client API")
    parser.add_argument("--output", type=Path, help="Write evidence without credentials or hazard payloads")
    args = parser.parse_args()
    command = ["docker", "compose", "--env-file", args.env_file,
               "-f", "docker-compose.yml", "-f", "tests/compose-operator.yml",
               "-f", "tests/compose-member-b.yml"]
    if args.project_name:
        command += ["--project-name", args.project_name]

    def compose(*options):
        timeout = 600 if "--build" in options else 240
        result = subprocess.run(command + list(options), capture_output=True, text=True, timeout=timeout)
        if result.returncode:
            # Config output contains local credentials; never include it in failure evidence.
            raise RuntimeError(f"Compose {options[0]} failed (exit {result.returncode}); inspect Docker locally")
        return result.stdout

    config = json.loads(compose("config", "--format", "json"))
    services = config["services"]
    for service in ("auth", "client-api"):
        assert "storage" not in services[service].get("networks", {}), f"{service} joined canonical storage"
        assert "DATABASE_URL" not in services[service].get("environment", {}), f"{service} received canonical credentials"
    assert str(services["client-api"]["environment"]["ENABLE_PROVISIONAL_HAZARD_ENDPOINT"]).lower() == "true", (
        "Set ENABLE_PROVISIONAL_HAZARD_ENDPOINT=true only in the local checkpoint configuration")

    def origin(service):
        for port in services[service].get("ports", []):
            if port.get("protocol", "tcp") == "tcp":
                return f"http://127.0.0.1:{port['published']}"
        raise RuntimeError(f"{service} has no published checkpoint port")

    def get(url, correlation, access=None):
        headers = {"X-Correlation-ID": correlation}
        if access:
            headers["Authorization"] = "Bearer " + access
        request = urllib.request.Request(url, headers=headers)
        with urllib.request.urlopen(request, timeout=10) as response:
            assert response.status == 200, "Expected HTTP 200"
            assert response.headers.get("X-Correlation-ID") == correlation, "Correlation ID changed"
            return json.load(response), response.headers

    if args.start:
        compose("up", "--build", "-d", "--wait", "--wait-timeout", "120")
    if args.check_lifecycle:
        try:
            compose("stop", "auth", "client-api")
            states = compose("ps", "--all", "--format", "{{.Service}} {{.State}}").splitlines()
            for service in ("auth", "client-api"):
                assert f"{service} exited" in states, f"{service} did not stop"
        finally:
            compose("up", "-d", "--no-deps", "--wait", "--wait-timeout", "30", "auth", "client-api")

    started = datetime.datetime.now(datetime.timezone.utc).isoformat()
    for service in ("auth", "client-api"):
        compose("exec", "-T", service, "/service", "--healthcheck")
        health, _ = get(origin(service) + "/health", f"member-b-health-{service}")
        assert health == {"service": service, "status": "ok"}, f"Unexpected {service} health response"

    # Wait for actual committed records from both sources, with a bounded deadline.
    deadline = time.monotonic() + 60
    while True:
        health, _ = get(origin("aggregator") + "/health", "member-b-storage-health")
        if health["storage"]["available"] and all(
                health["sources"][source].get("last_ingested_at") and not health["sources"][source]["stale"]
                for source in ("BMKG", "PVMBG")):
            break
        if time.monotonic() >= deadline:
            raise RuntimeError("Both sources must complete ingestion before the Stage 3 checkpoint")
        time.sleep(1)

    # Request full canonical fields on the protected legacy alias as Internal Ops.
    auth_env = services["auth"]["environment"]
    credentials = {"client_id": auth_env["INTERNAL_OPS_CLIENT_ID"], "password": auth_env["INTERNAL_OPS_CLIENT_PASSWORD"]}
    request = urllib.request.Request(origin("auth") + "/login", data=json.dumps(credentials).encode(),
                                     headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(request, timeout=15) as response:
        access = json.load(response)["access_token"]

    evidence = {"stage": "Member B Stage 2–3", "status": "PASS", "checked_at": started,
                "auth_checked": True, "lifecycle_checked": args.check_lifecycle, "sources": {}}
    required = {"hazard_id", "source", "source_ref_id", "hazard_type", "severity", "area_name",
                "latitude", "longitude", "occurred_at", "ingested_at", "attributes"}
    for source, hazard_type in (("BMKG", "SEISMIC"), ("PVMBG", "VOLCANIC")):
        correlation = "member-b-flow-" + uuid.uuid4().hex
        query = f"?source={source}&hazard_type={hazard_type}&since=1970-01-01T00:00:00Z&limit=5"
        direct, _ = get(origin("aggregator") + "/internal/v1/hazards" + query, correlation)
        proxied, headers = get(origin("client-api") + "/internal/provisional/hazards" + query, correlation, access)
        assert headers.get("X-Provisional-Endpoint") == "true", "Missing provisional marker"
        assert headers.get("Cache-Control") == "no-store", "Missing no-store marker"
        assert proxied["count"] == len(proxied["data"]) and 0 < proxied["count"] <= 5, "No bounded records"
        direct_rows = {row["hazard_id"]: row for row in direct["data"]}
        matched = 0
        for row in proxied["data"]:
            assert required <= row.keys(), "Canonical fields lost"
            assert row["source"] == source and row["hazard_type"] == hazard_type, "Filters not applied"
            if row["hazard_id"] in direct_rows:
                assert row == direct_rows[row["hazard_id"]], "Canonical response changed across HTTP proxy"
                matched += 1
        assert matched, "No common canonical record; retry checkpoint after concurrent source updates"
        metadata = direct["sources"][source]
        assert isinstance(metadata["stale"], bool) and metadata["last_ingested_at"], "Aggregator freshness metadata missing"
        for service in ("client-api", "aggregator"):
            records = []
            for line in compose("logs", "--no-log-prefix", "--since", started, service).splitlines():
                try:
                    records.append(json.loads(line))
                except json.JSONDecodeError:
                    pass
            assert any(record.get("correlation_id") == correlation and
                       isinstance(record.get("latency_ms"), (int, float)) for record in records), (
                f"Missing correlation/latency evidence in {service}")
        evidence["sources"][source] = {"count": proxied["count"], "correlation_id": correlation,
                                      "stale": metadata["stale"], "last_ingested_at": metadata["last_ingested_at"]}
    payload = json.dumps(evidence, indent=2) + "\n"
    if args.output:
        args.output.write_text(payload, encoding="utf-8")
    print(payload, end="")


if __name__ == "__main__":
    try:
        main()
    except (AssertionError, RuntimeError, OSError, ValueError, KeyError, subprocess.TimeoutExpired) as error:
        # URLs/configuration can contain credentials; use a fixed public failure message.
        detail = str(error) if isinstance(error, (AssertionError, RuntimeError)) else type(error).__name__
        print(f"FAIL: {detail}; verify local configuration, health, and Compose logs.")
        raise SystemExit(1)
