#!/usr/bin/env python3
"""Check an offline notification-consumer journal.

Authors: Project contributors.
References: notification-consumer journal and Pantai D-SLO.

Journal event_age_ms measures ingestion to pre-append, not consumer ACK latency.
For identity reconciliation, supply an independent JSONL baseline of critical
Pantai observations and a journal export for the same preselected window.
Each baseline row is {"source_ref_id":"observation-id"}, optionally with
"hazard_revision":1. Supply revisions for all rows or omit them for all rows.
Without revisions, repeated observation IDs cannot be distinguished from updates.

Usage:
    python3 scripts/m2/slo-check.py --journal events.jsonl --baseline expected.jsonl --output result.json
    python3 scripts/m2/slo-check.py --journal /data/events.jsonl --expected 1000

--expected compares counts only; it cannot establish zero loss.
"""
from __future__ import annotations

import argparse
import json
import math
import sys
from collections import Counter
from pathlib import Path
from typing import Any

PANTAI_SOURCE = "SENSOR_PANTAI"
EventKey = tuple[str, int]


def read_records(files: list[Path]) -> tuple[list[dict[str, Any]], int]:
    """Return JSONL objects and the number of malformed lines."""
    records, corrupt = [], 0
    for file in files:
        with file.open("rb") as handle:
            for line in handle:
                line = line.strip()
                if not line:
                    continue
                try:
                    record = json.loads(line.decode("utf-8"))
                except (UnicodeDecodeError, json.JSONDecodeError):
                    corrupt += 1
                    continue
                if isinstance(record, dict):
                    records.append(record)
                else:
                    corrupt += 1
    return records, corrupt


def event_key(ref: object, revision: object, match_revisions: bool) -> EventKey | None:
    """Return an observation identity, or None when its fields are invalid."""
    if not isinstance(ref, str) or not ref.strip():
        return None
    if not match_revisions:
        return ref, 0
    if type(revision) is not int or revision <= 0:
        return None
    return ref, revision


def reconcile(
    baseline: list[dict[str, Any]], records: list[dict[str, Any]]
) -> tuple[list[dict[str, Any]], dict[str, Any]]:
    """Compare expected and durable critical Pantai identities."""
    if not baseline:
        raise ValueError("baseline must contain at least one expected observation")
    match_revisions = "hazard_revision" in baseline[0]
    expected: set[EventKey] = set()
    for index, row in enumerate(baseline, 1):
        key = event_key(row.get("source_ref_id"), row.get("hazard_revision"), match_revisions)
        if key is None or ("hazard_revision" in row) != match_revisions:
            raise ValueError(f"invalid baseline row {index}: use a consistent identity format")
        if key in expected:
            raise ValueError(f"duplicate identity in baseline row {index}")
        expected.add(key)

    observed: Counter[EventKey] = Counter()
    matched = []
    invalid = 0
    for record in records:
        payload = record.get("payload")
        if not isinstance(payload, dict) or not isinstance(payload.get("source"), str) or not payload["source"]:
            invalid += 1
            continue
        if payload["source"] != PANTAI_SOURCE:
            continue
        attrs = payload.get("attributes")
        if not isinstance(attrs, dict) or type(attrs.get("critical")) is not bool:
            invalid += 1
            continue
        if not attrs["critical"]:
            continue
        key = event_key(payload.get("source_ref_id"), record.get("hazard_revision"), match_revisions)
        message_id = record.get("message_id")
        if key is None or not isinstance(message_id, str) or not message_id.strip():
            invalid += 1
            continue
        observed[key] += 1
        if key in expected and observed[key] == 1:
            matched.append(record)

    def as_row(key: EventKey) -> dict[str, str | int]:
        """Render one key using the baseline schema."""
        if match_revisions:
            return {"source_ref_id": key[0], "hazard_revision": key[1]}
        return {"source_ref_id": key[0]}

    return matched, {
        "identity": "source_ref_id+hazard_revision" if match_revisions else "source_ref_id",
        "expected": len(expected),
        "observed": len(observed),
        "matched": len(matched),
        "invalid_journal_records": invalid,
        "missing": [as_row(key) for key in sorted(expected - observed.keys())],
        "unexpected": [as_row(key) for key in sorted(observed.keys() - expected)],
        "duplicates": [{**as_row(key), "records": count} for key, count in sorted(observed.items()) if count > 1],
    }


