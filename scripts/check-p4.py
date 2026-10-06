#!/usr/bin/env python3
"""P4 demo: independent service rebuild, additive JSONB fields, and store ownership.

Uses the selected local environment, preserves volumes, leaves PVMBG in schema v2 for review.
"""
import json
from pathlib import Path
import subprocess
from demo_support import (options, compose, healthy, now, run, settings, sql, states, wait_for,
                          http_call, client_read, login, sessions, logs, redact)

folder = Path(options.evidence_dir or "docs/evidence/anggota-c/stage-4")
folder.mkdir(parents=True, exist_ok=True)


def request(port, endpoint, body=None, headers=None):
    token = (headers or {}).get("Authorization", "").removeprefix("Bearer ") or None
    response = http_call(port, endpoint, body, token)
    assert response["status"] == 200
    return response


def migrations():
    return json.loads(sql("SELECT COALESCE(json_agg(t ORDER BY version),'[]'::json) FROM schema_migrations t"))


def schema(version):
    return request(settings["PVMBG_PORT"], "/admin/schema-version", {"version": version},
                   {"Authorization": "Bearer " + settings["PVMBG_TOKEN"]})


def snapshots(ids):
    # hazard IDs are obtained from our database, not user input; SQL quote for completeness.
    quoted = ",".join("'" + identity.replace("'", "''") + "'" for identity in ids)
    return json.loads(sql("SELECT json_agg(t ORDER BY hazard_id) FROM (SELECT * FROM hazard_events WHERE hazard_id IN (" + quoted + ")) t"))


query_ids = []
summary_fields = {"hazard_id", "source", "hazard_type", "severity", "area_name", "occurred_at", "ingested_at"}


def read_together(ids):
    response = request(settings["AGGREGATOR_PORT"], "/internal/v1/hazards?source=PVMBG&limit=1000")
    records = [r for r in response["body"]["data"] if r["hazard_id"] in ids]
    assert len(records) == 2, "v1/v2 must be readable together through Aggregator"
    records = sorted(records, key=lambda r: r["hazard_id"])
    stored = {r["hazard_id"]: r for r in snapshots(ids)}
    assert all(r["attributes"] == stored[r["hazard_id"]]["attributes"] for r in records)
    result = {"aggregator": {"status": response["status"], "records": records}}
    for role in ("FIELD_TEAM", "INTERNAL_OPS", "MEDIA"):
        response = client_read("/hazards?source=PVMBG&limit=1000", role)
        assert response["status"] == 200
        selected = sorted((r for r in response["body"]["data"] if r["hazard_id"] in ids), key=lambda r: r["hazard_id"])
        expected = [{k: v for k, v in r.items() if k in summary_fields} for r in records] if role == "MEDIA" else records
        assert selected == expected, role + " must preserve both canonical snapshots"
        query_ids.append(response["correlation_id"])
        result[role] = {"status": response["status"], "correlation_id": response["correlation_id"], "records": selected}
    assert client_read("/hazards?include_raw=true", "MEDIA")["status"] == 403
    return result


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
    assert all(response[role]["records"] == evidence["api_before_restart"][role]["records"] for role in response)
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
probe_compose = compose + ["-f", "tests/compose-probes.yml"]
evidence["network_probes"] = {}
for role in ("aggregator", "client", "consumer"):
    probe = subprocess.run(probe_compose + ["run", "--rm", "--no-deps", "probe-" + role], text=True,
                           stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    expected = 0 if role == "aggregator" else 2
    assert probe.returncode == expected, role + " network access did not match ownership"
    evidence["network_probes"][role] = {"returncode": probe.returncode, "output": probe.stdout.strip()}
# Restart downstream services individually; Auth loses sessions, Client API does not.
evidence["downstream_lifecycle"] = {}
for service in ("client-api", "auth"):
    token = login("FIELD_TEAM")
    before = states()
    try:
        run("stop", service)
        stopped = states()
        assert not stopped[service]["running"]
        assert all(stopped[name] == state for name, state in before.items() if name != service)
    finally:
        run("up", "-d", "--no-deps", "--wait", "--wait-timeout", "30", service)
    after = states()
    assert all(after[name] == state for name, state in before.items() if name != service)
    response = http_call(settings["CLIENT_API_PORT"], "/hazards?source=PVMBG&limit=1000", token=token)
    expected_status = 401 if service == "auth" else 200
    assert response["status"] == expected_status
    if service == "auth":
        sessions.clear()
    restored = read_together(ids)
    assert all(restored[role]["records"] == evidence["api_before_restart"][role]["records"] for role in restored)
    evidence["downstream_lifecycle"][service] = {"before": before, "after": after, "old_token_status": expected_status,
                                               "persistent_reads": restored}
evidence["http_traces"] = {}
for service in ("client-api", "auth", "aggregator"):
    records = [{"correlation_id": r["correlation_id"], "latency_ms": r["latency_ms"]}
               for r in logs(service, evidence["started_at"]) if r.get("correlation_id") in query_ids
               and isinstance(r.get("latency_ms"), (int, float))]
    assert set(query_ids) <= {r["correlation_id"] for r in records}, service + " must preserve HTTP correlation"
    evidence["http_traces"][service] = records
evidence["finished_at"] = now()
evidence["result"] = "PASS"
(folder / "p4-check.json").write_text(redact(json.dumps(evidence, indent=2)) + "\n")
print("PASS: v1/v2 persistence through three client scopes; storage isolation; independent downstream restarts; HTTP traces")
print(folder / "p4-check.json")
