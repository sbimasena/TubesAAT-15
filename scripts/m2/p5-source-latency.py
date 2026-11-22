#!/usr/bin/env python3
"""P5.3: BMKG Client API read latency while Sensor Pantai is unavailable.

Measures the Field Team read path (GET /hazards?source=BMKG), not the internal
status endpoint. Scaling the sensor is opt-in via --scale-outage and is always
undone in a finally block. Without the opt-in the outage phase is BLOCKED.
"""
import argparse
import datetime
import secrets
import subprocess
import sys
import time
import urllib.parse
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import verify_common as common


ROOT = Path(__file__).resolve().parents[2]


def kubectl_run(binary, args, timeout=60):
    try:
        result = subprocess.run([binary, *args], capture_output=True, text=True,
                                encoding="utf-8", errors="replace", timeout=timeout)
        return {"returncode": result.returncode, "stdout": result.stdout.strip(),
                "stderr": result.stderr.strip()[:500]}
    except Exception as error:  # noqa: BLE001 - summarized for evidence
        return {"returncode": -1, "stdout": "", "stderr": common.summarize_error(error)}


def ready_replicas(binary, namespace, deployment):
    result = kubectl_run(binary, ["-n", namespace, "get", "deployment", deployment,
                                  "-o", "jsonpath={.status.readyReplicas}"])
    if result["returncode"] != 0:
        return None
    value = result["stdout"].strip()
    return int(value) if value.isdigit() else 0


def wait_ready(binary, namespace, deployment, wanted, timeout):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if ready_replicas(binary, namespace, deployment) == wanted:
            return True
        time.sleep(2)
    return False


def scale(binary, namespace, deployment, replicas):
    return kubectl_run(binary, ["-n", namespace, "scale", "deployment", deployment,
                                "--replicas=" + str(replicas)])


def collect(client_url, token, samples, interval, source="BMKG"):
    query = urllib.parse.urlencode({"source": source, "limit": "50"})
    results = []
    for index in range(samples):
        response = common.http_json(client_url, "/hazards?" + query, token=token)
        results.append({"status": response["status"], "latency_ms": response["latency_ms"],
                        "error": response["error"]})
        if index + 1 < samples:
            time.sleep(interval)
    return results


def summarize_phase(samples, target_ms):
    successful = [sample for sample in samples if sample["status"] == 200]
    if successful:
        summary = common.summarize_latencies([sample["latency_ms"] for sample in successful], target_ms)
    else:
        summary = {"count": 0, "p95_ms": None, "target_ms": float(target_ms), "pass": False}
    statuses = {}
    for sample in samples:
        statuses[str(sample["status"])] = statuses.get(str(sample["status"]), 0) + 1
    summary.update({
        "samples": len(samples),
        "status_counts": statuses,
        "transport_errors": sorted({sample.get("error") for sample in samples if sample.get("error")}),
    })
    summary["pass"] = bool(summary["pass"] and successful and len(successful) == len(samples))
    return summary


def coastal_snapshot(body):
    coastal = body.get("coastal") if isinstance(body, dict) else None
    if not isinstance(coastal, dict):
        return {}
    source = coastal.get("source") if isinstance(coastal.get("source"), dict) else {}
    breaker = coastal.get("breaker") if isinstance(coastal.get("breaker"), dict) else {}
    return {"available": source.get("available"), "stale": source.get("stale"),
            "breaker_state": breaker.get("state"), "rejected": breaker.get("rejected")}


def status_snapshot(aggregator_url):
    if not aggregator_url:
        return None
    response = common.http_json(aggregator_url, "/internal/v1/status")
    return {"http_status": response["status"], "error": response["error"],
            "coastal": coastal_snapshot(response["body"])}


