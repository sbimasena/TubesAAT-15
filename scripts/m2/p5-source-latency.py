#!/usr/bin/env python3
"""P5.3: BMKG/PVMBG latency during Sensor Pantai outage.

Measures baseline latency, simulates Pantai outage, measures during and after.
"""
import argparse
import datetime
import json
import secrets
import subprocess
import time
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
        start = time.time()
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            elapsed = (time.time() - start) * 1000
            body = json.loads(resp.read())
            return {"status": resp.status, "body": body, "latency_ms": round(elapsed, 2)}
    except urllib.error.HTTPError as e:
        elapsed = (time.time() - start) * 1000
        try:
            body = json.loads(e.read())
        except Exception:
            body = None
        return {"status": e.code, "body": body, "latency_ms": round(elapsed, 2)}
    except Exception as e:
        elapsed = (time.time() - start) * 1000
        return {"status": 0, "body": {"error": str(e)}, "latency_ms": round(elapsed, 2)}


def collect_samples(base_url, token, source, count=10, interval=2):
    samples = []
    for _ in range(count):
        resp = http(base_url, f"/internal/v1/status", token=token)
        samples.append(resp)
        time.sleep(interval)
    return samples


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--aggregator-url", required=True, help="Aggregator base URL")
    parser.add_argument("--audit-token", required=True, help="Audit bearer token")
    parser.add_argument("--kubectl", default="kubectl", help="kubectl binary")
    parser.add_argument("--namespace", default="sistem-informasi-kebencanaan-bnpb", help="K8s namespace")
    parser.add_argument("--output-dir", type=Path, help="Evidence directory")
    args = parser.parse_args()

    stamp = datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    folder = (args.output_dir or ROOT / "artifacts/m2/runs" / (stamp + "-p5-source-latency-" + secrets.token_hex(3))).resolve()
    folder.mkdir(parents=True, exist_ok=False)

    base = args.aggregator_url.rstrip("/")
    token = args.audit_token

    report = {"timestamp": now(), "phases": {}}

    # Phase 1: Baseline
    print("Phase 1: Baseline (10 samples)")
    baseline = collect_samples(base, token, "BMKG", count=10, interval=2)
    report["phases"]["baseline"] = {
        "samples": len(baseline),
        "statuses": [s["status"] for s in baseline],
        "avg_latency_ms": round(sum(s["latency_ms"] for s in baseline) / len(baseline), 2),
    }
    (folder / "baseline.json").write_text(json.dumps(baseline, indent=2), encoding="utf-8")

    # Phase 2: Outage (scale sensor to 0)
    print("Phase 2: Scaling sensor-pantai to 0")
    subprocess.run([args.kubectl, "-n", args.namespace, "scale", "deployment", "sensor-pantai", "--replicas=0"], capture_output=True)
    time.sleep(5)

    print("Phase 2: Measuring during outage (10 samples)")
    during = collect_samples(base, token, "BMKG", count=10, interval=2)
    report["phases"]["during_outage"] = {
        "samples": len(during),
        "statuses": [s["status"] for s in during],
        "avg_latency_ms": round(sum(s["latency_ms"] for s in during) / len(during), 2),
    }
    (folder / "during-outage.json").write_text(json.dumps(during, indent=2), encoding="utf-8")

    # Phase 3: Recovery (scale sensor back to 1)
    print("Phase 3: Scaling sensor-pantai back to 1")
    subprocess.run([args.kubectl, "-n", args.namespace, "scale", "deployment", "sensor-pantai", "--replicas=1"], capture_output=True)
    time.sleep(20)

    print("Phase 3: Measuring after recovery (10 samples)")
    after = collect_samples(base, token, "BMKG", count=10, interval=2)
    report["phases"]["after_recovery"] = {
        "samples": len(after),
        "statuses": [s["status"] for s in after],
        "avg_latency_ms": round(sum(s["latency_ms"] for s in after) / len(after), 2),
    }
    (folder / "after-recovery.json").write_text(json.dumps(after, indent=2), encoding="utf-8")

    (folder / "report.json").write_text(json.dumps(report, indent=2), encoding="utf-8")
    print(f"\nEvidence: {folder}")
    print("RESULT: MANUAL_REVIEW_REQUIRED")
    return 0


if __name__ == "__main__":
    import sys
    sys.exit(main())
