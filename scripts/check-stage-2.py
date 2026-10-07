#!/usr/bin/env python3
#Stage 2 check: stop/start RabbitMQ, keep polling/querying, recover durable queues.
import json
from pathlib import Path
import subprocess
import time
import urllib.request

from demo_support import options, broker, now, run, settings, sql
from demo_support import progress, heartbeat


def queues():
    return [{key: queue[key] for key in ("name", "durable", "auto_delete", "type", "messages_ready", "consumers")}
            for name in ("notification", "dashboard")
            for queue in [broker("queues/%2F/" + name)]]


def probes():
    samples = []
    for name in ("notification", "dashboard"):
        messages = broker("queues/%2F/" + name + "/get", {
            "count": 1, "ackmode": "ack_requeue_true", "encoding": "auto", "truncate": 100000})
        assert len(messages) == 1, "expected durable queued message"
        message = messages[0]
        properties = message["properties"]
        payload = json.loads(message["payload"])
        samples.append({"queue": name, "message_id": properties["message_id"],
                        "correlation_id": properties["correlation_id"], "headers": properties["headers"],
                        "delivery_mode": properties["delivery_mode"], "payload": payload})
    return samples


progress("Checking durable backlog; both consumers must be offline")
evidence = {"started_at": now(), "aggregator_id_before": run("ps", "-q", "aggregator"),
            "queues_before": queues(), "queue_samples_before": probes()}
assert all(q["durable"] and not q["auto_delete"] and q["consumers"] == 0 for q in evidence["queues_before"])
left, right = evidence["queue_samples_before"]
assert {k: v for k, v in left.items() if k != "queue"} == {k: v for k, v in right.items() if k != "queue"}
baseline = int(sql("SELECT COALESCE(max(message_id),0) FROM hazard_outbox"))
evidence["stop_requested_at"] = now()
try:
    run("stop", "message-broker")
    evidence["broker_stopped_at"] = now()
    progress("Waiting for pending outbox records while broker is offline")
    for attempt in range(15):
        heartbeat("pending outbox during broker downtime", attempt)
        time.sleep(2)
        count = int(sql(f"SELECT count(*) FROM hazard_outbox WHERE published_at IS NULL AND message_id>{baseline}"))
        if count:
            break
    assert count > 0, "polling must persist new pending events during broker downtime"
    evidence["pending_new_during_downtime"] = count
    sample = json.loads(sql(f"SELECT row_to_json(t) FROM (SELECT message_id,hazard_id,correlation_id,published_at FROM hazard_outbox WHERE message_id>{baseline} AND published_at IS NULL ORDER BY message_id LIMIT 1) t"))
    evidence["outbox_sample_offline"] = sample
    for endpoint in ("health", "hazards?limit=1"):
        with urllib.request.urlopen("http://127.0.0.1:" + settings["AGGREGATOR_PORT"] + "/" + endpoint, timeout=5) as response:
            evidence["offline_" + endpoint.split("?")[0] + "_status"] = response.status
            assert response.status == 200
finally:
    evidence["start_requested_at"] = now()
    run("start", "message-broker")

published = "f"
progress("Waiting for broker recovery and pending publication")
for attempt in range(30):
    heartbeat("broker recovery and queued publication", attempt)
    time.sleep(2)
    try:
        evidence["queues_after"] = queues()
        published = sql(f"SELECT published_at IS NOT NULL FROM hazard_outbox WHERE message_id={sample['message_id']}")
        if published == "t" and all(after["messages_ready"] >= before["messages_ready"]
                                   for before, after in zip(evidence["queues_before"], evidence["queues_after"])):
            break
    except (OSError, KeyError, subprocess.CalledProcessError):
        # Management queue statistics are temporarily absent immediately after boot.
        continue
assert published == "t", "outbox should recover without restarting Aggregator"
evidence["recovered_at"] = now()
evidence["outbox_sample_recovered"] = json.loads(sql(f"SELECT row_to_json(t) FROM (SELECT message_id,hazard_id,correlation_id,published_at FROM hazard_outbox WHERE message_id={sample['message_id']}) t"))
evidence["queue_samples_after"] = probes()
assert evidence["queue_samples_before"] == evidence["queue_samples_after"], "persistent queued snapshots must survive broker restart"
for before, after in zip(evidence["queues_before"], evidence["queues_after"]):
    assert after["messages_ready"] >= before["messages_ready"], "durable backlog must survive restart"
evidence["aggregator_id_after"] = run("ps", "-q", "aggregator")
assert evidence["aggregator_id_before"] == evidence["aggregator_id_after"]
records = []
for line in run("logs", "--no-log-prefix", "--since", evidence["started_at"], "aggregator").splitlines():
    try:
        record = json.loads(line)
    except json.JSONDecodeError:
        continue
    if record.get("msg") in ("outbox waiting for broker", "outbox publish pending", "outbox publisher connected") or record.get("message_id") == sample["message_id"]:
        records.append(record)
evidence["publisher_logs"] = records
evidence["result"] = "PASS"
destination = Path(options.evidence_dir or "docs/evidence/anggota-c/stage-2") / "broker-recovery.json"
destination.parent.mkdir(parents=True, exist_ok=True)
destination.write_text(json.dumps(evidence, indent=2) + "\n")
progress("Result file saved")
print("PASS: pending during downtime; API 200; recovered without Aggregator restart; durable queued snapshots preserved")
print(destination)
