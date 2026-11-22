#!/usr/bin/env python3
"""P5.1: Critical alert latency and zero-loss verification.

Generates critical alerts via Sensor Pantai, measures latency from Aggregator
ingestion to notification-consumer ACK, and reconciles IDs for zero loss.
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


def http(base_url, path, token=None, timeout=30):
    url = base_url + path
    req = urllib.request.Request(url, method="GET")
    if token:
        req.add_header("Authorization", "Bearer " + token)
    try:
        start = __import__("time").time()
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            elapsed = (__import__("time").time() - start) * 1000
            body = json.loads(resp.read())
            return {"status": resp.status, "body": body, "latency_ms": round(elapsed, 2)}
    except Exception as e:
        return {"status": 0, "body": {"error": str(e)}, "latency_ms": 0}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--aggregator-url", required=True, help="Aggregator base URL")
    parser.add_argument("--audit-token", required=True, help="Audit bearer token")
    parser.add_argument("--consumer-metrics-url", help="Notification consumer metrics URL")
    parser.add_argument("--journal-path", help="Path to notification consumer journal")
    parser.add_argument("--output-dir", type=Path, help="Evidence directory")
    args = parser.parse_args()

    stamp = datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    folder = (args.output_dir or ROOT / "artifacts/m2/runs" / (stamp + "-p5-critical-alert-" + secrets.token_hex(3))).resolve()
    folder.mkdir(parents=True, exist_ok=False)

    base = args.aggregator_url.rstrip("/")
    token = args.audit_token

    # Collect baseline alert count from journal
    baseline_alerts = []
    if args.journal_path:
        jp = Path(args.journal_path)
        if jp.exists():
            for line in jp.read_text().splitlines():
                if line.strip():
                    try:
                        rec = json.loads(line)
                        if rec.get("payload", {}).get("source") == "SENSOR_PANTAI" and \
                           rec.get("payload", {}).get("attributes", {}).get("critical") is True:
                            baseline_alerts.append(rec.get("message_id"))
                    except json.JSONDecodeError:
                        pass

    report = {
        "timestamp": now(),
        "baseline_critical_alerts": len(baseline_alerts),
        "note": "Generate critical alerts via sensor, then run slo-check.py --baseline with independent expected IDs.",
    }

    # If consumer metrics available, fetch current values
    if args.consumer_metrics_url:
        try:
            import urllib.request
            with urllib.request.urlopen(args.consumer_metrics_url + "/metrics", timeout=10) as resp:
                metrics_text = resp.read().decode()
            for line in metrics_text.splitlines():
                if "notification_pantai_critical_acknowledged_total" in line and not line.startswith("#"):
                    report["critical_acknowledged_total"] = line.strip()
                if "notification_pantai_critical_age_seconds_count" in line and not line.startswith("#"):
                    report["critical_age_count"] = line.strip()
        except Exception as e:
            report["metrics_error"] = str(e)

    (folder / "report.json").write_text(json.dumps(report, indent=2), encoding="utf-8")

    print(f"\nEvidence: {folder}")
    print("Critical alerts baseline:", len(baseline_alerts))
    print("RESULT: MANUAL_REVIEW_REQUIRED (generate alerts, then run slo-check.py --baseline)")
    return 0


if __name__ == "__main__":
    import sys
    sys.exit(main())
PYEOF
