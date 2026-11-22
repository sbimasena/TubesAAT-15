#!/usr/bin/env python3
"""Practice "auditor": Audit API snapshot, Tester Agent scenario, recovery, snapshot, verdict.

usage: AUDIT_TOKEN=... audit-run.py <scenario> [--port 18093] [--settle 90] [--timeout 600]

The exam auditor is the assistants' (spesifikasi-m2.md §Lampiran A); this mimics it for
practice scenarios: no source may lose events, no hazard_id may appear twice, and sampled
events must still have a primary and a replica on different nodes.
"""
from __future__ import annotations

import argparse
import datetime
import json
import os
import subprocess
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from verify_common import http_json  # noqa: E402

NS = "sistem-informasi-kebencanaan-bnpb"
TESTER_NS = "tester-agent"
SOURCES = ["BMKG", "PVMBG", "SATELIT", "SENSOR_PANTAI"]
ROOT = Path(__file__).resolve().parents[2]
ENV = {**os.environ, "MSYS_NO_PATHCONV": "1"}


def now():
    return datetime.datetime.now(datetime.timezone.utc)


def verdict(before, after, window_ids, locations):
    """Pure check over snapshots; returns (passed, list of failure reasons)."""
    reasons = []
    for source in SOURCES:
        b, a = before[source], after[source]
        if a.get("partial"):
            reasons.append(f"{source}: checksum sesudah masih partial")
        if a.get("count", 0) < b.get("count", 0):
            reasons.append(f"{source}: count turun {b['count']} -> {a['count']}")
    dupes = len(window_ids) - len(set(window_ids))
    if dupes:
        reasons.append(f"{dupes} hazard_id duplikat di jendela skenario")
    for hazard_id, loc in locations.items():
        nodes = {item["node"] for item in loc.get("locations", [])}
        if loc.get("partial") or len(nodes) < 2:
            reasons.append(f"{hazard_id}: {len(nodes)} node, partial={loc.get('partial')}")
    return not reasons, reasons


class Audit:
    def __init__(self, token, port):
        self.token, self.port, self.proc = token, port, None
        self.base = f"http://127.0.0.1:{port}"

    def forward(self):
        # A port-forward pins one aggregator pod; kill-leader may kill it, so restart on failure.
        if self.proc and self.proc.poll() is None:
            self.proc.kill()
        self.proc = subprocess.Popen(["kubectl", "-n", NS, "port-forward", "svc/aggregator", f"{self.port}:8083"],
                                     stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, env=ENV)
        time.sleep(3)

    def get(self, path, tries=6):
        for _ in range(tries):
            result = http_json(self.base, path, self.token, timeout=30)
            if result["status"] == 200:
                return result["body"]
            if result["status"] in (0, 502, 503):
                self.forward()
                continue
            raise RuntimeError(f"{path}: {result['error']}")
        raise RuntimeError(f"{path}: tidak menjawab setelah {tries} percobaan")

    def checksums(self):
        return {s: self.get(f"/internal/audit/checksum?source={s}") for s in SOURCES}

    def window_ids(self, since):
        ids, cursor = [], ""
        while True:
            page = self.get(f"/internal/audit/events?limit=1000&since={since}" + (f"&cursor={cursor}" if cursor else ""))
            ids += [item["hazard_id"] for item in page["items"]]
            cursor = page.get("next_cursor") or ""
            if not cursor:
                return ids, page.get("partial", False)

    def close(self):
        if self.proc:
            self.proc.kill()


def kubectl(*args, check=False):
    return subprocess.run(["kubectl", *args], capture_output=True, text=True, env=ENV, check=check)


def stores_ready():
    out = kubectl("-n", NS, "get", "pods", "-l", "app=store", "-o",
                  "jsonpath={range .items[*]}{.status.containerStatuses[0].ready} {end}").stdout.split()
    return len(out) == 3 and all(x == "true" for x in out)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("scenario")
    parser.add_argument("--port", type=int, default=18093)
    parser.add_argument("--settle", type=int, default=90, help="detik minimum setelah skenario dimulai")
    parser.add_argument("--timeout", type=int, default=600, help="batas tunggu pemulihan (detik)")
    args = parser.parse_args()
    token = os.environ.get("AUDIT_TOKEN", "").strip()
    if not token:
        sys.exit("AUDIT_TOKEN kosong")

    stamp = now().strftime("%Y%m%dT%H%M%SZ")
    out = ROOT / "artifacts/m2/runs" / f"{stamp}-audit-{args.scenario}"
    out.parent.mkdir(parents=True, exist_ok=True)
    audit = Audit(token, args.port)
    audit.forward()
    try:
        before = audit.checksums()
        sample = audit.get("/internal/audit/events?limit=5")["items"]
        ids = [item["hazard_id"] for item in sample]
        print(f"sebelum: " + ", ".join(f"{s}={before[s]['count']}" for s in SOURCES), flush=True)

        started = now()
        run = kubectl("-n", TESTER_NS, "exec", "daemonset/tester-agent", "--", "/app", "run", "practice", args.scenario)
        print(f"tester exit={run.returncode}", flush=True)
        time.sleep(args.settle)
        deadline = time.monotonic() + args.timeout
        while True:
            after = audit.checksums()
            if stores_ready() and not any(after[s].get("partial") for s in SOURCES):
                break
            if time.monotonic() > deadline:
                print("batas tunggu pemulihan habis", flush=True)
                break
            time.sleep(10)
        recovered = now()

        window, window_partial = audit.window_ids(started.isoformat().replace("+00:00", "Z"))
        locations = {i: audit.get(f"/internal/audit/location?hazard_id={i}") for i in ids}
        passed, reasons = verdict(before, after, window, locations)
        if window_partial:
            reasons.append("pembacaan events jendela skenario partial")
            passed = False
    finally:
        audit.close()

    # --tail, not --since-time: clock-skew leaves the agent's timestamps ~300 s behind.
    tester_log = kubectl("-n", TESTER_NS, "logs", "-l", "app=tester-agent", "--prefix", "--max-log-requests=10",
                         "--tail=20").stdout
    report = {
        "scenario": args.scenario, "started_at": started.isoformat(), "recovered_at": recovered.isoformat(),
        "recovery_s": round((recovered - started).total_seconds()), "tester_exit": run.returncode,
        "before": before, "after": after,
        "window_events": len(window), "window_duplicates": len(window) - len(set(window)),
        "locations": locations, "verdict": "LOLOS" if passed else "GAGAL", "reasons": reasons,
    }
    out.with_suffix(".json").write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    out.with_suffix(".log").write_text(run.stdout + run.stderr + "\n--- tester-agent logs ---\n" + tester_log,
                                       encoding="utf-8")
    print(f"sesudah: " + ", ".join(f"{s}={after[s]['count']}" for s in SOURCES))
    print(f"event baru di jendela: {len(window)}, duplikat: {report['window_duplicates']}, pulih {report['recovery_s']} s")
    for i, loc in locations.items():
        print(f"location {i}: " + ", ".join(f"{x['node']}/{x['unit']}/{x['role']}" for x in loc["locations"]))
    print(f"PUTUSAN: {report['verdict']}" + ("" if passed else " - " + "; ".join(reasons)))
    print(f"bukti: {out}.json, {out}.log")
    return 0 if passed else 1


if __name__ == "__main__":
    sys.exit(main())
