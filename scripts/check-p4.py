#!/usr/bin/env python3
"""P4 demo: independent service rebuild, additive JSONB fields, and store ownership.

Uses .env.example, preserves volumes, leaves PVMBG in schema v2 for review.
"""
import json
from pathlib import Path
import subprocess
import urllib.error
import urllib.parse
import urllib.request
from demo_support import compose, healthy, now, run, settings, sql, wait_for

folder = Path("docs/evidence/anggota-c/stage-4")
folder.mkdir(parents=True, exist_ok=True)


def request(port, endpoint, body=None, headers=None):
    headers = dict(headers or {})
    headers["X-Correlation-ID"] = "p4-review-" + str(port)
    if body is not None:
        headers["Content-Type"] = "application/json"
    request = urllib.request.Request("http://127.0.0.1:" + str(port) + endpoint,
        data=None if body is None else json.dumps(body).encode(), headers=headers)
    with urllib.request.urlopen(request, timeout=5) as response:
        return {"status": response.status, "correlation_id": response.headers.get("X-Correlation-ID"),
                "body": json.load(response)}


def states():
    result = {}
    for line in run("ps", "--all", "--format", "{{.Service}} {{.ID}}").splitlines():
        service, identity = line.split()
        state = json.loads(subprocess.check_output(["docker", "inspect", "--format", "{{json .State}}", identity], text=True))
        result[service] = {"id": identity, "running": state["Running"], "started_at": state["StartedAt"]}
    return result


def migrations():
    return json.loads(sql("SELECT COALESCE(json_agg(t ORDER BY version),'[]'::json) FROM schema_migrations t"))


def schema(version):
    return request(settings["PVMBG_PORT"], "/admin/schema-version", {"version": version},
                   {"Authorization": "Bearer " + settings["PVMBG_TOKEN"]})


def snapshots(ids):
    # hazard IDs are obtained from our database, not user input; SQL quote for completeness.
    quoted = ",".join("'" + identity.replace("'", "''") + "'" for identity in ids)
    return json.loads(sql("SELECT json_agg(t ORDER BY hazard_id) FROM (SELECT * FROM hazard_events WHERE hazard_id IN (" + quoted + ")) t"))


def read_together(ids):
    response = request(settings["AGGREGATOR_PORT"], "/internal/v1/hazards?source=PVMBG&limit=1000")
    records = [r for r in response["body"]["data"] if r["hazard_id"] in ids]
    assert len(records) == 2, "v1/v2 must be readable together through Aggregator"
    return {"status": response["status"], "correlation_id": response["correlation_id"],
            "records": sorted(records, key=lambda r: r["hazard_id"])}


evidence = {"started_at": now(), "states_before": states()}
assert all(healthy(s) for s in ("aggregator", "notification-consumer", "dashboard-consumer"))
evidence["stop_requested_at"] = now()
try:
    run("stop", "pvmbg")
    evidence["states_while_stopped"] = states()
    assert not evidence["states_while_stopped"]["pvmbg"]["running"]
    evidence["bmkg_while_pvmbg_stopped"] = request(settings["BMKG_PORT"], "/health")
    evidence["aggregator_while_pvmbg_stopped"] = request(settings["AGGREGATOR_PORT"], "/hazards?source=BMKG&limit=1")
    assert all(healthy(s) for s in ("notification-consumer", "dashboard-consumer"))
    rebuild = run("build", "pvmbg")
    (folder / "pvmbg-rebuild.log").write_text(rebuild + "\n")
finally:
    evidence["start_requested_at"] = now()
    run("up", "-d", "--no-deps", "pvmbg")
wait_for(lambda: request(settings["PVMBG_PORT"], "/health")["status"] == 200, "PVMBG should recover")
evidence["states_after_rebuild"] = states()
for name, state in evidence["states_before"].items():
    if name != "pvmbg" and state["running"]:
        assert evidence["states_after_rebuild"][name] == state, name + " must not restart during PVMBG rebuild"
