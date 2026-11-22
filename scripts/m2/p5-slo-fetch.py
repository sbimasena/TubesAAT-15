#!/usr/bin/env python3
"""P5.8: read the Grafana alert-path SLO panels 13-16 from Prometheus.

The queries are copied from the dashboard so the evidence matches what the
operator sees. A panel whose denominator is empty is reported as NO_DATA, not
as zero or PASS: without critical alerts the SLO cannot be evaluated.
"""
import argparse
import datetime
import secrets
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import verify_common as common


ROOT = Path(__file__).resolve().parents[2]
PANELS = {
    "ratio_within_5s": {
        "panel": 13,
        "query": 'sum(rate(notification_pantai_critical_age_seconds_bucket{le="5"}[5m])) / '
                 '(sum(rate(notification_pantai_critical_age_seconds_count[5m])) > 0)',
    },
    "p95_ack_seconds": {
        "panel": 14,
        "query": 'histogram_quantile(0.95, sum by (le) (rate(notification_pantai_critical_age_seconds_bucket[5m]))) '
                 'and (sum(rate(notification_pantai_critical_age_seconds_count[5m])) > 0)',
    },
    "error_budget_remaining_1h": {
        "panel": 15,
        "query": '1 - (1 - sum(increase(notification_pantai_critical_age_seconds_bucket{le="5"}[1h])) / '
                 '(sum(increase(notification_pantai_critical_age_seconds_count[1h])) > 0)) / 0.01',
    },
    "burn_rate_5m": {
        "panel": 16,
        "query": '(1 - sum(rate(notification_pantai_critical_age_seconds_bucket{le="5"}[5m])) / '
                 '(sum(rate(notification_pantai_critical_age_seconds_count[5m])) > 0)) / 0.01',
    },
}
SUPPORTING = {
    "acknowledged_total_5m": "sum(increase(notification_pantai_critical_acknowledged_total[5m]))",
    "age_samples_5m": "sum(increase(notification_pantai_critical_age_seconds_count[5m]))",
}


def evaluate_panels(results):
    """Return PASS/FAIL/BLOCKED from the four panel values."""
    required = list(PANELS)
    missing = [name for name in required
               if results.get(name, {}).get("state") != "VALUE"]
    if missing:
        return {"result": "BLOCKED", "reasons": ["no data for: " + ", ".join(sorted(missing))]}
    values = {name: results[name]["value"] for name in required}
    reasons = []
    if values["ratio_within_5s"] < 0.99:
        reasons.append("ratio <=5 s is below 0.99")
    if values["p95_ack_seconds"] > 5.0:
        reasons.append("p95 ACK age is above 5 s")
    if values["error_budget_remaining_1h"] < 0.0:
        reasons.append("1 h error budget is exhausted")
    if values["burn_rate_5m"] > 1.0:
        reasons.append("5 m burn rate is above 1")
    return {"result": "FAIL" if reasons else "PASS", "reasons": reasons, "values": values}


def journal_cross_check(path):
    records = common.read_jsonl(path)
    critical = common.critical_alerts(records)
    ages = [item["event_age_ms"] for item in critical
            if isinstance(item.get("event_age_ms"), (int, float)) and item["event_age_ms"] >= 0]
    return {
        "total_records": len(records),
        "critical_alerts": len(critical),
        "with_pre_append_age": len(ages),
        "within_5s_pre_append": sum(1 for age in ages if age <= 5000),
        "pre_append_p95_ms": round(common.percentile(ages, 0.95), 3) if ages else None,
        "note": "event_age_ms is measured before the journal append; the ACK SLO comes from Prometheus",
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--prometheus-url", required=True, help="Prometheus base URL")
    parser.add_argument("--consumer-journal", help="Notification consumer journal (optional cross-check)")
    parser.add_argument("--output-dir", type=Path, help="Evidence directory")
    args = parser.parse_args()

    stamp = datetime.datetime.now(common.UTC).strftime("%Y%m%dT%H%M%SZ")
    folder = (args.output_dir or ROOT / "artifacts/m2/runs" /
              (stamp + "-p5-slo-fetch-" + secrets.token_hex(3))).resolve()
    folder.mkdir(parents=True, exist_ok=False)

    results, supporting = {}, {}
    for name, panel in PANELS.items():
        results[name] = common.prom_query(args.prometheus_url, panel["query"])
    for name, query in SUPPORTING.items():
        supporting[name] = common.prom_query(args.prometheus_url, query)

    evaluation = evaluate_panels({name: value["result"] for name, value in results.items()})
    report = {
        "timestamp": common.isoformat(),
        "prometheus_url": args.prometheus_url,
        "panels": {name: {"panel": panel["panel"], "query": panel["query"],
                          "http_status": results[name]["http_status"],
                          "transport_error": results[name]["error"],
                          "result": results[name]["result"]}
                   for name, panel in PANELS.items()},
        "supporting": {name: {"query": query, "http_status": supporting[name]["http_status"],
                              "transport_error": supporting[name]["error"],
                              "result": supporting[name]["result"]}
                       for name, query in SUPPORTING.items()},
        "evaluation": evaluation,
        "journal_cross_check": journal_cross_check(args.consumer_journal) if args.consumer_journal else {},
    }
    common.write_json(folder / "report.json", report)

    print("\nEvidence:", folder)
    for name, value in report["panels"].items():
        print(f"  panel {value['panel']} {name}: {value['result']}")
    if report["journal_cross_check"]:
        print("Journal cross-check:", report["journal_cross_check"])
    print("RESULT:", evaluation["result"], "; ".join(evaluation["reasons"]))
    return common.EXIT_CODES[evaluation["result"]]


if __name__ == "__main__":
    sys.exit(main())
