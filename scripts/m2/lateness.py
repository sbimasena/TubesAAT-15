#!/usr/bin/env python3
"""Measure acquisition-time disorder in a captured satellite SSE stream.

This measures the disorder relevant to the processor's event-time watermark:
for each previously unseen detection, how far it falls behind the newest
acquisition time observed before it. It does not measure acquisition-to-ingest
wall-clock latency because the raw sampler does not timestamp individual
frames against the feed's virtual clock.

Usage:
    python scripts/m2/lateness.py STREAM.sse
    python scripts/m2/lateness.py STREAM.sse --lateness-seconds 86400
"""

import argparse
from datetime import datetime, timezone
import json
import math
from pathlib import Path


DEFAULT_LATENESS_SECONDS = 86_400
DEFAULT_MARGIN_SECONDS = 3_600


class RawNumber(str):
    """Preserve the source spelling of JSON numbers used in detection keys."""


def scalar_text(value):
    """Match the satellite decoder's string conversion for raw JSON scalars."""
    if isinstance(value, RawNumber):
        return str(value)
    if isinstance(value, str):
        return value
    if value is None:
        return ""
    if isinstance(value, bool):
        return "true" if value else "false"
    return str(value)


def event_time(payload):
    """Parse a detection's acquisition timestamp, or return None if absent."""
    detection = payload.get("detection") or payload
    date = detection.get("acq_date")
    clock = detection.get("acq_time")
    if date is None or clock is None:
        key = payload.get("key") or payload.get("detection_key") or ""
        parts = str(key).split("|")
        if len(parts) != 5:
            return None
        date, clock = parts[1], parts[2]
    clock_text = scalar_text(clock).strip()
    if not clock_text.isdigit() or len(clock_text) > 4:
        return None
    padded = clock_text.zfill(4)
    try:
        return datetime.strptime(f"{date} {padded}", "%Y-%m-%d %H%M").replace(
            tzinfo=timezone.utc
        )
    except (TypeError, ValueError):
        return None


def detection_key(payload):
    """Return the same identity tuple used by the Go detection decoder."""
    detection = payload.get("detection") or payload
    fields = (
        detection.get("satellite"),
        detection.get("acq_date"),
        detection.get("acq_time"),
        detection.get("latitude"),
        detection.get("longitude"),
    )
    if any(value is None for value in fields):
        return None
    return "|".join(scalar_text(value) for value in fields)


def nearest_rank(values, fraction):
    """Return a nearest-rank percentile from a non-empty sequence."""
    if not values:
        return None
    ordered = sorted(values)
    rank = max(1, math.ceil(fraction * len(ordered)))
    return ordered[rank - 1]


def iter_sse_events(path):
    """Yield ``(event_name, event_id, joined_data)`` tuples from an SSE file."""
    event_name = "message"
    event_id = ""
    data_lines = []

    def take_event():
        nonlocal event_name, event_id, data_lines
        result = (event_name, event_id, "\n".join(data_lines)) if data_lines else None
        event_name = "message"
        event_id = ""
        data_lines = []
        return result

    with path.open(encoding="utf-8", errors="replace", newline="") as handle:
        for raw_line in handle:
            line = raw_line.rstrip("\r\n")
            if not line:
                event = take_event()
                if event is not None:
                    yield event
                continue
            if line.startswith(":"):
                continue
            field, separator, value = line.partition(":")
            if separator and value.startswith(" "):
                value = value[1:]
            if field == "event":
                event_name = value
            elif field == "id" and "\x00" not in value:
                event_id = value
            elif field == "data":
                data_lines.append(value)
        event = take_event()
        if event is not None:
            yield event


