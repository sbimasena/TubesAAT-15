#!/usr/bin/env python3
"""Check the default nine-service deployment, ownership, and protected HTTP reads."""
import argparse
import json
from pathlib import Path
import subprocess
import urllib.request
import uuid

from demo_support import compose, now, redact, settings, wait_for

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--start", action="store_true", help="Build/start the default stack in the selected project")
parser.add_argument("--output", type=Path)
args = parser.parse_args()
# The shared demos opt into operator access; deployment must prove the base file alone.
deployment = compose[:-2]


def run(*parts):
    result = subprocess.run(deployment + list(parts), capture_output=True, text=True)
    if result.returncode:
        raise RuntimeError("Deployment Compose operation failed; inspect the selected project locally")
    return result.stdout.strip()


config = json.loads(run("config", "--format", "json"))
services = config["services"]
expected = {"bmkg", "pvmbg", "aggregator", "canonical-store", "message-broker",
            "auth", "client-api", "notification-consumer", "dashboard-consumer"}
assert set(services) == expected
assert all(service.get("healthcheck", {}).get("test") for service in services.values())
assert not services["aggregator"].get("ports") and not services["canonical-store"].get("ports")
assert {name for name, service in services.items() if "storage" in service["networks"]} == {"aggregator", "canonical-store"}
assert {name for name, service in services.items() if "DATABASE_URL" in service.get("environment", {})} == {"aggregator"}
assert {name for name, service in services.items() if "JWT_SIGNING_SECRET" in service.get("environment", {})} == {"auth"}
assert {name for name, service in services.items() if "AUTH_INTERNAL_SECRET" in service.get("environment", {})} == {"auth", "client-api"}

if args.start:
    run("up", "--build", "-d", "--wait", "--wait-timeout", "120")

states = [json.loads(line) for line in run("ps", "--format", "json").splitlines()]
assert {state["Service"] for state in states} == expected
assert all(state["State"] == "running" and state["Health"] == "healthy" for state in states)
# Inspect actual container port bindings too, in case an operator overlay was used previously.
for service in ("aggregator", "canonical-store"):
    inspection = subprocess.run(["docker", "inspect", "--format", "{{json .HostConfig.PortBindings}}",
                                 run("ps", "-q", service)], capture_output=True, text=True, check=True)
    assert not json.loads(inspection.stdout), service + " must not have a published port in default deployment"

query_ids = []

def call(port, path, body=None, token=None):
    correlation = "deployment-" + uuid.uuid4().hex
    if path.startswith("/hazards"):
        query_ids.append(correlation)
    headers = {"X-Correlation-ID": correlation}
    if token:
        headers["Authorization"] = "Bearer " + token
    if body is not None:
        headers["Content-Type"] = "application/json"
    request = urllib.request.Request("http://127.0.0.1:" + port + path,
        data=None if body is None else json.dumps(body).encode(), headers=headers)
    with urllib.request.urlopen(request, timeout=15) as response:
        assert response.status == 200 and response.headers.get("X-Correlation-ID") == correlation
        return json.load(response)


reads = {}
for role in ("MEDIA", "FIELD_TEAM", "INTERNAL_OPS"):
    pair = call(settings["AUTH_PORT"], "/login", {"client_id": settings[role + "_CLIENT_ID"],
                                               "password": settings[role + "_CLIENT_PASSWORD"]})
    def readable():
        data = call(settings["CLIENT_API_PORT"], "/hazards", token=pair["access_token"])
        return data if {row["source"] for row in data["data"]} == {"BMKG", "PVMBG"} and all(
            not source["stale"] and source["last_ingested_at"] for source in data["sources"].values()) else None
    wait_for(readable, "both sources must complete ingestion and be readable through Client API")
    payload = readable()
    summary = {"hazard_id", "source", "hazard_type", "severity", "area_name", "occurred_at", "ingested_at"}
    fields = summary if role == "MEDIA" else summary | {"source_ref_id", "latitude", "longitude", "attributes"}
    assert all(set(row) == fields for row in payload["data"])
    reads[role.lower().replace("_", "-")] = {"count": payload["count"], "field_count": len(fields)}

traces = {}
for service in ("client-api", "auth", "aggregator"):
    records = []
    for line in run("logs", "--no-log-prefix", "--tail", "200", service).splitlines():
        try:
            record = json.loads(line)
        except json.JSONDecodeError:
            continue
        if record.get("correlation_id") in query_ids and isinstance(record.get("latency_ms"), (int, float)):
            records.append({key: record[key] for key in ("correlation_id", "latency_ms")})
    assert set(query_ids) <= {record["correlation_id"] for record in records}, service + " is missing an HTTP trace"
    traces[service] = records

evidence = {"result": "PASS", "checked_at": now(), "scope": "DEFAULT_COMPOSE",
            "services": [{"service": state["Service"], "health": state["Health"]} for state in states],
            "aggregator_host_port": False, "canonical_host_port": False, "protected_reads": reads,
            "http_traces": traces}
if args.output:
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(redact(json.dumps(evidence, indent=2)) + "\n")
print(json.dumps(evidence, indent=2))