# Capture new v1, enable v2 live, and capture confidence_level without changing the SQL schema.
evidence["migrations_before"] = migrations()
v1_since = now()
evidence["schema_v1_response"] = schema(1)
query_v1 = f"SELECT count(*)>0 FROM hazard_events WHERE source='PVMBG' AND ingested_at>'{v1_since}'::timestamptz AND NOT attributes ? 'confidence_level'"
wait_for(lambda: sql(query_v1) == "t", "fresh v1 event must persist")
v1 = sql(f"SELECT hazard_id FROM hazard_events WHERE source='PVMBG' AND ingested_at>'{v1_since}'::timestamptz AND NOT attributes ? 'confidence_level' ORDER BY ingested_at DESC LIMIT 1")
v2_since = now()
evidence["schema_v2_response"] = schema(2)
query_v2 = f"SELECT count(*)>0 FROM hazard_events WHERE source='PVMBG' AND ingested_at>'{v2_since}'::timestamptz AND attributes ? 'confidence_level'"
wait_for(lambda: sql(query_v2) == "t", "fresh v2 event must persist")
v2 = sql(f"SELECT hazard_id FROM hazard_events WHERE source='PVMBG' AND ingested_at>'{v2_since}'::timestamptz AND attributes ? 'confidence_level' ORDER BY ingested_at DESC LIMIT 1")
ids = [v1, v2]
evidence["record_ids"] = {"v1": v1, "v2": v2}
evidence["snapshots_before_restart"] = snapshots(ids)
evidence["api_before_restart"] = read_together(ids)
for record in evidence["snapshots_before_restart"]:
    assert ("confidence_level" in record["attributes"]) == (record["hazard_id"] == v2)
for service in ("aggregator", "canonical-store"):
    evidence["restart_" + service + "_at"] = now()
    run("restart", service)
    wait_for(lambda: healthy("aggregator"), "Aggregator should recover with persisted records")
    after = snapshots(ids)
    assert after == evidence["snapshots_before_restart"]
    evidence["snapshots_after_" + service] = after
    response = read_together(ids)
    assert response == evidence["api_before_restart"]
    evidence["api_after_" + service] = response
    assert migrations() == evidence["migrations_before"], "schema evolution/restarts must not add/reapply migrations"
evidence["migrations_after"] = migrations()
# Only names/memberships are recorded; never write environment credential values.
config = json.loads(run("config", "--format", "json"))
evidence["declared_services"] = sorted(config["services"])
evidence["ownership"] = {name: {"networks": sorted(service.get("networks", {})),
    "database_url_present": "DATABASE_URL" in service.get("environment", {}),
    "host_ports": service.get("ports", [])} for name, service in config["services"].items()}
assert [n for n, s in evidence["ownership"].items() if s["database_url_present"]] == ["aggregator"]
assert sorted(n for n, s in evidence["ownership"].items() if "storage" in s["networks"]) == ["aggregator", "canonical-store"]
assert not evidence["ownership"]["canonical-store"]["host_ports"]
probe_compose = compose + ["-f", "docker-compose.yml", "-f", "tests/compose-probes.yml"]
evidence["network_probes"] = {}
for role in ("aggregator", "client", "consumer"):
    probe = subprocess.run(probe_compose + ["run", "--rm", "--no-deps", "probe-" + role], text=True,
                           stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    expected = 0 if role == "aggregator" else 2
    assert probe.returncode == expected, role + " network access did not match ownership"
    evidence["network_probes"][role] = {"returncode": probe.returncode, "output": probe.stdout.strip()}
evidence["client_api_request"] = {"status": "PENDING_B", "reason": "Client API and Auth main functions remain empty; probes prove network role isolation, not the Client API HTTP implementation."}
evidence["finished_at"] = now()
evidence["result"] = "PASS_C_SCOPE; CLIENT_API_PENDING_B"
(folder / "p4-check.json").write_text(json.dumps(evidence, indent=2) + "\n")
print("PASS: independent rebuild; v1/v2 JSONB survive restarts without migration; network ownership verified")
print("PENDING B: Client API -> Aggregator HTTP request")
print(folder / "p4-check.json")
