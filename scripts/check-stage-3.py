#!/usr/bin/env python3
"""Stage 3: two journals, independent downtime, restart dedup and broker reconnect.

Uses the selected local environment. Keeps queues/volumes and restores stopped services.
"""
import argparse
import json
from pathlib import Path
from demo_support import options, broker, healthy, logs, now, received, run, sql, wait_for

services = ("notification-consumer", "dashboard-consumer")
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--output", type=Path, default=Path(options.evidence_dir or "docs/evidence/anggota-c/stage-3") / "consumer-check.json")
arguments = parser.parse_args()


def republish(sample):
    result = broker("exchanges/%2F/hazard.events/publish", {
        "routing_key": "", "payload": json.dumps(sample["payload"]), "payload_encoding": "string",
        "properties": {"delivery_mode": 2, "content_type": "application/json",
                       "message_id": str(sample["message_id"]), "correlation_id": sample["correlation_id"],
                       "headers": {"schema_version": 1, "hazard_revision": sample["hazard_revision"],
                                   "X-Correlation-ID": sample["correlation_id"]}}})
    assert result["routed"]


evidence = {"started_at": now(), "aggregator_id": run("ps", "-q", "aggregator"),
            "dashboard_id": run("ps", "-q", "dashboard-consumer")}
wait_for(lambda: all(healthy(s) for s in services), "consumers must become ready")
baseline = int(sql("SELECT COALESCE(max(message_id),0) FROM hazard_outbox"))
evidence["stop_notification_requested_at"] = now()
try:
    run("stop", "notification-consumer")
    evidence["notification_stopped_at"] = now()
    wait_for(lambda: sql(f"SELECT count(*)>0 FROM hazard_outbox WHERE message_id>{baseline} AND published_at IS NOT NULL") == "t",
             "producer must publish new events while notification is offline")
    sample = json.loads(sql(f"SELECT row_to_json(t) FROM (SELECT message_id,hazard_id,hazard_revision,correlation_id,payload,published_at FROM hazard_outbox WHERE message_id>{baseline} AND published_at IS NOT NULL ORDER BY message_id LIMIT 1) t"))
    evidence["producer_sample"] = sample
    wait_for(lambda: bool(received("dashboard-consumer", sample["message_id"])), "dashboard must receive during notification downtime")
    assert not received("notification-consumer", sample["message_id"])
    evidence["dashboard_during_downtime"] = received("dashboard-consumer", sample["message_id"])
    wait_for(lambda: broker("queues/%2F/notification").get("messages_ready", 0) > 0, "offline subscription must retain backlog")
    evidence["offline_notification_backlog"] = broker("queues/%2F/notification")["messages_ready"]
    assert healthy("dashboard-consumer")
    assert run("ps", "-q", "aggregator") == evidence["aggregator_id"]
    assert run("ps", "-q", "dashboard-consumer") == evidence["dashboard_id"]
finally:
    evidence["start_notification_requested_at"] = now()
    run("start", "notification-consumer")
wait_for(lambda: healthy("notification-consumer") and bool(received("notification-consumer", sample["message_id"])),
         "notification must receive the same retained event after restart")
evidence["notification_recovered_at"] = now()
evidence["consumer_samples"] = {s: received(s, sample["message_id"])[0] for s in services}
for record in evidence["consumer_samples"].values():
    assert record["hazard_id"] == sample["hazard_id"] and record["correlation_id"] == sample["correlation_id"]
    assert record["hazard_revision"] == sample["hazard_revision"] and record["payload"] == sample["payload"]
# Republish a real producer snapshot twice, with a consumer restart between attempts.
for attempt in range(2):
    if attempt:
        run("restart", *services)
        wait_for(lambda: all(healthy(s) for s in services), "consumers must restore journals")
    since = now()
    republish(sample)
    wait_for(lambda: all(any(r.get("result") == "duplicate" and r.get("message_id") == str(sample["message_id"])
                           for r in logs(s, since)) for s in services), "replayed IDs must be duplicates")
    evidence["duplicate_attempt_" + str(attempt + 1)] = {
        s: [r for r in logs(s, since) if r.get("message_id") == str(sample["message_id"])] for s in services}
    for s in services:
        assert len(received(s, sample["message_id"])) == 1, "one durable result per ID across restart"
# Connection recovery and health must reflect a broker outage, with volumes retained.
evidence["broker_stop_requested_at"] = now()
consumer_ids = {s: run("ps", "-q", s) for s in services}
try:
    run("stop", "message-broker")
    wait_for(lambda: all(not healthy(s) for s in services), "health must fail without active subscriptions")
    evidence["offline_consumer_health"] = {s: healthy(s) for s in services}
finally:
    evidence["broker_start_requested_at"] = now()
    run("start", "message-broker")
wait_for(lambda: all(healthy(s) for s in services), "consumers must reconnect without container restart")
evidence["broker_recovered_at"] = now()
assert consumer_ids == {s: run("ps", "-q", s) for s in services}
assert run("ps", "-q", "aggregator") == evidence["aggregator_id"]
evidence["consumer_ids_before_and_after_broker_restart"] = consumer_ids
evidence["journal_results_for_sample"] = {s: len(received(s, sample["message_id"])) for s in services}
evidence["producer_logs"] = [r for r in logs("aggregator", evidence["started_at"]) if r.get("message_id") == sample["message_id"]]
evidence["consumer_logs"] = {s: [r for r in logs(s, evidence["started_at"]) if r.get("message_id") == str(sample["message_id"]) or
                                  r.get("msg") in ("consumer retry pending", "consumer subscribed", "journal restored")]
                             for s in services}
evidence["result"] = "PASS"
destination = arguments.output
destination.parent.mkdir(parents=True, exist_ok=True)
destination.write_text(json.dumps(evidence, indent=2) + "\n")
print("PASS: two consumers; independent downtime/backlog; one journal result per ID across restart; broker reconnect")
print(destination)
