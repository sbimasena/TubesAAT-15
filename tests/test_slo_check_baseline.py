"""CLI tests for offline Pantai identity reconciliation.

Authors: Project contributors.
References: scripts/m2/slo-check.py.
"""
import json
import subprocess
import sys
from pathlib import Path
from typing import Any

import pytest

SCRIPT = Path(__file__).resolve().parents[1] / "scripts/m2/slo-check.py"


def delivery(ref: str, revision: int = 1, age: object = 10, **payload_fields: Any) -> dict[str, Any]:
    """Build a synthetic journal row."""
    payload = {"source": "SENSOR_PANTAI", "source_ref_id": ref, "attributes": {"critical": True}}
    return {
        "payload": {**payload, **payload_fields},
        "hazard_revision": revision,
        "message_id": f"{ref}:{revision}",
        "event_age_ms": age,
    }


def invoke(
    tmp_path: Path, baseline: list[dict[str, Any]], journal: list[object], *args: str
) -> tuple[subprocess.CompletedProcess[str], dict[str, Any]]:
    """Run the CLI using isolated JSONL inputs and collect its report."""
    expected, observed, output = (tmp_path / name for name in ("expected.jsonl", "journal.jsonl", "report.json"))
    for path, rows in ((expected, baseline), (observed, journal)):
        path.write_text("".join(json.dumps(row) + "\n" for row in rows), encoding="utf-8")
    result = subprocess.run(
        [sys.executable, "-B", str(SCRIPT), "--journal", str(observed), "--baseline", str(expected),
         "--output", str(output), *args],
        capture_output=True, text=True, check=False,
    )
    return result, json.loads(output.read_text(encoding="utf-8")) if output.exists() else {}


@pytest.mark.parametrize("revision", [False, True])
def test_matching_cohort(tmp_path: Path, revision: bool) -> None:
    """Condition: Critical alert plus unrelated records. Expected: Only the expected cohort is measured."""
    expected = {"source_ref_id": "a", **({"hazard_revision": 1} if revision else {})}
    result, report = invoke(tmp_path, [expected], [
        delivery("a", age=5000),
        delivery("a", source="BMKG"),
        delivery("b", attributes={"critical": False}),
    ])
    assert result.returncode == 0
    assert report["reconciliation"]["matched"] == report["measured"] == 1
    assert report["within_target_ratio"] == 1
    assert report["latency_basis"] == "aggregator_ingestion_to_journal_pre_append"


def test_equal_counts_do_not_hide_loss(tmp_path: Path) -> None:
    """Condition: Equal identity counts with mismatch and duplicate. Expected: All discrepancies appear."""
    expected = [{"source_ref_id": "a", "hazard_revision": rev} for rev in (1, 2)]
    result, report = invoke(tmp_path, expected, [delivery("a"), delivery("a"), delivery("a", 3)])
    reconciliation = report["reconciliation"]
    assert result.returncode == 1
    assert reconciliation["expected"] == reconciliation["observed"] == 2
    assert reconciliation["missing"] == [{"source_ref_id": "a", "hazard_revision": 2}]
    assert reconciliation["unexpected"] == [{"source_ref_id": "a", "hazard_revision": 3}]
    assert reconciliation["duplicates"] == [{"source_ref_id": "a", "hazard_revision": 1, "records": 2}]


@pytest.mark.parametrize("age", [None, True, -1, float("nan"), float("inf")])
def test_invalid_age_does_not_hide_reconciliation(tmp_path: Path, age: object) -> None:
    """Condition: Missing measurements and incomplete journal. Expected: Identity report and nonzero exit."""
    expected = [{"source_ref_id": ref} for ref in ("a", "b")]
    result, report = invoke(tmp_path, expected, [delivery("a", age=age), [], delivery(" ", age=10)])
    assert result.returncode == 1
    assert report["reconciliation"]["missing"] == [{"source_ref_id": "b"}]
    assert report["reconciliation"]["invalid_journal_records"] == report["corrupt_lines"] == 1
    assert report["excluded_without_age"] == 1
    assert report["within_target_ratio"] is None


def test_matching_identities_with_partial_timing(tmp_path: Path) -> None:
    """Condition: All IDs match but timing is partial. Expected: Identity check passes; overall check fails."""
    expected = [{"source_ref_id": ref} for ref in ("a", "b")]
    result, report = invoke(tmp_path, expected, [delivery("a"), delivery("b", age=None)])
    assert result.returncode == 1
    assert report["reconciliation"]["passed"] is True
    assert report["measured"] == report["excluded_without_age"] == 1


@pytest.mark.parametrize("baseline", [
    [],
    [{"source_ref_id": "a"}, {"source_ref_id": "a"}],
    [{"source_ref_id": "a"}, {"source_ref_id": "b", "hazard_revision": 1}],
    [{"source_ref_id": "a", "hazard_revision": True}],
])
def test_invalid_baseline(tmp_path: Path, baseline: list[dict[str, Any]]) -> None:
    """Condition: Inconsistent or empty baseline. Expected: Usage error, not a report."""
    result, report = invoke(tmp_path, baseline, [delivery("a")])
    assert result.returncode == 2
    assert not report


def test_baseline_and_count_are_exclusive(tmp_path: Path) -> None:
    """Condition: Both modes requested. Expected: Usage error."""
    result, _ = invoke(tmp_path, [{"source_ref_id": "a"}], [delivery("a")], "--expected", "1")
    assert result.returncode == 2
