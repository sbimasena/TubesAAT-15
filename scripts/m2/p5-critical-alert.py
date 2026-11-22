#!/usr/bin/env python3
"""P5.1: critical Sensor Pantai alert SLO from Prometheus ACK histograms.

The journal provides identity (one row per critical notification) while
Prometheus provides the ingestion-to-local-ACK latency. An independent
expected-ID file is required to claim zero loss; without it the result is
PARTIAL, never PASS. Fewer than --min-alerts observed alerts is BLOCKED.
"""
import argparse
import datetime
import secrets
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import verify_common as common


ROOT = Path(__file__).resolve().parents[2]
TARGET_RATIO = 0.99
TARGET_SECONDS = 5.0


def reconcile_expected(critical, expected, kind="auto"):
    """Compare journal identities with an independent expected set."""
    if not expected:
        return None
    if kind == "auto":
        kind = "hazard_id" if all(str(item).startswith("haz_") for item in expected) else "message_id"
    if kind in ("hazard_id", "source_ref_id"):
        # source_ref_id = the sensor's own observation_id, so the expected list can come
        # straight from the sensor API, independent of the Aggregator.
        observed = {item[kind] for item in critical if item.get(kind)}
    else:
        observed = {item["message_id"] for item in critical}
    result = common.reconcile_ids(observed, expected)
    result["kind"] = kind
    return result


def evaluate(observed_count, age_samples, min_alerts, ratio, p95, reconciliation):
    """Return PASS/FAIL/BLOCKED/PARTIAL without inventing missing samples."""
    if observed_count < min_alerts:
        return {"result": "BLOCKED",
                "reasons": [f"only {observed_count} critical alerts observed; need {min_alerts}"]}
    if age_samples is None or age_samples < min_alerts:
        return {"result": "BLOCKED",
                "reasons": [f"Prometheus has {age_samples} ACK samples; need {min_alerts}"]}
    if ratio is None or p95 is None:
        return {"result": "BLOCKED", "reasons": ["Prometheus ACK histogram has no data"]}
    reasons = []
    if ratio < TARGET_RATIO:
        reasons.append(f"ACK ratio within 5 s is {ratio:.4f}, below {TARGET_RATIO}")
    if p95 > TARGET_SECONDS:
        reasons.append(f"p95 ACK age is {p95:.3f} s, above {TARGET_SECONDS} s")
    if reconciliation is not None and reconciliation["missing"]:
        reasons.append(f"{len(reconciliation['missing'])} expected alert(s) missing from the journal")
    if reasons:
        return {"result": "FAIL", "reasons": reasons}
    if reconciliation is None:
        return {"result": "PARTIAL",
                "reasons": ["zero loss not proven: no independent expected-alert baseline supplied"]}
    return {"result": "PASS", "reasons": []}


def journal_window(path, since, until):
    records = common.read_jsonl(path)
    critical = common.critical_alerts(common.within(records, since, until))
    return records, critical


def metric_queries(window):
    return {
        "ack_total": f"sum(increase(notification_pantai_critical_acknowledged_total[{window}]))",
        "age_samples": f"sum(increase(notification_pantai_critical_age_seconds_count[{window}]))",
        "within_5s": f'sum(increase(notification_pantai_critical_age_seconds_bucket{{le="5"}}[{window}]))',
        "ratio": f'sum(rate(notification_pantai_critical_age_seconds_bucket{{le="5"}}[5m])) / '
                 f'(sum(rate(notification_pantai_critical_age_seconds_count[5m])) > 0)',
        "p95": 'histogram_quantile(0.95, sum by (le) '
               '(increase(notification_pantai_critical_age_seconds_bucket[' + window + '])))',
    }


