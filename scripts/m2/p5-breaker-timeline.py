#!/usr/bin/env python3
"""P5.4: Circuit breaker timeline, request rate, and recovery.

Polls Aggregator detail endpoint during Sensor Pantai outage and records
request rate, retry count, rejected requests, breaker state, Client API latency.
"""
import argparse
import datetime
import json
import secrets
import subprocess
import time
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]


def now():
    return datetime.datetime.now(datetime.timezone.utc).isoformat()


def http(base_url, path, token=None, timeout=10):
    url = base_url + path
    req = urllib.request.Request(url, method="GET")
    if token:
        req.add_header("Authorization", "Bearer " + token)
    try:
        start = time.time()
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            elapsed = (time.time() - start) * 1000
            body = json.loads(resp.read())
            return {"status": resp.status, "body": body, "latency_ms": round(elapsed, 2)}
    except Exception as e:
        elapsed = (time.time() - start) * 1000
        return {"status": 0, "body": {"error": str(e)}, "latency_ms": round(elapsed, 2)}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--aggregator-url", required=True, help="Aggregator base URL")
    parser.add_argument("--audit-token", required=True, help="Audit bearer token")
    parser.add_argument("--client-api-url", help="Client API base URL")
    parser.add_argument("--kubectl", default="kubectl", help="kubectl binary")
    parser.add_argument("--namespace", default="sistem-informasi-kebencanaan-bnpb", help="K8s namespace")
    parser.add_argument("--poll-interval", type=int, default=5, help="Poll interval seconds")
    parser.add_argument("--output-dir", type=Path, help="Evidence directory")
    args = parser.parse_args()

    stamp = datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    folder = (args.output_dir or ROOT / "artifacts/m2/runs" / (stamp + "-p5-breaker-timeline-" + secrets.token_hex(3))).resolve()
    folder.mkdir(parents=True, exist_ok=False)

    base = args.aggregator_url.rstrip("/")
    token = args.audit_token
    timeline = []

    def poll(label):
        detail = http(base, "/internal/v1/status", token=token)
        coastal = detail["body"].get("sources", {}).get("SENSOR_PANTAI", {})
        breaker = detail["body"].get("breaker") if "breaker" in str(detail["body"]) else None
        entry = {
            "timestamp": now(),
            "label": label,
            "status_http": detail["status"],
            "latency_ms": detail["latency_ms"],
            "coastal_available": coastal.get("available"),
            "coastal_stale": coastal.get("stale"),
            "stale_since": coastal.get("stale_since"),
            "ingestion_error": coastal.get("ingestion_error"),
            "breaker": breaker,
        }
        if args.client_api_url:
            ca = http(args.client_api_url, "/hazards?source=SENSOR_PANTAI&limit=5", token=token)
            entry["client_api_status"] = ca["status"]
            entry["client_api_latency_ms"] = ca["latency_ms"]
        timeline.append(entry)
        (folder / "timeline.json").write_text(json.dumps(timeline, indent=2), encoding="utf-8")
        print(f"[{label}] coastal={coastal.get('available')}/{coastal.get('stale')} breaker={breaker} latency={detail['latency_ms']}ms")
        return entry

    # Phase 1: Baseline
    print("Phase 1: Baseline (5 polls)")
    for i in range(5):
        poll("baseline")
        time.sleep(args.poll_interval)

    # Phase 2: Outage
    print("Phase 2: Scaling sensor-pantai to 0")
    subprocess.run([args.kubectl, "-n", args.namespace, "scale", "deployment", "sensor-pantai", "--replicas=0"], capture_output=True)
    print("Phase 2: Polling during outage (10 polls)")
    for i in range(10):
        poll("outage")
        time.sleep(args.poll_interval)

    # Phase 3: Recovery
    print("Phase 3: Scaling sensor-pantai back to 1")
    subprocess.run([args.kubectl, "-n", args.namespace, "scale", "deployment", "sensor-pantai", "--replicas=1"], capture_output=True)
    print("Phase 3: Polling during recovery (10 polls)")
    for i in range(10):
        poll("recovery")
        time.sleep(args.poll_interval)

    (folder / "timeline.json").write_text(json.dumps(timeline, indent=2), encoding="utf-8")
    print(f"\nEvidence: {folder}")
    print("RESULT: MANUAL_REVIEW_REQUIRED")
    return 0


if __name__ == "__main__":
    import sys
    sys.exit(main())
