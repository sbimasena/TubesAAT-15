#!/usr/bin/env python3
"""Verify runtime schema evolution, fanout and source freshness without restarting services.

Requires the seven A/C development services using .env.example. Leaves PVMBG at
schema v1 and outage=false. Does not delete volumes, queues, or canonical data.
"""
import argparse
from datetime import datetime
import json
import subprocess
import time
from pathlib import Path
import urllib.request

from demo_support import journal, now, run, settings, wait_for

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--output", default="/tmp/tubesaat-ingestion-check.json")
args = parser.parse_args()

def development_states():
    result = {}
    for line in run("ps", "--all", "--format", "json").splitlines():
        entry = json.loads(line)
        container = json.loads(subprocess.check_output(["docker", "inspect", entry["ID"]], text=True))[0]
        # Compose test runners may share a Service name with a long-running container.
        if container["Config"]["Labels"].get("com.docker.compose.oneoff", "false").lower() == "true":
            continue
        state = container["State"]
        result[entry["Service"]] = {"id": entry["ID"], "running": state["Running"],
                                    "started_at": state["StartedAt"]}
    return result


evidence = {"started_at": now(), "states_before": development_states()}


def request(port, path, body=None, token=None):
    headers = {"X-Correlation-ID": "member-a-ingestion-check"}
    if token:
        headers["Authorization"] = "Bearer " + token
    if body is not None:
        headers["Content-Type"] = "application/json"
    req = urllib.request.Request(
        "http://127.0.0.1:" + port + path,
        data=None if body is None else json.dumps(body).encode(), headers=headers,
    )
    with urllib.request.urlopen(req, timeout=8) as response:
        assert response.status == 200
        return json.load(response)


def admin(path, body):
    return request(settings["PVMBG_PORT"], "/admin/" + path, body, settings["PVMBG_TOKEN"])


def health():
    return request(settings["AGGREGATOR_PORT"], "/health")


def hazards():
    return request(settings["AGGREGATOR_PORT"], "/internal/v1/hazards?source=PVMBG&limit=1000")


def canonical(reference):
    return next((event for event in hazards()["data"] if event["source_ref_id"] == reference), None)


def same_hazard(left, right):
    # PostgreSQL JSON snapshots and Go API output use equivalent ISO-8601 forms.
    left, right = dict(left), dict(right)
    for event in (left, right):
        for field in ("occurred_at", "ingested_at"):
            event[field] = datetime.fromisoformat(event[field].replace("Z", "+00:00"))
    return left == right


def ready():
    response = health()
    return response["storage"]["available"] and all(
        status["available"] and not status["stale"] for status in response["sources"].values()
    )


try:
    wait_for(ready, "both sources should complete fresh ingestion before the check")
    admin("schema-version", {"version": 1})
    raw = request(settings["PVMBG_PORT"], "/volcanic-reports", token=settings["PVMBG_TOKEN"])
    old = next((report for report in raw if "confidence_level" not in report), None)
    assert old, "use a development catalog with at least one v1 report"
    wait_for(lambda: canonical(old["report_id"]) is not None, "v1 report should be persisted")
    old_hazard = canonical(old["report_id"])
    assert "confidence_level" not in old_hazard["attributes"]
    evidence["v1_hazard"] = old_hazard

    version = admin("schema-version", {"version": 2})
    assert version["confidence_level_enabled"] and version["schema_version"] == 2
    new_id = version["report_id"]
    wait_for(lambda: canonical(new_id) is not None, "v2 report should be ingested without restart")
    new_hazard = canonical(new_id)
    raw_v2 = next(report for report in request(settings["PVMBG_PORT"], "/volcanic-reports",
                                             token=settings["PVMBG_TOKEN"]) if report["report_id"] == new_id)
    confidence = new_hazard["attributes"]["confidence_level"]
    assert isinstance(confidence, (float, int)) and confidence == raw_v2["confidence_level"]
    assert new_hazard["source"] == "PVMBG" and new_hazard["hazard_type"] == "VOLCANIC"
    assert "confidence_level" not in canonical(old["report_id"])["attributes"]
    evidence["v2_hazard"] = new_hazard

    evidence["consumers"] = {}
    for service in ("notification-consumer", "dashboard-consumer"):
        def delivered():
            rows = [row for row in journal(service) if row["payload"]["source_ref_id"] == new_id]
            if rows:
                assert len(rows) == 1, "consumer journal must deduplicate message IDs"
                assert same_hazard(rows[0]["payload"], new_hazard)
                evidence["consumers"][service] = rows[0]
                return True
            return False
        wait_for(delivered, service + " should receive the canonical v2 snapshot")
    notification = evidence["consumers"]["notification-consumer"]
    dashboard = evidence["consumers"]["dashboard-consumer"]
    assert notification["message_id"] == dashboard["message_id"]
    assert notification["correlation_id"] == dashboard["correlation_id"]

    before_outage = health()["sources"]["PVMBG"]["last_ingested_at"]
    admin("outage", {"enabled": True})

    def degraded():
        response = health()
        pvmbg, bmkg = response["sources"]["PVMBG"], response["sources"]["BMKG"]
        if not pvmbg["available"] and pvmbg["stale"]:
            assert pvmbg["stale_since"] and pvmbg["last_error"]
            assert pvmbg["last_ingested_at"] >= before_outage
            assert bmkg["available"] and not bmkg["stale"]
            evidence["outage_health"] = response
            return True
        return False

    wait_for(degraded, "PVMBG outage should mark only PVMBG stale")
    # A successful fetch already in flight when outage is enabled may still commit.
    # Once a failed poll is observed, subsequent failed polls must not advance ingestion.
    time.sleep(float(settings["POLL_INTERVAL_SECONDS"]) + 1)
    repeated = health()
    failed_status = evidence["outage_health"]["sources"]["PVMBG"]
    repeated_status = repeated["sources"]["PVMBG"]
    assert not repeated_status["available"] and repeated_status["stale"]
    assert repeated_status["last_ingested_at"] == failed_status["last_ingested_at"]
    assert repeated_status["stale_since"] == failed_status["stale_since"]
    evidence["repeated_outage_health"] = repeated
    assert canonical(new_id) == new_hazard, "stored volcanic data must remain readable during outage"
    admin("outage", {"enabled": False})
    wait_for(ready, "PVMBG should become fresh after a successful committed retry")
    evidence["recovered_health"] = health()
    recovered = evidence["recovered_health"]["sources"]["PVMBG"]
    assert recovered["last_ingested_at"] > evidence["outage_health"]["sources"]["PVMBG"]["last_ingested_at"]
    assert "stale_since" not in recovered and "last_error" not in recovered
    evidence["result"] = "PASS"
except Exception as error:
    evidence["result"] = "FAIL"
    evidence["failure"] = str(error)
    raise
finally:
    try:
        admin("outage", {"enabled": False})
        admin("schema-version", {"version": 1})
        evidence["states_after"] = development_states()
        assert evidence["states_after"] == evidence["states_before"], "services must not restart during this check"
    except Exception as error:
        evidence["result"] = "FAIL"
        evidence["cleanup_failure"] = str(error)
        raise
    finally:
        evidence["finished_at"] = now()
        output = Path(args.output)
        output.parent.mkdir(parents=True, exist_ok=True)
        output.write_text(json.dumps(evidence, indent=2) + "\n")

print("PASS: runtime v1/v2 mapping, both consumers, source outage/freshness and recovery without restart")
print(args.output)