def percentile(sorted_values: list[float], percent: float) -> float:
    """Nearest-rank percentile; the list must be sorted and non-empty."""
    rank = max(1, math.ceil(percent / 100 * len(sorted_values)))
    return sorted_values[rank - 1]


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--journal", type=Path, default=Path("artifacts/notification/events.jsonl"),
                        help="JSONL journal file or directory of *.jsonl (default: %(default)s)")
    parser.add_argument("--target-ms", type=float, default=5000.0,
                        help="Latency ceiling for journal pre-append age (default: %(default)s)")
    parser.add_argument("--percentile", type=float, default=99.0,
                        help="Percentile that must meet the target (default: %(default)s)")
    comparison = parser.add_mutually_exclusive_group()
    comparison.add_argument("--expected", type=int, help="Expected count for the same window (count-only check)")
    comparison.add_argument("--baseline", type=Path, help="Independent critical Pantai identity JSONL for the same window")
    parser.add_argument("--output", type=Path, default=None, help="Write the JSON report to this path")
    args = parser.parse_args()

    if not 0 < args.percentile <= 100:
        parser.error("--percentile must be in (0, 100]")
    if not math.isfinite(args.target_ms) or args.target_ms <= 0:
        parser.error("--target-ms must be positive and finite")
    if args.expected is not None and args.expected < 0:
        parser.error("--expected must be nonnegative")

    files = sorted(args.journal.glob("*.jsonl")) if args.journal.is_dir() else [args.journal]
    if not files:
        parser.error("no journal files found")
    if args.baseline and args.baseline.resolve() in {file.resolve() for file in files}:
        parser.error("--baseline must not be part of the journal input")
    if args.output:
        output = args.output.resolve()
        inputs = files + ([args.baseline] if args.baseline else [])
        if output in {file.resolve() for file in inputs} or output == args.journal.resolve():
            parser.error("--output must not overwrite an input")
        if args.journal.is_dir() and output.parent == args.journal.resolve() and output.suffix.lower() == ".jsonl":
            parser.error("--output must not become a journal input")
    try:
        records, corrupt = read_records(files)
        identity = None
        if args.baseline:
            baseline, bad_baseline = read_records([args.baseline])
            if bad_baseline:
                raise ValueError(f"baseline has {bad_baseline} malformed lines")
            timing_records, identity = reconcile(baseline, records)
        else:
            timing_records = records
    except (OSError, ValueError) as err:
        parser.error(str(err))
    ages, seen, excluded = [], set(), 0
    for record in timing_records:
        message_id = record.get("message_id")
        if isinstance(message_id, str) and message_id and identity is None:
            if message_id in seen:
                continue
            seen.add(message_id)
        age = record.get("event_age_ms")
        if type(age) not in (int, float) or not 0 <= age <= sys.maxsize:
            excluded += 1
            continue
        ages.append(float(age))

    ages.sort()
    p_slo = percentile(ages, args.percentile) if ages else None
    report = {
        "journal": str(args.journal),
        "latency_basis": "aggregator_ingestion_to_journal_pre_append",
        "records": len(records),
        "unique_events": identity["observed"] if identity is not None else len(seen),
        "measured": len(ages),
        "excluded_without_age": excluded,
        "corrupt_lines": corrupt,
        "target_ms": args.target_ms,
        "percentile": args.percentile,
        "p50_ms": round(percentile(ages, 50), 3) if ages else None,
        "p95_ms": round(percentile(ages, 95), 3) if ages else None,
        "p99_ms": round(percentile(ages, 99), 3) if ages else None,
        "max_ms": round(ages[-1], 3) if ages else None,
        "within_target": sum(1 for age in ages if age <= args.target_ms),
    }
    report["within_target_ratio"] = report["within_target"] / len(ages) if ages else None
    report["over_target"] = len(ages) - report["within_target"]
    report["slo_percentile_ms"] = round(p_slo, 3) if p_slo is not None else None

    failed = corrupt > 0 or p_slo is None or p_slo > args.target_ms
    if identity is not None:
        report["reconciliation"] = identity
        identity["passed"] = not corrupt and not any(
            identity[key] for key in ("missing", "unexpected", "duplicates", "invalid_journal_records")
        )
        failed = failed or not identity["passed"] or excluded > 0
    if args.expected is not None:
        report["expected"] = args.expected
        report["lost"] = max(0, args.expected - len(seen))
        failed = failed or report["lost"] > 0

    print(f"journal: {args.journal}")
    print(f"events: {report['unique_events']} unique, {len(ages)} measured, {excluded} without age, "
          f"{corrupt} corrupt lines")
    if ages:
        print(f"pre-append: p50={report['p50_ms']:.1f}ms p95={report['p95_ms']:.1f}ms "
              f"p99={report['p99_ms']:.1f}ms max={report['max_ms']:.1f}ms")
        print(f"within {args.target_ms:.0f}ms: {report['within_target_ratio']:.2%} "
              f"({report['within_target']}/{len(ages)}), target p{args.percentile:g} <= {args.target_ms:.0f}ms")
    else:
        print("no event_age_ms samples", file=sys.stderr)
    if identity is not None:
        print(f"identity check: {len(identity['missing'])} missing, {len(identity['unexpected'])} unexpected, "
              f"{len(identity['duplicates'])} duplicated identities, "
              f"{identity['invalid_journal_records']} invalid journal records")
    if args.expected is not None:
        print(f"count-only shortfall: {report['lost']} of {args.expected}; identities not verified")
    if args.output:
        try:
            args.output.parent.mkdir(parents=True, exist_ok=True)
            args.output.write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
        except OSError as err:
            parser.error(str(err))
    print("RESULT:", "FAIL" if failed else "PASS")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