def capture_consumer_metrics(folder, url):
    response = common.http_text(url, "/metrics")
    lines = [line for line in response["text"].splitlines()
             if "notification_pantai_critical" in line and not line.startswith("#")]
    (folder / "consumer-metrics.txt").write_text("\n".join(lines) + "\n", encoding="utf-8")
    return {"http_status": response["status"], "error": response["error"], "lines": len(lines)}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--prometheus-url", required=True, help="Prometheus base URL")
    parser.add_argument("--journal-path", required=True, help="Notification consumer journal")
    parser.add_argument("--expected-ids", help="Independent expected alert IDs (one per line or JSON)")
    parser.add_argument("--expected-id-kind", choices=("auto", "message_id", "hazard_id", "source_ref_id"), default="auto")
    parser.add_argument("--min-alerts", type=int, default=20, help="Minimum critical alerts for an evaluation")
    parser.add_argument("--window", default="1h", help="Prometheus lookback, e.g. 15m or 1h")
    parser.add_argument("--since", help="Journal window start (ISO 8601); defaults to now-window")
    parser.add_argument("--until", help="Journal window end (ISO 8601); defaults to now")
    parser.add_argument("--wait-seconds", type=int, default=0, help="Wait for critical alerts before evaluating")
    parser.add_argument("--poll-seconds", type=int, default=10, help="Journal poll interval while waiting")
    parser.add_argument("--consumer-metrics-url", help="Optional notification consumer /metrics base URL")
    parser.add_argument("--output-dir", type=Path, help="Evidence directory")
    args = parser.parse_args()
    if args.min_alerts < 1:
        parser.error("--min-alerts must be at least 1")
    window_seconds, window = common.parse_window(args.window)
    until = common.parse_timestamp(args.until) or common.utcnow()
    since = common.parse_timestamp(args.since) or (until - datetime.timedelta(seconds=window_seconds))
    if since >= until:
        parser.error("--since must be before --until")

    stamp = datetime.datetime.now(common.UTC).strftime("%Y%m%dT%H%M%SZ")
    folder = (args.output_dir or ROOT / "artifacts/m2/runs" /
              (stamp + "-p5-critical-alert-" + secrets.token_hex(3))).resolve()
    folder.mkdir(parents=True, exist_ok=False)

    deadline = time.monotonic() + max(0, args.wait_seconds)
    records, critical = journal_window(args.journal_path, since, until)
    while len(critical) < args.min_alerts and time.monotonic() < deadline:
        time.sleep(max(1, args.poll_seconds))
        until = common.utcnow()
        records, critical = journal_window(args.journal_path, since, until)
    common.write_json(folder / "journal-critical.json", critical)

    results = {name: common.prom_query(args.prometheus_url, query)
               for name, query in metric_queries(window).items()}
    states = {name: value["result"] for name, value in results.items()}
    values = {name: state.get("value") for name, state in states.items()}

    ratio = values["ratio"]
    if ratio is None and values["within_5s"] is not None and values["age_samples"]:
        ratio = values["within_5s"] / values["age_samples"]
    expected = common.parse_id_file(args.expected_ids) if args.expected_ids else []
    reconciliation = reconcile_expected(critical, expected, args.expected_id_kind)
    evaluation = evaluate(len(critical), values["age_samples"], args.min_alerts,
                          ratio, values["p95"], reconciliation)
    metrics_capture = capture_consumer_metrics(folder, args.consumer_metrics_url) \
        if args.consumer_metrics_url else {}

    report = {
        "timestamp": common.isoformat(),
        "window": {"since": since.isoformat(), "until": until.isoformat(), "prometheus": window},
        "target": {"ack_ratio_within_5s": TARGET_RATIO, "p95_seconds": TARGET_SECONDS,
                   "min_alerts": args.min_alerts},
        "journal": {"path": str(args.journal_path), "records": len(records),
                    "critical_unique": len(critical)},
        "prometheus": {name: {"query": query, "http_status": results[name]["http_status"],
                              "transport_error": results[name]["error"], "result": states[name]}
                       for name, query in metric_queries(window).items()},
        "ratio_used": ratio,
        "reconciliation": reconciliation,
        "zero_loss": ("PROVEN" if reconciliation is not None and not reconciliation["missing"]
                      else ("FAILED" if reconciliation is not None else "NOT_PROVEN")),
        "consumer_metrics": metrics_capture,
        "evaluation": evaluation,
    }
    common.write_json(folder / "report.json", report)

    print("\nEvidence:", folder)
    print("Critical alerts (journal):", len(critical), "Prometheus ACK samples:", values["age_samples"])
    print("Ratio within 5 s:", ratio, "p95 seconds:", values["p95"])
    if reconciliation is not None:
        print("Expected reconciliation:", reconciliation)
    print("Zero loss:", report["zero_loss"])
    print("RESULT:", evaluation["result"], "; ".join(evaluation["reasons"]))
    return common.EXIT_CODES[evaluation["result"]]


if __name__ == "__main__":
    sys.exit(main())
