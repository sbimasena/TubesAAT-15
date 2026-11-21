#!/usr/bin/env python3
"""Check the event-to-durable-ack SLO from a notification-consumer journal.

The journal records one line per durably acknowledged event with the
Aggregator's ``ingested_at`` and the measured ``event_age_ms``. This script
computes the latency percentiles and, when a published baseline is supplied,
the number of events that never reached the durable journal.

Usage:
    python3 scripts/m2/slo-check.py --journal artifacts/notification/events.jsonl
    python3 scripts/m2/slo-check.py --journal /data/events.jsonl --expected 1000

Exit status is 0 only when there are samples, the configured percentile meets
the target, and no event is missing relative to the baseline.
"""
import argparse
import json
import math
import sys
from pathlib import Path


def read_records(path):
    """Return records from a JSONL file or every *.jsonl file in a directory."""
    files = sorted(path.glob("*.jsonl")) if path.is_dir() else [path]
    if not files:
        raise SystemExit(f"no journal files under {path}")
    records, corrupt = [], 0
    for file in files:
        with file.open("r", encoding="utf-8", errors="replace") as handle:
            for line in handle:
                line = line.strip()
                if not line:
                    continue
                try:
                    records.append(json.loads(line))
                except json.JSONDecodeError:
                    corrupt += 1
    return records, corrupt


def percentile(sorted_values, percent):
    """Nearest-rank percentile; the list must be sorted and non-empty."""
    rank = max(1, math.ceil(percent / 100 * len(sorted_values)))
    return sorted_values[rank - 1]


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--journal", type=Path, default=Path("artifacts/notification/events.jsonl"),
                        help="JSONL journal file or directory of *.jsonl (default: %(default)s)")
    parser.add_argument("--target-ms", type=float, default=5000.0,
                        help="Latency ceiling for the SLI (default: %(default)s)")
    parser.add_argument("--percentile", type=float, default=99.0,
                        help="Percentile that must meet the target (default: %(default)s)")
    parser.add_argument("--expected", type=int, default=None,
                        help="Published event count for the same window; enables the loss check")
    parser.add_argument("--output", type=Path, default=None, help="Write the JSON report to this path")
    args = parser.parse_args()

    if not args.journal.exists():
        raise SystemExit(f"journal not found: {args.journal}")
    if not 0 < args.percentile <= 100:
        raise SystemExit("--percentile must be in (0, 100]")

    records, corrupt = read_records(args.journal)
    ages, seen, excluded = [], set(), 0
    for record in records:
        message_id = record.get("message_id")
        if message_id:
            if message_id in seen:
                continue
            seen.add(message_id)
        age = record.get("event_age_ms")
        if not isinstance(age, (int, float)) or age < 0:
            excluded += 1
            continue
        ages.append(float(age))

    if not ages:
        raise SystemExit(f"no event_age_ms samples in {args.journal} ({len(records)} records)")

    ages.sort()
    report = {
        "journal": str(args.journal),
        "records": len(records),
        "unique_events": len(seen),
        "measured": len(ages),
        "excluded_without_age": excluded,
        "corrupt_lines": corrupt,
        "target_ms": args.target_ms,
        "percentile": args.percentile,
        "p50_ms": round(percentile(ages, 50), 3),
        "p95_ms": round(percentile(ages, 95), 3),
        "p99_ms": round(percentile(ages, 99), 3),
        "max_ms": round(ages[-1], 3),
        "within_target": sum(1 for age in ages if age <= args.target_ms),
    }
    report["within_target_ratio"] = report["within_target"] / len(ages)
    report["over_target"] = len(ages) - report["within_target"]
    report["slo_percentile_ms"] = round(percentile(ages, args.percentile), 3)

    failed = report["slo_percentile_ms"] > args.target_ms
    if args.expected is not None:
        report["expected"] = args.expected
        report["lost"] = max(0, args.expected - len(seen))
        failed = failed or report["lost"] > 0

    print(f"journal: {args.journal}")
    print(f"events: {len(seen)} unique, {len(ages)} measured, {excluded} without age, {corrupt} corrupt lines")
    print(f"p50={report['p50_ms']:.1f}ms p95={report['p95_ms']:.1f}ms p99={report['p99_ms']:.1f}ms max={report['max_ms']:.1f}ms")
    print(f"within {args.target_ms:.0f}ms: {report['within_target_ratio']:.2%} "
          f"({report['within_target']}/{len(ages)}), target p{args.percentile:g} <= {args.target_ms:.0f}ms")
    if args.expected is not None:
        print(f"lost: {report['lost']} of {args.expected} published events")
    print("RESULT:", "FAIL" if failed else "PASS")

    if args.output:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