def analyse(path, lateness_seconds=DEFAULT_LATENESS_SECONDS):
    """Return event-time disorder metrics for previously unseen detections."""
    event_counts = {}
    lag_samples = []
    seen = set()
    newest_event_time = None
    late_detections = 0
    unparsed_detections = 0
    duplicate_detections = 0
    duplicate_offsets = 0
    offset_gaps = 0
    invalid_offsets = 0
    first_offset = last_offset = None

    for kind, event_id, data in iter_sse_events(path):
        if event_id:
            try:
                offset = int(event_id)
                if offset < 0:
                    raise ValueError
            except ValueError:
                offset = None
                invalid_offsets += 1
            if offset is not None:
                if last_offset is not None and offset <= last_offset:
                    duplicate_offsets += 1
                    continue
                if last_offset is not None and offset > last_offset + 1:
                    offset_gaps += offset - last_offset - 1
                if first_offset is None:
                    first_offset = offset
                last_offset = offset
        event_counts[kind] = event_counts.get(kind, 0) + 1
        if kind == "heartbeat":
            continue
        if kind == "update":
            try:
                update_payload = json.loads(
                    data, parse_int=RawNumber, parse_float=RawNumber
                )
            except json.JSONDecodeError:
                update_payload = {}
            if update_payload.get("changes") is not None:
                continue
        elif kind != "detection":
            continue

        try:
            payload = json.loads(data, parse_int=RawNumber, parse_float=RawNumber)
        except json.JSONDecodeError:
            unparsed_detections += 1
            continue

        identity = detection_key(payload)
        acquired = event_time(payload)
        if identity is None or acquired is None:
            unparsed_detections += 1
            continue
        if identity in seen:
            duplicate_detections += 1
            continue

        lag = (
            max(0.0, (newest_event_time - acquired).total_seconds())
            if newest_event_time is not None
            else 0.0
        )
        lag_samples.append(lag)
        if newest_event_time is not None and lag > lateness_seconds:
            late_detections += 1
        if newest_event_time is None or acquired > newest_event_time:
            newest_event_time = acquired
        seen.add(identity)

    return {
        "measurement": "event_time_disorder_not_acquisition_to_ingest_latency",
        "events": event_counts,
        "eligible_detections": len(lag_samples),
        "new_detections_in_order": sum(lag == 0 for lag in lag_samples),
        "out_of_order_detections": sum(lag > 0 for lag in lag_samples),
        "duplicate_detections_excluded": duplicate_detections,
        "unparsed_detections": unparsed_detections,
        "first_offset": first_offset,
        "last_offset": last_offset,
        "duplicate_offsets_excluded": duplicate_offsets,
        "offset_gaps": offset_gaps,
        "invalid_offsets": invalid_offsets,
        "newest_event_time": (
            newest_event_time.isoformat() if newest_event_time is not None else None
        ),
        "lag_seconds": {
            "p50": nearest_rank(lag_samples, 0.50),
            "p95": nearest_rank(lag_samples, 0.95),
            "p99": nearest_rank(lag_samples, 0.99),
            "max": max(lag_samples) if lag_samples else None,
        },
        "lateness_threshold_seconds": lateness_seconds,
        "detections_beyond_lateness_threshold": late_detections,
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("stream", type=Path)
    parser.add_argument(
        "--lateness-seconds",
        type=int,
        default=DEFAULT_LATENESS_SECONDS,
        help="current processor threshold (default: %(default)s)",
    )
    parser.add_argument(
        "--margin-seconds",
        type=int,
        default=DEFAULT_MARGIN_SECONDS,
        help="margin to add to the measured p99 for a candidate (default: %(default)s)",
    )
    args = parser.parse_args()
    if args.lateness_seconds <= 0:
        parser.error("--lateness-seconds must be positive")
    if args.margin_seconds < 0:
        parser.error("--margin-seconds must be nonnegative")

    result = analyse(args.stream, args.lateness_seconds)
    p99 = result["lag_seconds"]["p99"]
    result["candidate_lateness_seconds"] = (
        math.ceil(p99 + args.margin_seconds) if p99 is not None else None
    )
    print(json.dumps(result, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