def evaluate(phases, outage_confirmed):
    if not outage_confirmed:
        return {"result": "BLOCKED", "reasons": ["outage was not simulated or the sensor did not reach 0 ready replicas"]}
    reasons = []
    for name in ("baseline", "during_outage", "after_recovery"):
        phase = phases.get(name, {})
        if not phase.get("samples"):
            reasons.append(name + " has no samples")
        elif not phase.get("pass"):
            reasons.append(f"{name} p95={phase.get('p95_ms')} ms or HTTP failures")
    return {"result": "FAIL" if reasons else "PASS", "reasons": reasons}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--client-api-url", required=True, help="Client API base URL")
    parser.add_argument("--client-token-env", default="FIELD_TEAM_TOKEN",
                        help="Environment variable holding a Field Team access token")
    parser.add_argument("--client-token-file", help="File holding a Field Team access token")
    parser.add_argument("--aggregator-url", help="Aggregator base URL for an optional coastal status snapshot")
    parser.add_argument("--kubectl", default="kubectl", help="kubectl binary")
    parser.add_argument("--namespace", default="sistem-informasi-kebencanaan-bnpb")
    parser.add_argument("--deployment", default="sensor-pantai")
    parser.add_argument("--restore-replicas", type=int, default=1)
    parser.add_argument("--samples", type=int, default=40)
    parser.add_argument("--interval", type=float, default=0.25, help="Seconds between samples")
    parser.add_argument("--target-ms", type=float, default=300.0, help="p95 target for BMKG reads")
    parser.add_argument("--scale-outage", action="store_true",
                        help="Explicitly scale the sensor to 0 for the outage phase")
    parser.add_argument("--outage-timeout", type=int, default=90)
    parser.add_argument("--output-dir", type=Path, help="Evidence directory")
    args = parser.parse_args()
    if args.samples < 1:
        parser.error("--samples must be at least 1")
    if args.restore_replicas < 1:
        parser.error("--restore-replicas must be at least 1")

    token, token_source = common.resolve_token(args.client_token_env, args.client_token_file,
                                               "Field Team token")
    stamp = datetime.datetime.now(common.UTC).strftime("%Y%m%dT%H%M%SZ")
    folder = (args.output_dir or ROOT / "artifacts/m2/runs" /
              (stamp + "-p5-source-latency-" + secrets.token_hex(3))).resolve()
    folder.mkdir(parents=True, exist_ok=False)

    phases, kubectl_evidence = {}, {}
    scaled = False
    outage_confirmed = False
    try:
        baseline = collect(args.client_api_url, token, args.samples, args.interval)
        phases["baseline"] = summarize_phase(baseline, args.target_ms)
        common.write_json(folder / "baseline.json", baseline)

        if not args.scale_outage:
            kubectl_evidence["scale"] = "skipped: --scale-outage not supplied"
        else:
            original = ready_replicas(args.kubectl, args.namespace, args.deployment)
            kubectl_evidence["original_ready_replicas"] = original
            kubectl_evidence["scale"] = scale(args.kubectl, args.namespace, args.deployment, 0)
            scaled = True
            outage_confirmed = wait_ready(args.kubectl, args.namespace, args.deployment,
                                          0, args.outage_timeout)
            kubectl_evidence["outage_ready_replicas"] = ready_replicas(
                args.kubectl, args.namespace, args.deployment)
            if outage_confirmed:
                phases["status_during_outage"] = status_snapshot(args.aggregator_url)
                during = collect(args.client_api_url, token, args.samples, args.interval)
                phases["during_outage"] = summarize_phase(during, args.target_ms)
                common.write_json(folder / "during-outage.json", during)
    finally:
        if scaled:
            kubectl_evidence["restore"] = scale(args.kubectl, args.namespace, args.deployment,
                                                args.restore_replicas)
            kubectl_evidence["restored"] = wait_ready(args.kubectl, args.namespace,
                                                      args.deployment, args.restore_replicas,
                                                      args.outage_timeout)
            kubectl_evidence["restored_ready_replicas"] = ready_replicas(
                args.kubectl, args.namespace, args.deployment)

    if outage_confirmed:
        after = collect(args.client_api_url, token, args.samples, args.interval)
        phases["after_recovery"] = summarize_phase(after, args.target_ms)
        common.write_json(folder / "after-recovery.json", after)

    evaluation = evaluate(phases, outage_confirmed)
    report = {
        "timestamp": common.isoformat(),
        "client_api_url": args.client_api_url,
        "token_source": token_source,
        "source": "BMKG",
        "target_ms": args.target_ms,
        "scale_outage": args.scale_outage,
        "kubectl": kubectl_evidence,
        "phases": phases,
        "evaluation": evaluation,
    }
    common.write_json(folder / "report.json", report)

    print("\nEvidence:", folder)
    for name, phase in phases.items():
        if isinstance(phase, dict) and "p95_ms" in phase:
            print(f"  {name}: p95={phase['p95_ms']} ms samples={phase.get('samples')} pass={phase.get('pass')}")
    print("RESULT:", evaluation["result"], "; ".join(evaluation["reasons"]))
    return common.EXIT_CODES[evaluation["result"]]


if __name__ == "__main__":
    sys.exit(main())
