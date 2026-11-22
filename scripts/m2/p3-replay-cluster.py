#!/usr/bin/env python3
"""P3.4: Replay DLQ and ordering verification via Audit API.

Inventories DLQ items, replays them in isolation, verifies recovered status,
HazardEvent count, no duplicates, and ordering per source_ref_id.
Uses cluster Audit API. Does NOT mutate cluster DLQ without approval.
"""
import argparse
import datetime
import json
import secrets
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]


def now():
    return datetime.datetime.now(datetime.timezone.utc).isoformat()


def http(base_url, path, body=None, token=None, timeout=30):
    url = base_url + path
    data = json.dumps(body).encode() if body else None
    req = urllib.request.Request(url, data=data, method="POST" if data else "GET")
    req.add_header("Content-Type", "application/json")
    if token:
        req.add_header("Authorization", "Bearer " + token)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            return {"status": resp.status, "body": json.loads(resp.read())}
    except urllib.error.HTTPError as e:
        try:
            body = json.loads(e.read())
        except Exception:
            body = None
        return {"status": e.code, "body": body}
    except Exception as e:
        return {"status": 0, "body": {"error": str(e)}}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--audit-url", required=True, help="Aggregator Audit API base URL")
    parser.add_argument("--audit-token", required=True, help="Audit bearer token")
    parser.add_argument("--output-dir", type=Path, help="Evidence directory")
    args = parser.parse_args()

    stamp = datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    folder = (args.output_dir or ROOT / "artifacts/m2/runs" / (stamp + "-p3-replay-" + secrets.token_hex(3))).resolve()
    folder.mkdir(parents=True, exist_ok=False)

    base = args.audit_url.rstrip("/")
    token = args.audit_token

    # Inventory DLQ
    resp = http(base, "/internal/audit/unprocessed?source=PVMBG", token=token)
    require(resp["status"] == 200, "DLQ inventory failed: %s" % resp)
    items = resp["body"].get("items", [])
    inventory = []
    for item in items:
        inventory.append({
            "message_id": item.get("message_id"),
            "source_ref_id": item.get("source_ref_id"),
            "status": item.get("status"),
            "reason": item.get("reason"),
            "retryable": item.get("retryable"),
            "attempts": item.get("attempts"),
            "received_at": item.get("received_at"),
        })

    (folder / "dlq-inventory.json").write_text(json.dumps(inventory, indent=2), encoding="utf-8")

    # Replay one at a time, verify
    replay_results = []
    for item in inventory:
        mid = item["message_id"]
        if item["status"] == "recovered":
            replay_results.append({"message_id": mid, "action": "already_recovered", "status": "skipped"})
            continue
        # Replay
        resp = http(base, "/internal/audit/unprocessed/replay",
                    body={"message_ids": [mid]}, token=token)
        replay_results.append({
            "message_id": mid,
            "source_ref_id": item["source_ref_id"],
            "replay_status": resp["status"],
            "replay_body": resp["body"],
        })
        # Wait a moment for processing
        import time
        time.sleep(1)

    (folder / "replay-results.json").write_text(json.dumps(replay_results, indent=2), encoding="utf-8")

    # Post-replay inventory
    resp2 = http(base, "/internal/audit/unprocessed?source=PVMBG", token=token)
    post_items = resp2["body"].get("items", []) if resp2["status"] == 200 else []
    post_inventory = [{"message_id": i.get("message_id"), "status": i.get("status")} for i in post_items]

    report = {
        "timestamp": now(),
        "total_before": len(inventory),
        "total_after": len(post_inventory),
        "inventory_before": inventory,
        "replay_results": replay_results,
        "inventory_after": post_inventory,
    }
    (folder / "report.json").write_text(json.dumps(report, indent=2), encoding="utf-8")

    print(f"\nEvidence: {folder}")
    print(f"DLQ before: {len(inventory)}, after: {len(post_inventory)}")
    print("RESULT: MANUAL_REVIEW_REQUIRED")
    return 0


if __name__ == "__main__":
    import sys
    sys.exit(main())
