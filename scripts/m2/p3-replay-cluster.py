#!/usr/bin/env python3
"""P3.4: inventory PVMBG DLQ items and verify replay ordering via the Audit API.

The default mode is read-only inventory. Replay requires --replay plus explicit
message IDs; there is no replay-all switch. Ordering is checked by comparing
canonical Audit event snapshots before and after: per source_ref_id the
occurred_at must not decrease and the hazard_id must stay stable.
"""
import argparse
import datetime
import secrets
import sys
import time
import urllib.parse
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import verify_common as common


ROOT = Path(__file__).resolve().parents[2]
MAX_REPLAY_BATCH = 32


def require(condition, message):
    if not condition:
        raise AssertionError(message)


def validate_message_ids(message_ids):
    for message_id in message_ids:
        require(isinstance(message_id, str) and common.MESSAGE_ID_RE.match(message_id),
                "invalid PVMBG message_id: " + repr(message_id))
    return list(dict.fromkeys(message_ids))


def index_events(events):
    by_ref, hazards, violations = {}, set(), []
    for event in events:
        reference = event.get("source_ref_id")
        hazard_id = event.get("hazard_id")
        if not reference or not hazard_id:
            violations.append({"violation": "event_missing_identity", "event": event})
            continue
        if reference in by_ref:
            violations.append({"source_ref_id": reference, "violation": "duplicate_source_ref_id"})
        if hazard_id in hazards:
            violations.append({"hazard_id": hazard_id, "violation": "duplicate_hazard_id"})
        by_ref[reference] = event
        hazards.add(hazard_id)
    return by_ref, violations


def verify_ordering(before, after):
    before_by_ref, violations = index_events(before)
    after_by_ref, after_violations = index_events(after)
    violations.extend(after_violations)
    new_events = []
    for reference, old in before_by_ref.items():
        new = after_by_ref.get(reference)
        if new is None:
            violations.append({"source_ref_id": reference, "violation": "event_disappeared"})
            continue
        if new.get("hazard_id") != old.get("hazard_id"):
            violations.append({"source_ref_id": reference, "violation": "hazard_id_changed",
                               "before": old.get("hazard_id"), "after": new.get("hazard_id")})
        old_time = common.parse_timestamp(old.get("occurred_at"))
        new_time = common.parse_timestamp(new.get("occurred_at"))
        if old_time is None or new_time is None:
            violations.append({"source_ref_id": reference, "violation": "invalid_occurred_at"})
        elif new_time < old_time:
            violations.append({"source_ref_id": reference, "violation": "occurred_at_decreased",
                               "before": old.get("occurred_at"), "after": new.get("occurred_at")})
    for reference, new in after_by_ref.items():
        if reference not in before_by_ref:
            new_events.append({"source_ref_id": reference, "hazard_id": new.get("hazard_id")})
    return {"violations": violations, "new_events": new_events}


def inventory(base, token, source, limit):
    items, cursor, seen = [], "", set()
    while True:
        query = {"source": source, "limit": str(limit)}
        if cursor:
            query["cursor"] = cursor
        response = common.http_json(base, "/internal/audit/unprocessed?" + urllib.parse.urlencode(query),
                                    token=token)
        require(response["status"] == 200, "DLQ inventory failed: " + str(response["status"]))
        page = response["body"] or {}
        items.extend(page.get("items", []))
        cursor = page.get("next_cursor") or ""
        if not cursor:
            break
        require(cursor not in seen, "DLQ pagination repeated a cursor")
        seen.add(cursor)
    return items


def audit_events(base, token, source, limit=1000):
    items, cursor, seen = [], "", set()
    while True:
        query = {"source": source, "limit": str(limit)}
        if cursor:
            query["cursor"] = cursor
        response = common.http_json(base, "/internal/audit/events?" + urllib.parse.urlencode(query),
                                    token=token)
        require(response["status"] == 200, "Audit events query failed: " + str(response["status"]))
        page = response["body"] or {}
        require(not page.get("partial"), "Audit events response is partial; refusing to verify ordering")
        items.extend(page.get("items", []))
        cursor = page.get("next_cursor") or ""
        if not cursor:
            break
        require(cursor not in seen, "Audit events pagination repeated a cursor")
        seen.add(cursor)
    return items


