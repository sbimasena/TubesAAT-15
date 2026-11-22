#!/usr/bin/env python3
"""P5.8: Fetch SLO and error budget values from Prometheus/Grafana.

Queries Prometheus for panel 13-16 values and cross-checks with notification
consumer journal.
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


def prom_query(prom_url, query):
    url = prom_url.rstrip("/") + "/api/v1/query?query=" + urllib.request.quote(query)
    try:
        with urllib.request.urlopen(url, timeout=15) as resp:
            data = json.loads(resp.read())
            if data.get("status") == "success":
                results = data.get("data", {}).get("result", [])
                if results:
                    return results[0].get("value", [None, None])[1]
            return None
    except Exception as e:
        return {"error": str(e)}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--prometheus-url", required=True, help="Prometheus base URL")
    parser.add_argument("--consumer-journal", help="Path to notification consumer journal")
    parser.add_argument("--output-dir", type=Path, help="Evidence directory")
    args = parser.parse_args()

    stamp = datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    folder = (args.output_dir or ROOT / "artifacts/m2/runs" / (stamp + "-p5-slo-fetch-" + secrets.token_hex(3))).resolve()
    folder.mkdir(parents=True, exist_ok=False)

    prom = args.prometheus_url.rstrip("/")

    queries = {
        "critical_acknowledged_5m": 'sum(increase(notification_pantai_critical_acknowledged_total[5m]))',
        "critical_age_count_5m": 'sum(increase(notification_pantai_critical_age_seconds_count[5m]))',
        "critical_age_within_5s_ratio": 'sum(rate(notification_pantai_critical_age_seconds_bucket{le="5"}[5m])) / (sum(rate(notification_pantai_critical_age_seconds_count[5m])) > 0)',
        "critical_age_p95": 'histogram_quantile(0.95, sum by (le) (rate(notification_pantai_critical_age_seconds_bucket[5m])))',
        "critical_age_p99": 'histogram_quantile(0.99, sum by (le) (rate(notification_pantai_critical_age_seconds_bucket[5m])))',
    }

    results = {}
    for name, query in queries.items():
        results[name] = prom_query(prom, query)

    # Cross-check with journal
    journal_check = {}
    if args.consumer_journal:
        import pathlib
        jp = pathlib.Path(args.consumer_journal)
        if jp.exists():
            records = []
            for line in jp.read_text().splitlines():
                if line.strip():
                    try:
                        records.append(json.loads(line))
                    except json.JSONDecodeError:
                        pass
            critical = [r for r in records if r.get("payload", {}).get("source") == "SENSOR_PANTAI"
                        and r.get("payload", {}).get("attributes", {}).get("critical") is True]
            journal_check = {
                "total_records": len(records),
                "critical_alerts": len(critical),
                "with_valid_age": sum(1 for r in critical if isinstance(r.get("event_age_ms"), (int, float)) and r["event_age_ms"] >= 0),
                "within_5s": sum(1 for r in critical if isinstance(r.get("event_age_ms"), (int, float)) and 0 <= r["event_age_ms"] <= 5000),
            }

    report = {
        "timestamp": now(),
        "prometheus_url": prom,
        "queries": queries,
        "results": results,
        "journal_cross_check": journal_check,
    }
    (folder / "report.json").write_text(json.dumps(report, indent=2), encoding="utf-8")

    print(f"\nEvidence: {folder}")
    print("Prometheus results:")
    for k, v in results.items():
        print(f"  {k}: {v}")
    if journal_check:
        print("Journal cross-check:", json.dumps(journal_check))
    print("RESULT: MANUAL_REVIEW_REQUIRED")
    return 0


if __name__ == "__main__":
    import sys
    sys.exit(main())
