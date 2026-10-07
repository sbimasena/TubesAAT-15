#!/usr/bin/env python3
"""Check P3 upstream credential isolation without changing service state."""
import argparse
from datetime import datetime, timezone
import json
from pathlib import Path
import subprocess
import urllib.error
import urllib.request
import uuid
from checker_progress import progress


def check_upstreams(services, checks=None):
    if checks is None:
        checks = []
    bmkg_key = services["bmkg"]["environment"]["BMKG_API_KEY"]
    pvmbg_token = services["pvmbg"]["environment"]["PVMBG_TOKEN"]
    if not bmkg_key or not pvmbg_token or bmkg_key == pvmbg_token:
        raise ValueError("Upstream credentials must be nonempty and distinct")
    # Never follow redirects carrying authentication headers.
    class NoRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, req, fp, code, msg, headers, newurl):
            return None

    opener = urllib.request.build_opener(NoRedirect)
    for service, paths, header, own, foreign, foreign_header in (
        ("bmkg", ("/seismic-events", "/tsunami-warnings"), "X-BMKG-Key",
         bmkg_key, pvmbg_token, {"Authorization": "Bearer " + pvmbg_token}),
        ("pvmbg", ("/volcanic-reports",), "Authorization",
         "Bearer " + pvmbg_token, "Bearer " + bmkg_key, {"X-BMKG-Key": bmkg_key}),
    ):
        port = services[service]["ports"][0]["published"]
        for path in paths:
            for name, headers in (
                ("own credential", {header: own}),
                ("foreign credential in native header", {header: foreign}),
                ("foreign trust-domain header", foreign_header),
                ("missing credential", {}),
            ):
                progress(f"Request: {service} {path}; {name}")
                correlation = "cross-credential-" + uuid.uuid4().hex
                request = urllib.request.Request(
                    f"http://127.0.0.1:{port}{path}",
                    headers={**headers, "X-Correlation-ID": correlation},
                )
                try:
                    response = opener.open(request, timeout=10)
                except urllib.error.HTTPError as error:
                    response = error
                with response:
                    status = response.status
                    checks.append({"service": service, "path": path, "case": name,
                                   "status": status, "correlation_id": correlation})
                    progress(f"Response: {service} {path}; {name}; HTTP {status}")
                    expected = (200,) if name == "own credential" else (401, 403)
                    if status not in expected:
                        raise ValueError(f"{service} {path}: {name} returned {status}; expected {expected}")
                    if name == "own credential" and not isinstance(json.load(response), list):
                        raise ValueError(f"{service} {path}: expected a JSON array")
    return checks


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--env-file", default=".env")
    parser.add_argument("--project-name")
    parser.add_argument("--output", type=Path, help="Save statuses only; no credentials or response bodies")
    args = parser.parse_args()
    progress("Starting configuration and prerequisite checks")
    evidence = {"started_at": datetime.now(timezone.utc).isoformat(), "checks": []}
    command = ["docker", "compose", "--env-file", str(Path(args.env_file).resolve())]
    if args.project_name:
        command += ["--project-name", args.project_name]
    command += ["-f", "docker-compose.yml", "config", "--format", "json"]
    try:
        config = subprocess.run(command, capture_output=True, text=True, timeout=30)
        if config.returncode:
            raise ValueError("Invalid Compose configuration; inspect the selected environment locally")
        check_upstreams(json.loads(config.stdout)["services"], evidence["checks"])
        evidence["result"] = "PASS"
    except Exception:
        # Configuration, HTTP errors and payloads may contain secrets. Keep failures generic.
        evidence["result"] = "FAIL"
        evidence["failure"] = "Credential isolation check failed; verify configuration, upstream availability, and rejection behavior locally"
    evidence["finished_at"] = datetime.now(timezone.utc).isoformat()
    if args.output:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(json.dumps(evidence, indent=2) + "\n")
        progress("Result file saved")
    progress("Credential isolation checks complete")
    print(json.dumps(evidence, indent=2))
    return 0 if evidence["result"] == "PASS" else 1


if __name__ == "__main__":
    raise SystemExit(main())