def summarize_inventory(items):
    by_status, by_reason = {}, {}
    for item in items:
        status = str(item.get("status"))
        reason = str(item.get("reason"))
        by_status[status] = by_status.get(status, 0) + 1
        by_reason[reason] = by_reason.get(reason, 0) + 1
    return {"total": len(items), "by_status": by_status, "by_reason": by_reason}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--audit-url", required=True, help="Aggregator Audit API base URL")
    parser.add_argument("--audit-token-env", default="AUDIT_TOKEN")
    parser.add_argument("--audit-token-file")
    parser.add_argument("--source", default="PVMBG", choices=("PVMBG",))
    parser.add_argument("--limit", type=int, default=200, help="Inventory page size (1..1000)")
    parser.add_argument("--replay", action="store_true", help="Replay explicitly listed message IDs")
    parser.add_argument("--message-ids", help="Comma-separated message IDs to replay")
    parser.add_argument("--message-id-file", help="File with one message ID per line or a JSON array")
    parser.add_argument("--timeout", type=int, default=120, help="Recovery wait in seconds")
    parser.add_argument("--poll-seconds", type=float, default=2.0)
    parser.add_argument("--output-dir", type=Path, help="Evidence directory")
    args = parser.parse_args()
    if not 1 <= args.limit <= 1000:
        parser.error("--limit must be between 1 and 1000")

    token_env = None if args.audit_token_file else args.audit_token_env
    token, token_source = common.resolve_token(token_env, args.audit_token_file, "Audit token")
    requested = common.parse_csv_ids(args.message_ids)
    if args.message_id_file:
        requested.extend(common.parse_id_file(args.message_id_file))
    requested = validate_message_ids(requested) if requested else []
    if args.replay:
        require(requested, "replay requires explicit --message-ids or --message-id-file")
    elif requested:
        parser.error("message IDs are only used together with --replay")

    stamp = datetime.datetime.now(common.UTC).strftime("%Y%m%dT%H%M%SZ")
    folder = (args.output_dir or ROOT / "artifacts/m2/runs" /
              (stamp + "-p3-replay-" + secrets.token_hex(3))).resolve()
    folder.mkdir(parents=True, exist_ok=False)
    base = args.audit_url.rstrip("/")

    inventory_before = inventory(base, token, args.source, args.limit)
    events_before = audit_events(base, token, args.source)
    common.write_json(folder / "inventory-before.json", inventory_before)
    common.write_json(folder / "events-before.json", events_before)

    report = {
        "timestamp": common.isoformat(),
        "audit_url": base,
        "source": args.source,
        "token_source": token_source,
        "mode": "replay" if args.replay else "inventory",
        "inventory_before": summarize_inventory(inventory_before),
        "events_before": len(events_before),
        "requested": requested,
    }

    if not args.replay:
        report.update(result="INVENTORY_ONLY", reasons=[])
        common.write_json(folder / "report.json", report)
        print("\nEvidence:", folder)
        print("DLQ inventory:", report["inventory_before"])
        print("Canonical events:", len(events_before))
        print("RESULT: INVENTORY_ONLY (no mutation; pass --replay with explicit message IDs to replay)")
        return common.EXIT_CODES["INVENTORY_ONLY"]

    known = {item["message_id"]: item for item in inventory_before}
    for message_id in requested:
        require(message_id in known, "message_id is not present in the current DLQ inventory: " + message_id)
        require(known[message_id].get("status") in ("parked", "retrying"),
                "message_id is not replayable in its current status: " + message_id)

    requests = []
    for batch in common.chunked(requested, MAX_REPLAY_BATCH):
        response = common.http_json(base, "/internal/audit/unprocessed/replay", token=token,
                                    method="POST", body={"message_ids": batch})
        requests.append({"message_ids": batch, "status": response["status"], "error": response["error"]})
        require(response["status"] == 202, "replay request was not accepted: " + str(response["status"]))
    common.write_json(folder / "replay-requests.json", requests)

    deadline = time.monotonic() + max(0, args.timeout)
    inventory_after, remaining = inventory(base, token, args.source, args.limit), list(requested)
    while remaining and time.monotonic() < deadline:
        time.sleep(max(0.5, args.poll_seconds))
        inventory_after = inventory(base, token, args.source, args.limit)
        present = {item["message_id"] for item in inventory_after}
        remaining = [message_id for message_id in requested if message_id in present]
    events_after = audit_events(base, token, args.source)
    ordering = verify_ordering(events_before, events_after)
    recovered = [message_id for message_id in requested if message_id not in remaining]
    common.write_json(folder / "inventory-after.json", inventory_after)
    common.write_json(folder / "events-after.json", events_after)
    common.write_json(folder / "ordering.json", ordering)

    if ordering["violations"]:
        result = "FAIL"
        reasons = [f"{len(ordering['violations'])} ordering violation(s)"]
    elif remaining and recovered:
        result = "PARTIAL"
        reasons = [f"{len(remaining)} message(s) still listed after the wait"]
    elif remaining:
        result = "FAIL"
        reasons = ["no requested message left the active DLQ set"]
    else:
        result = "PASS"
        reasons = []

    report.update({
        "inventory_after": summarize_inventory(inventory_after),
        "events_after": len(events_after),
        "recovered": recovered,
        "remaining": remaining,
        "ordering": ordering,
        "result": result,
        "reasons": reasons,
    })
    common.write_json(folder / "report.json", report)

    print("\nEvidence:", folder)
    print("DLQ before:", report["inventory_before"], "after:", report["inventory_after"])
    print("Recovered:", len(recovered), "remaining:", len(remaining))
    print("Ordering violations:", len(ordering["violations"]), "new events:", len(ordering["new_events"]))
    print("RESULT:", result, "; ".join(reasons))
    return common.EXIT_CODES[result]


if __name__ == "__main__":
    sys.exit(main())
