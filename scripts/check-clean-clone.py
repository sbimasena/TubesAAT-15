#!/usr/bin/env python3
"""Build/boot committed HEAD in a clean local clone and isolated Compose project.

Requires free localhost ports 18081/18082/18083/25672. Removes only its own
temporary containers/networks/volumes; main development volumes are untouched.
"""
import json
import argparse
import hashlib
import os
from pathlib import Path
import subprocess
import tempfile
import urllib.request
from demo_support import now, settings, states, wait_for

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--with-working-broker-fix", action="store_true",
                    help="test the uncommitted startup fix explicitly; result is NOT an unchanged HEAD check")
args = parser.parse_args()
folder = Path("docs/evidence/anggota-c/stage-5")
folder.mkdir(parents=True, exist_ok=True)
services = ["bmkg", "pvmbg", "canonical-store", "message-broker", "aggregator",
            "notification-consumer", "dashboard-consumer"]
evidence = {"started_at": now(), "main_states_before": states()}
with tempfile.TemporaryDirectory(prefix="tubesaat-clean-", dir="/private/tmp") as temporary:
    clone = Path(temporary) / "repo"
    subprocess.check_call(["git", "clone", "--local", "--no-hardlinks", ".", str(clone)])
    evidence["commit"] = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=clone, text=True).strip()
    evidence["clean_status"] = subprocess.check_output(["git", "status", "--porcelain"], cwd=clone, text=True).strip()
    assert not evidence["clean_status"]
    if args.with_working_broker_fix:
        relative = Path("infrastructure/message-broker/start-with-topology.sh")
        data = relative.read_bytes()
        (clone / relative).write_bytes(data)
        evidence["working_tree_patch"] = {"file": str(relative), "sha256": hashlib.sha256(data).hexdigest(),
                                         "clone_status": subprocess.check_output(
                                             ["git", "status", "--porcelain"], cwd=clone, text=True).strip()}
    project = Path(temporary).name.replace("_", "x")
    evidence["compose_project"] = project
    evidence["host_ports"] = {"bmkg": 18081, "pvmbg": 18082, "aggregator": 18083, "broker_management": 25672}
    env = dict(os.environ, BMKG_PORT="18081", PVMBG_PORT="18082", AGGREGATOR_PORT="18083",
               RABBITMQ_MANAGEMENT_PORT="25672")
    command = ["docker", "compose", "--env-file", ".env.example", "-p", project]

    def run(*args):
        return subprocess.check_output(command + list(args), cwd=clone, env=env,
                                       text=True, stderr=subprocess.STDOUT).strip()

    def request(endpoint):
        req = urllib.request.Request("http://127.0.0.1:18083" + endpoint,
                                     headers={"X-Correlation-ID": "clean-clone-review"})
        with urllib.request.urlopen(req, timeout=5) as response:
            assert response.status == 200
            return json.load(response)

    def ready():
        response = request("/health")
        return response["storage"]["available"] and all(s["available"] for s in response["sources"].values())

    def journal(service):
        path = Path(temporary) / (service + ".jsonl")
        run("cp", service + ":/data/events.jsonl", str(path))
        data = path.read_bytes()
        return [json.loads(line) for line in data[:data.rfind(b"\n") + 1].splitlines()]

    try:
        evidence["declared_services"] = run("config", "--services").splitlines()
        with (folder / "clean-clone-build.log").open("w") as output:
            subprocess.run(command + ["up", "--build", "-d"] + services, cwd=clone, env=env,
                           stdout=output, stderr=subprocess.STDOUT, check=True)
        wait_for(ready, "fresh clone should ingest both sources into its new database")
        evidence["health"] = request("/health")
        evidence["api"] = request("/internal/v1/hazards?limit=100")
        assert {r["source"] for r in evidence["api"]["data"]} == {"BMKG", "PVMBG"}
        # Match a confirmed producer snapshot to both independent persistent journals.
        query = "SELECT row_to_json(t) FROM (SELECT message_id::text, hazard_id, correlation_id, payload FROM hazard_outbox WHERE published_at IS NOT NULL ORDER BY message_id LIMIT 1) t"

        def published():
            value = run("exec", "-T", "canonical-store", "psql", "-U", settings["POSTGRES_USER"],
                        "-d", settings["POSTGRES_DB"], "-At", "-c", query)
            if value:
                evidence["producer"] = json.loads(value)
                return True
            return False

        wait_for(published, "fresh clone should publish outbox events")
        evidence["consumers"] = {}
        for service in ("notification-consumer", "dashboard-consumer"):
            def received():
                matches = [r for r in journal(service) if r["message_id"] == evidence["producer"]["message_id"]]
                if matches:
                    assert len(matches) == 1
                    evidence["consumers"][service] = matches[0]
                    return True
                return False
            wait_for(received, service + " should receive the same producer event")
            for field in ("hazard_id", "correlation_id", "payload"):
                assert evidence["consumers"][service][field] == evidence["producer"][field]
        evidence["services"] = [json.loads(line) for line in run("ps", "--format", "json").splitlines()]
        assert sorted(s["Service"] for s in evidence["services"]) == sorted(services)
        assert all(s["State"] == "running" for s in evidence["services"])
        wait_for(lambda: all(json.loads(line).get("Health") in ("", "healthy")
                            for line in run("ps", "--format", "json").splitlines()), "fresh clone services should be healthy")
        evidence["services"] = [json.loads(line) for line in run("ps", "--format", "json").splitlines()]
        evidence["result"] = ("PASS_WITH_EXPLICIT_WORKING_TREE_FIX" if args.with_working_broker_fix else "PASS_C_SCOPE") + "; AUTH_CLIENT_API_PENDING_B"
    except Exception as error:
        evidence["result"] = "FAIL"
        evidence["failure"] = str(error)
        if "message-broker" in run("ps", "--all", "--format", "{{.Service}}").splitlines():
            diagnostics = run("logs", "--no-log-prefix", "--tail", "100", "message-broker")
            for key in ("POSTGRES_PASSWORD", "RABBITMQ_DEFAULT_PASS", "BMKG_API_KEY", "PVMBG_TOKEN", "DATABASE_URL", "BROKER_URL"):
                diagnostics = diagnostics.replace(settings[key], "[REDACTED]")
            evidence["diagnostics_file"] = "clean-clone-failure-" + project + ".log"
            (folder / evidence["diagnostics_file"]).write_text(diagnostics + "\n")
        raise
    finally:
        try:
            # project is generated by this script; never call down on the main stack.
            evidence["cleanup"] = run("down", "--volumes", "--remove-orphans")
            evidence["main_states_after"] = states()
            assert evidence["main_states_after"] == evidence["main_states_before"], "main containers must not restart"
        except Exception as error:
            evidence["result"] = "FAIL"
            evidence["cleanup_failure"] = str(error)
            raise
        finally:
            evidence["finished_at"] = now()
            (folder / "clean-clone.json").write_text(json.dumps(evidence, indent=2) + "\n")
print("PASS: " + ("clone with explicit working-tree broker fix" if args.with_working_broker_fix else "committed clean clone")
      + " builds/boots seven C/A services and fans out to both consumers")
print("Temporary project/volumes removed; main development containers unchanged")
print(folder / "clean-clone.json")
