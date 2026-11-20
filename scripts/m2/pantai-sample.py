#!/usr/bin/env python3
"""Sample the official Sensor Pantai over HTTP on an isolated Docker container.

This records observed behavior, not P5 acceptance or critical-delivery metrics.
No personal .env, Kubernetes context, existing stack or image is modified.
"""
import argparse
import base64
import datetime
import hashlib
import json
import secrets
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
IMAGE = "sustuna/sensor-pantai@sha256:51fbd90d3891ad6e9f5768ad330ffb65205644cb29e50da65c7538f2e0b4397d"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--seconds", type=int, default=30, help="Sampling window, 1..300 seconds")
    parser.add_argument("--speed", type=int, default=60, help="Virtual sensor speed, 1..60")
    parser.add_argument("--seed", type=int, default=42, help="Practice fixture seed; never used by ingestion logic")
    parser.add_argument("--output-dir", type=Path, help="New evidence directory")
    args = parser.parse_args()
    if not 1 <= args.seconds <= 300 or not 1 <= args.speed <= 60:
        parser.error("seconds must be 1..300 and speed must be 1..60")
    stamp = datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    folder = (args.output_dir or ROOT / "artifacts/m2/runs" / (stamp + "-pantai-" + secrets.token_hex(3))).resolve()
    folder.mkdir(parents=True, exist_ok=False)
    name, token = "tubesaat-pantai-" + secrets.token_hex(5), secrets.token_urlsafe(32)
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        port = sock.getsockname()[1]
    evidence = {"scope": "HTTP_BLACK_BOX_SAMPLE", "image": IMAGE, "profile": "practice", "speed": args.speed,
                "seed": args.seed, "seconds": args.seconds, "p5_acceptance": False, "samples": [], "result": "RUNNING"}

    def command(arguments, check=True):
        print("+ " + " ".join(map(str, arguments)), flush=True)
        return subprocess.run(arguments, check=check, capture_output=True, text=True, timeout=120)

    def sample(path, authenticated=True):
        headers = {"X-Correlation-ID": "pantai-sample-" + secrets.token_hex(8)}
        if authenticated:
            headers["Authorization"] = "Bearer " + token
        request = urllib.request.Request("http://127.0.0.1:" + str(port) + path, headers=headers)
        started = time.monotonic()
        try:
            response = urllib.request.urlopen(request, timeout=3)
        except urllib.error.HTTPError as error:
            response = error
        except OSError as error:
            evidence["samples"].append({"path": path, "status": 0, "failure": type(error).__name__,
                                         "elapsed_ms": round((time.monotonic() - started) * 1000, 3)})
            return None
        with response:
            raw = response.read((1 << 20) + 1)
            truncated = len(raw) > 1 << 20
            captured = raw[:1 << 20].replace(token.encode(), b"[REDACTED]")
            record = {"path": path, "authenticated": authenticated, "status": response.status,
                      "elapsed_ms": round((time.monotonic() - started) * 1000, 3), "truncated": truncated,
                      "captured_sha256": hashlib.sha256(captured).hexdigest(), "raw_base64": base64.b64encode(captured).decode()}
            evidence["samples"].append(record)
            print(f"HTTP {response.status} {path} {record['elapsed_ms']} ms; truncated={truncated}", flush=True)
            try:
                return json.loads(captured) if response.status == 200 and not truncated else None
            except (ValueError, UnicodeError):
                return None

    with tempfile.TemporaryDirectory(prefix="pantai-sample-") as temporary:
        env = Path(temporary) / "sensor.env"
        env.write_text(f"PANTAI_PROFILE=practice\nPANTAI_SPEED={args.speed}\nPANTAI_SEED={args.seed}\nPANTAI_AUTH_TOKEN={token}\n")
        env.chmod(0o600)
        try:
            command(["docker", "run", "-d", "--name", name, "--publish", f"127.0.0.1:{port}:8080", "--env-file", str(env), IMAGE])
            ready_by = time.monotonic() + 15
            while time.monotonic() < ready_by:
                if sample("/healthz", False) == {"status": "ok"}:
                    break
                time.sleep(0.5)
            else:
                raise RuntimeError("sensor did not become ready")
            sample("/v1/stations", False)
            sample("/v1/stations")
            end, cursor = time.monotonic() + args.seconds, None
            while time.monotonic() < end:
                query = {"limit": "100"}
                if cursor:
                    query["cursor"] = cursor
                page = sample("/v1/observations?" + urllib.parse.urlencode(query))
                if isinstance(page, dict) and isinstance(page.get("next_cursor"), str) and page["next_cursor"]:
                    # Follow an opaque cursor for characterization only. This is
                    # not a durable ingestion checkpoint or a repair strategy.
                    cursor = page["next_cursor"]
                time.sleep(min(1, max(0, end - time.monotonic())))
            evidence["result"] = "SAMPLED"
        except (OSError, RuntimeError, subprocess.SubprocessError) as error:
            evidence.update(result="FAIL", failure=type(error).__name__)
        finally:
            logs = command(["docker", "logs", name], check=False)
            (folder / "sensor.log").write_text((logs.stdout + logs.stderr).replace(token, "[REDACTED]"), encoding="utf-8")
            cleanup = command(["docker", "rm", "--force", name], check=False)
            evidence["cleanup"] = "PASS" if cleanup.returncode == 0 else "FAIL"
            if cleanup.returncode:
                evidence["result"] = "FAIL"
            evidence["finished_at"] = datetime.datetime.now(datetime.timezone.utc).isoformat()
            (folder / "pantai-sample.json").write_text(json.dumps(evidence, indent=2) + "\n", encoding="utf-8")
    print(str(folder / "pantai-sample.json"))
    return 0 if evidence["result"] == "SAMPLED" else 1


if __name__ == "__main__":
    raise SystemExit(main())
