#!/usr/bin/env python3
"""Collect real P1 report evidence on an already running, selected Compose project.

Runs check-ingestion.py (schema v1/v2, fanout, outage/recovery), then compares
source payloads with canonical records. Leaves PVMBG at v1/outage=false.
Does not restart services, alter application code, or delete data.
"""
import argparse
from datetime import datetime
import hashlib
import json
from pathlib import Path
import subprocess
import sys
import urllib.request

from demo_support import options, config, aggregator_read, settings, sql, logs, now, progress, redact


def get(service, path):
    if service == "aggregator":
        return aggregator_read(path, "p1-report-payload")["body"]
    headers = {"X-Correlation-ID": "p1-report-payload"}
    if service == "bmkg":
        headers["X-BMKG-Key"] = settings["BMKG_API_KEY"]
    elif service == "pvmbg":
        headers["Authorization"] = "Bearer " + settings["PVMBG_TOKEN"]
    port = settings[service.upper() + "_PORT"]
    request = urllib.request.Request("http://127.0.0.1:" + port + path, headers=headers)
    with urllib.request.urlopen(request, timeout=10) as response:
        assert response.status == 200
        return json.load(response)


def timestamp(value):
    return datetime.fromisoformat(value.replace("Z", "+00:00"))


def check_identity(event, source, reference):
    digest = hashlib.sha256((source + ":" + reference).encode()).hexdigest()[:32]
    assert event["hazard_id"] == "haz_" + digest
    assert event["source"] == source and event["source_ref_id"] == reference
    assert len(event) == 11


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, default=Path(options.evidence_dir) / "report-check.json")
    args = parser.parse_args()
    folder = args.output.parent
    folder.mkdir(parents=True, exist_ok=True)
    evidence = {"started_at": now(), "result": "FAIL", "project": options.project_name or config.get("name"),
                "source_commit": subprocess.check_output(["git", "rev-parse", "HEAD"], text=True).strip(),
                "tested_source": "WORKSPACE", "configuration": {
                    key: settings[key] for key in ("POLL_INTERVAL_SECONDS", "PVMBG_DELAY_MS",
                        "BMKG_GENERATE_INTERVAL_SECONDS", "PVMBG_GENERATE_INTERVAL_SECONDS")}}
    try:
        evidence["migrations_before"] = json.loads(sql(
            "SELECT json_agg(t) FROM (SELECT version, applied_at FROM schema_migrations ORDER BY version) t"))
        command = [sys.executable, "-B", "scripts/check-ingestion.py", "--env-file", options.env_file,
                   "--output", str(folder / "ingestion-check.json")]
        if options.project_name:
            command += ["--project-name", options.project_name]
        progress("Running schema evolution, two-consumer, outage and recovery checks")
        with (folder / "ingestion-run.log").open("w") as output:
            subprocess.run(command, stdout=output, stderr=subprocess.STDOUT, check=True)
        ingestion = json.loads((folder / "ingestion-check.json").read_text())
        assert ingestion["result"] == "PASS"
        progress("Comparing BMKG source event and related tsunami warning with canonical data")
        events = get("bmkg", "/seismic-events")
        warnings = get("bmkg", "/tsunami-warnings")
        canonical = get("aggregator", "/internal/v1/hazards?source=BMKG&limit=1000")["data"]
        by_id = {event["source_ref_id"]: event for event in canonical}
        warning = next(w for w in warnings if w["event_id"] in by_id
                       and "tsunami_warning" in by_id[w["event_id"]]["attributes"])
        raw = next(e for e in events if e["event_id"] == warning["event_id"])
        mapped = by_id[raw["event_id"]]
        check_identity(mapped, "BMKG", raw["event_id"])
        assert mapped["hazard_type"] == "SEISMIC"
        assert mapped["area_name"] == raw["region_name"]
        assert mapped["latitude"] == raw["epicenter_lat"] and mapped["longitude"] == raw["epicenter_lon"]
        assert abs((timestamp(mapped["occurred_at"]) - timestamp(raw["occurred_at"])).total_seconds()) < 0.00001
        assert mapped["severity"] == warning["threat_level"].strip().upper()
        for field in ("magnitude", "depth_km", "potential_tsunami"):
            assert mapped["attributes"][field] == raw[field]
        assert mapped["attributes"]["tsunami_warning"]["warning_id"] == warning["warning_id"]
        evidence["bmkg"] = {"source_event": raw, "source_warning": warning, "hazard": mapped,
                            "mapping_and_correlation": "PASS"}
        progress("Comparing PVMBG v1/v2 source fields, static coordinates and confidence value")
        reports = {r["report_id"]: r for r in get("pvmbg", "/volcanic-reports")}
        reference = {"V-001": ("Merapi", -7.5407, 110.4457), "V-002": ("Semeru", -8.1080, 112.9220),
                     "V-003": ("Anak Krakatau", -6.1020, 105.4230), "V-004": ("Sinabung", 3.1700, 98.3920),
                     "V-005": ("Agung", -8.3420, 115.5080), "V-006": ("Marapi", -0.3810, 100.4730)}
        for version in ("v1", "v2"):
            hazard = ingestion[version + "_hazard"]
            raw = reports[hazard["source_ref_id"]]
            check_identity(hazard, "PVMBG", raw["report_id"])
            assert hazard["hazard_type"] == "VOLCANIC" and hazard["severity"] == raw["alert_level"]
            assert (hazard["area_name"], hazard["latitude"], hazard["longitude"]) == reference[raw["volcano_id"]]
            assert abs((timestamp(hazard["occurred_at"]) - timestamp(raw["reported_at"])).total_seconds()) < 0.00001
            for field in ("eruption_count_24h", "ash_column_height_m"):
                assert hazard["attributes"][field] == raw[field]
            if version == "v1":
                assert "confidence_level" not in raw and "confidence_level" not in hazard["attributes"]
            else:
                assert hazard["attributes"]["confidence_level"] == raw["confidence_level"]
            evidence["pvmbg_" + version] = {"source_report": raw, "hazard": hazard, "mapping": "PASS"}
        evidence["migrations_after"] = json.loads(sql(
            "SELECT json_agg(t) FROM (SELECT version, applied_at FROM schema_migrations ORDER BY version) t"))
        assert evidence["migrations_before"] == evidence["migrations_after"]
        assert ingestion["states_before"] == ingestion["states_after"]
        evidence["no_restart"] = {name: {"container_id": state["id"], "started_at": state["started_at"],
                                         "unchanged": state == ingestion["states_after"][name]}
                                  for name, state in ingestion["states_before"].items()}
        evidence["consumers"] = ingestion["consumers"]
        correlation = ingestion["consumers"]["notification-consumer"]["correlation_id"]
        evidence["v2_correlation_id"] = correlation
        evidence["v2_trace"] = [record for record in logs("aggregator", evidence["started_at"])
                                if record.get("correlation_id") == correlation]
        assert any(record.get("target") == "pvmbg" for record in evidence["v2_trace"])
        assert any(record.get("message") == "canonical events and outbox committed"
                   or record.get("msg") == "canonical events and outbox committed" for record in evidence["v2_trace"])
        evidence["freshness"] = {key: ingestion[key] for key in
                                 ("outage_health", "repeated_outage_health", "recovered_health")}
        evidence["result"] = "PASS"
    except Exception as error:
        evidence["failure"] = redact(str(error))
        raise
    finally:
        evidence["finished_at"] = now()
        args.output.write_text(redact(json.dumps(evidence, indent=2)) + "\n")
    print("PASS: BMKG/tsunami mapping, PVMBG v1/v2, equal confidence value, unchanged containers/migration, two consumers")
    print(args.output)


if __name__ == "__main__":
    main()
