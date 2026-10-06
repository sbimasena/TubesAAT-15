#!/usr/bin/env python3
"""P5: attach an independent temporary AMQP subscriber to the live exchange."""
import hashlib
import json
from pathlib import Path
from demo_support import options, broker, compose, healthy, logs, now, received, run, sql, wait_for
import subprocess

review_compose = compose + ["-f", "tests/compose-review.yml"]


def review(*args):
    return subprocess.check_output(review_compose + list(args), text=True, stderr=subprocess.STDOUT).strip()


def producer_hashes():
    return {str(path): hashlib.sha256(path.read_bytes()).hexdigest()
            for path in sorted(Path("services/aggregator").rglob("*")) if path.is_file()}


def review_logs():
    return [json.loads(line) for line in review("logs", "--no-log-prefix", "review-subscriber").splitlines()
            if line.startswith("{")]


evidence = {"started_at": now(), "producer_commit_before": subprocess.check_output(
    ["git", "log", "-1", "--format=%H", "--", "services/aggregator"], text=True).strip(),
    "producer_hashes_before": producer_hashes(), "aggregator_id_before": run("ps", "-q", "aggregator")}
assert all(healthy(s) for s in ("aggregator", "notification-consumer", "dashboard-consumer"))
try:
    build_output = review("up", "--build", "-d", "--no-deps", "review-subscriber")
    wait_for(lambda: any(r.get("msg") == "review subscribed" for r in review_logs()), "review subscriber must bind")
    subscribed = next(r for r in review_logs() if r.get("msg") == "review subscribed")
    evidence["subscription"] = subscribed
    queue = broker("queues/%2F/" + subscribed["queue"])
    evidence["queue"] = {k: queue[k] for k in ("name", "durable", "auto_delete", "exclusive")}
    evidence["bindings"] = [{k: b[k] for k in ("source", "destination", "destination_type", "routing_key")}
                            for b in broker("exchanges/%2F/hazard.events/bindings/source")]
    # Match a real committed event created after subscription, rather than reusing history.
    from_time = subscribed["time"]
    wait_for(lambda: sql(f"SELECT count(*)>0 FROM hazard_outbox WHERE created_at > '{from_time}'::timestamptz AND published_at IS NOT NULL") == "t",
             "new producer event must be published after subscription")
    sample = json.loads(sql(f"SELECT row_to_json(t) FROM (SELECT message_id,hazard_id,hazard_revision,correlation_id,payload,created_at,published_at FROM hazard_outbox WHERE created_at > '{from_time}'::timestamptz AND published_at IS NOT NULL ORDER BY message_id LIMIT 1) t"))
    evidence["producer_sample"] = sample
    mid = str(sample["message_id"])
    wait_for(lambda: any(r.get("message_id") == mid for r in review_logs()), "third subscriber must receive the new event")
    third = next(r for r in review_logs() if r.get("message_id") == mid)
    evidence["third_consumer"] = third
    for service in ("notification-consumer", "dashboard-consumer"):
        wait_for(lambda: bool(received(service, mid)), service + " must receive the same event")
    evidence["base_consumers"] = {s: received(s, mid)[0] for s in ("notification-consumer", "dashboard-consumer")}
    for record in [third, *evidence["base_consumers"].values()]:
        assert record["hazard_id"] == sample["hazard_id"] and record["correlation_id"] == sample["correlation_id"]
        assert record["payload"] == sample["payload"]
    assert all(r["hazard_revision"] == sample["hazard_revision"] for r in evidence["base_consumers"].values())
    assert third["headers"]["hazard_revision"] == sample["hazard_revision"]
    assert third["headers"]["X-Correlation-ID"] == sample["correlation_id"]
    evidence["producer_logs"] = [r for r in logs("aggregator", evidence["started_at"]) if r.get("message_id") == sample["message_id"]]
    assert any(r.get("msg") == "outbox publish confirmed" for r in evidence["producer_logs"])
    evidence["producer_hashes_after"] = producer_hashes()
    evidence["producer_commit_after"] = subprocess.check_output(["git", "log", "-1", "--format=%H", "--", "services/aggregator"], text=True).strip()
    evidence["aggregator_id_after"] = run("ps", "-q", "aggregator")
    assert evidence["producer_hashes_before"] == evidence["producer_hashes_after"]
    assert evidence["producer_commit_before"] == evidence["producer_commit_after"]
    assert evidence["aggregator_id_before"] == evidence["aggregator_id_after"]
    folder = Path(options.evidence_dir or "docs/evidence/anggota-c/stage-4")
    folder.mkdir(parents=True, exist_ok=True)
    (folder / "review-build.log").write_text(build_output + "\n")
finally:
    # Only the demo container is removed; its exclusive queue disappears on disconnect.
    review("rm", "-s", "-f", "review-subscriber")
wait_for(lambda: not any(q["name"] == subscribed["queue"] for q in broker("queues/%2F")), "temporary queue must disappear")
evidence["temporary_queue_removed"] = True
evidence["finished_at"] = now()
evidence["result"] = "PASS"
(folder / "third-consumer.json").write_text(json.dumps(evidence, indent=2) + "\n")
print("PASS: the same new producer event reaches three independent consumers; producer files/commit/container unchanged")
print(folder / "third-consumer.json")
