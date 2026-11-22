#!/usr/bin/env python3
"""P5.4: circuit-breaker timeline and Client API behaviour during a Pantai outage.

Reads coastal.breaker from /internal/v1/status, polls it through baseline,
outage and recovery, and optionally measures the Field Team Client API. Scaling
the sensor is opt-in via --scale-outage and is always restored in a finally
block. Without the opt-in the result is BLOCKED, not PASS.
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


def breaker_from_status(body):
    coastal = body.get("coastal") if isinstance(body, dict) else None
    if not isinstance(coastal, dict):
        return None
    breaker = coastal.get("breaker")
    return breaker if isinstance(breaker, dict) else None


def source_from_status(body):
    coastal = body.get("coastal") if isinstance(body, dict) else None
    if not isinstance(coastal, dict):
        return {}
    source = coastal.get("source")
    return source if isinstance(source, dict) else {}


def summarize_timeline(timeline, client_target_ms):
    breaker_entries = [entry for entry in timeline if isinstance(entry.get("breaker"), dict)]
    states = [entry["breaker"].get("state") for entry in breaker_entries]

    def phase(label):
        return [entry["breaker"].get("state") for entry in breaker_entries if entry.get("label") == label]

    # Judge per phase: a later 503 burst from the sensor itself may legitimately reopen the
    # breaker, so recovery means "closed once the sensor is back", not "closed at the last poll".
    opened = "open" in phase("outage")
    recovered = bool(opened and "closed" in phase("recovery"))
    first = breaker_entries[0]["breaker"] if breaker_entries else {}
    last = breaker_entries[-1]["breaker"] if breaker_entries else {}
    rejected_delta = (last.get("rejected") or 0) - (first.get("rejected") or 0)
    attempts_delta = (last.get("attempts") or 0) - (first.get("attempts") or 0)
    retries_delta = (last.get("retries") or 0) - (first.get("retries") or 0)
    client_entries = [entry for entry in timeline if entry.get("client_api_status") is not None]
    client_latencies = [entry["client_api_latency_ms"] for entry in client_entries
                        if entry.get("client_api_status") == 200]
    client_ok = bool(client_entries) and all(entry["client_api_status"] == 200 for entry in client_entries)
    client_p95 = common.percentile(client_latencies, 0.95) if client_latencies else None
    reasons = []
    if not opened:
        reasons.append("breaker never reached open")
    if not recovered:
        reasons.append("breaker did not return to closed")
    if rejected_delta <= 0:
        reasons.append("rejected counter did not increase")
    if client_entries and not client_ok:
        reasons.append("Client API returned a non-200 status")
    if client_p95 is not None and client_p95 >= client_target_ms:
        reasons.append(f"Client API p95 {client_p95:.1f} ms is at or above {client_target_ms} ms")
    if reasons:
        result = "FAIL"
    elif not client_entries:
        result = "PARTIAL"
        reasons = ["Client API was not measured"]
    else:
        result = "PASS"
    return {
        "result": result,
        "reasons": reasons,
        "breaker_states": states,
        "opened": opened,
        "recovered": recovered,
        "rejected_delta": rejected_delta,
        "attempts_delta": attempts_delta,
        "retries_delta": retries_delta,
        "client_samples": len(client_entries),
        "client_p95_ms": round(client_p95, 3) if client_p95 is not None else None,
        "client_target_ms": client_target_ms,
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--aggregator-url", required=True, help="Aggregator base URL")
    parser.add_argument("--client-api-url", help="Client API base URL for Field Team reads")
    common.add_client_auth_args(parser)
    parser.add_argument("--client-target-ms", type=float, default=1000.0)
    parser.add_argument("--kubectl", default="kubectl")
    parser.add_argument("--namespace", default="sistem-informasi-kebencanaan-bnpb")
    parser.add_argument("--deployment", default="sensor-pantai")
    parser.add_argument("--restore-replicas", type=int, default=1)
    parser.add_argument("--poll-interval", type=float, default=5.0)
    parser.add_argument("--baseline-polls", type=int, default=5)
    parser.add_argument("--outage-polls", type=int, default=10)
    parser.add_argument("--recovery-polls", type=int, default=10)
    parser.add_argument("--scale-outage", action="store_true",
                        help="Explicitly scale the sensor to 0 for the outage phase")
    parser.add_argument("--outage-timeout", type=int, default=90)
    parser.add_argument("--output-dir", type=Path, help="Evidence directory")
    args = parser.parse_args()
    if min(args.baseline_polls, args.outage_polls, args.recovery_polls) < 1:
        parser.error("poll counts must be at least 1")

    session, token_source = common.client_session(args) if args.client_api_url else (None, None)

    stamp = datetime.datetime.now(common.UTC).strftime("%Y%m%dT%H%M%SZ")
    folder = (args.output_dir or ROOT / "artifacts/m2/runs" /
              (stamp + "-p5-breaker-timeline-" + secrets.token_hex(3))).resolve()
    folder.mkdir(parents=True, exist_ok=False)

    timeline = []

    def poll(label):
        status = common.http_json(args.aggregator_url, "/internal/v1/status")
        breaker = breaker_from_status(status["body"])
        source = source_from_status(status["body"])
        entry = {
            "timestamp": common.isoformat(),
            "label": label,
            "status_http": status["status"],
            "status_error": status["error"],
            "status_latency_ms": status["latency_ms"],
            "coastal_available": source.get("available"),
            "coastal_stale": source.get("stale"),
            "stale_since": source.get("stale_since"),
            "breaker": breaker,
        }
        if args.client_api_url:
            query = urllib.parse.urlencode({"source": "SENSOR_PANTAI", "limit": "5"})
            client = session.get(args.client_api_url, "/hazards?" + query)
            entry.update({"client_api_status": client["status"],
                          "client_api_latency_ms": client["latency_ms"],
                          "client_api_error": client["error"]})
        timeline.append(entry)
        common.write_json(folder / "timeline.json", timeline)
        state = breaker.get("state") if breaker else None
        print(f"[{label}] status={status['status']} coastal={source.get('available')}/"
              f"{source.get('stale')} breaker={state}")
        return entry

    kubectl_evidence, outage_confirmed, scaled = {}, False, False
    try:
        for _ in range(args.baseline_polls):
            poll("baseline")
            time.sleep(args.poll_interval)
        if not args.scale_outage:
            kubectl_evidence["scale"] = "skipped: --scale-outage not supplied"
        else:
            kubectl_evidence["original_ready_replicas"] = ready_replicas(
                args.kubectl, args.namespace, args.deployment)
            kubectl_evidence["scale"] = scale(args.kubectl, args.namespace, args.deployment, 0)
            scaled = True
            outage_confirmed = wait_ready(args.kubectl, args.namespace, args.deployment,
                                          0, args.outage_timeout)
            kubectl_evidence["outage_ready_replicas"] = ready_replicas(
                args.kubectl, args.namespace, args.deployment)
            if outage_confirmed:
                for _ in range(args.outage_polls):
                    poll("outage")
                    time.sleep(args.poll_interval)
    finally:
        if scaled:
            kubectl_evidence["restore"] = scale(args.kubectl, args.namespace, args.deployment,
                                                args.restore_replicas)
            kubectl_evidence["restored"] = wait_ready(args.kubectl, args.namespace,
                                                      args.deployment, args.restore_replicas,
                                                      args.outage_timeout)
            kubectl_evidence["restored_ready_replicas"] = ready_replicas(
                args.kubectl, args.namespace, args.deployment)

    if outage_confirmed and kubectl_evidence.get("restored"):
        for _ in range(args.recovery_polls):
            poll("recovery")
            time.sleep(args.poll_interval)

    summary = summarize_timeline(timeline, args.client_target_ms)
    if not outage_confirmed:
        summary = {"result": "BLOCKED",
                   "reasons": ["outage was not simulated or the sensor did not reach 0 ready replicas"],
                   "breaker_states": summary["breaker_states"]}
    report = {
        "timestamp": common.isoformat(),
        "aggregator_url": args.aggregator_url,
        "client_api_url": args.client_api_url,
        "token_source": token_source,
        "relogins": session.relogins if session else 0,
        "scale_outage": args.scale_outage,
        "kubectl": kubectl_evidence,
        "summary": summary,
        "timeline": timeline,
    }
    common.write_json(folder / "report.json", report)

    print("\nEvidence:", folder)
    print("Breaker states:", summary.get("breaker_states"))
    print("RESULT:", summary["result"], "; ".join(summary["reasons"]))
    return common.EXIT_CODES[summary["result"]]


if __name__ == "__main__":
    sys.exit(main())
