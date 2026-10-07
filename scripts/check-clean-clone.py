#!/usr/bin/env python3
"""Build/boot nine services from a clean committed checkout with disposable credentials.

Uses only the base Compose file, isolated ports/volumes, and preserves existing projects.
"""
import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import secrets
import subprocess
import tempfile
import time

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--revision", default="HEAD", help="Committed revision to verify")
parser.add_argument("--evidence-dir", type=Path, default=Path("docs/evidence/anggota-c/integrasi/clean-clone"))
parser.add_argument("--project-name", help="Existing project to observe; default observes all existing Compose containers")
args = parser.parse_args()
folder = args.evidence_dir.resolve()
folder.mkdir(parents=True, exist_ok=True)
checker = Path("scripts/check-deployment.py").resolve()


def now():
    return datetime.datetime.now(datetime.timezone.utc).isoformat()


def existing_states():
    label = "com.docker.compose.project" + ("=" + args.project_name if args.project_name else "")
    result = subprocess.run(["docker", "ps", "-aq", "--filter", "label=" + label], capture_output=True, text=True, check=True)
    ids = result.stdout.split()
    if not ids:
        return {}
    records = json.loads(subprocess.run(["docker", "inspect", *ids], capture_output=True, text=True, check=True).stdout)
    return {r["Id"]: {"running": r["State"]["Running"], "started_at": r["State"]["StartedAt"]} for r in records}


def wait_for(check, description):
    for _ in range(30):
        if check():
            return
        time.sleep(2)
    raise AssertionError(description)


evidence = {"started_at": now(), "existing_states_before": existing_states(),
            "verification_tool_sha256": hashlib.sha256(checker.read_bytes()).hexdigest()}
with tempfile.TemporaryDirectory(prefix="tubesaat-clean-", dir="/private/tmp") as temporary:
    root = Path(temporary)
    clone = root / "repo"
    subprocess.run(["git", "clone", "--local", "--no-hardlinks", ".", str(clone)], capture_output=True, text=True, check=True)
    revision = subprocess.run(["git", "rev-parse", args.revision + "^{commit}"], capture_output=True, text=True, check=True).stdout.strip()
    subprocess.run(["git", "checkout", "--detach", revision], cwd=clone, capture_output=True, text=True, check=True)
    evidence["commit"] = revision
    evidence["source"] = "COMMITTED_CHECKOUT_WITHOUT_PATCH"
    evidence["clean_status_before"] = subprocess.run(["git", "status", "--porcelain"], cwd=clone,
                                                   capture_output=True, text=True, check=True).stdout.strip()
    assert not evidence["clean_status_before"]
    values = {}
    for line in (clone / ".env.example").read_text().splitlines():
        if line and not line.startswith("#") and "=" in line:
            key, value = line.split("=", 1)
            values[key] = value
    for key in ("JWT_SIGNING_SECRET", "AUTH_INTERNAL_SECRET", "MEDIA_CLIENT_PASSWORD", "FIELD_TEAM_CLIENT_PASSWORD",
                "INTERNAL_OPS_CLIENT_PASSWORD", "POSTGRES_PASSWORD", "RABBITMQ_DEFAULT_PASS", "BMKG_API_KEY", "PVMBG_TOKEN"):
        values[key] = secrets.token_urlsafe(32)
    values.update(BMKG_PORT="28081", PVMBG_PORT="28082", AUTH_PORT="28084", CLIENT_API_PORT="28080",
                  RABBITMQ_MANAGEMENT_PORT="35672", ENABLE_PROVISIONAL_HAZARD_ENDPOINT="false",
                  ACCESS_TOKEN_TTL_SECONDS="60", REFRESH_TOKEN_TTL_SECONDS="3600")
    values["DATABASE_URL"] = "postgres://" + values["POSTGRES_USER"] + ":" + values["POSTGRES_PASSWORD"] + "@canonical-store:5432/" + values["POSTGRES_DB"] + "?sslmode=disable"
    values["BROKER_URL"] = "amqp://" + values["RABBITMQ_DEFAULT_USER"] + ":" + values["RABBITMQ_DEFAULT_PASS"] + "@message-broker:5672/"
    environment_file = root / "clone.env"
    environment_file.write_text("\n".join(k + "=" + v for k, v in values.items()) + "\n")
    environment_file.chmod(0o600)
    env = {k: v for k, v in os.environ.items() if k not in values and k not in ("COMPOSE_FILE", "COMPOSE_PROJECT_NAME")}
    project = root.name.replace("_", "x")
    evidence["compose_project"] = project
    evidence["host_ports"] = {k: int(values[k]) for k in ("BMKG_PORT", "PVMBG_PORT", "AUTH_PORT", "CLIENT_API_PORT", "RABBITMQ_MANAGEMENT_PORT")}
    command = ["docker", "compose", "--env-file", str(environment_file), "--project-name", project, "-f", "docker-compose.yml"]

    def redact(text):
        for key, value in values.items():
            if value and (key.endswith(("_SECRET", "_PASSWORD", "_PASS", "_TOKEN", "_KEY")) or key in ("DATABASE_URL", "BROKER_URL")):
                text = text.replace(value, "[REDACTED]")
        return text

    def run(*parts):
        result = subprocess.run(command + list(parts), cwd=clone, env=env, capture_output=True, text=True)
        if result.returncode:
            raise RuntimeError("Clone Compose operation failed: " + parts[0] + "; inspect sanitized build/diagnostic logs")
        return result.stdout.strip()

    def sql(query):
        return run("exec", "-T", "canonical-store", "psql", "-U", values["POSTGRES_USER"], "-d", values["POSTGRES_DB"], "-At", "-c", query)

    def journal(service):
        path = root / (service + ".jsonl")
        run("cp", service + ":/data/events.jsonl", str(path))
        data = path.read_bytes()
        return [json.loads(line) for line in data[:data.rfind(b"\n") + 1].splitlines()]

    try:
        build = subprocess.run(command + ["up", "--build", "-d", "--wait", "--wait-timeout", "120"],
                               cwd=clone, env=env, capture_output=True, text=True)
        (folder / "build.log").write_text(redact(build.stdout + build.stderr))
        assert build.returncode == 0, "clean clone startup failed; inspect build.log"
        # Run the current verification tool against committed service source, not copied application files.
        deployment_file = folder / "deployment.json"
        result = subprocess.run(["python3", "-B", str(checker), "--env-file", str(environment_file),
                                 "--project-name", project, "--output", str(deployment_file)],
                                cwd=clone, env=env, capture_output=True, text=True)
        assert result.returncode == 0, "clone deployment verification failed: " + redact(result.stderr)
        evidence["deployment"] = json.loads(deployment_file.read_text())
        def published():
            value = sql("SELECT row_to_json(t) FROM (SELECT message_id::text,hazard_id,hazard_revision,correlation_id,payload FROM hazard_outbox WHERE published_at IS NOT NULL ORDER BY message_id LIMIT 1) t")
            if value:
                evidence["producer"] = json.loads(value)
                return True
            return False
        wait_for(published, "fresh clone must publish an outbox snapshot")
        evidence["consumers"] = {}
        for service in ("notification-consumer", "dashboard-consumer"):
            def received():
                matches = [r for r in journal(service) if r["message_id"] == evidence["producer"]["message_id"]]
                if matches:
                    assert len(matches) == 1
                    for field in ("hazard_id", "hazard_revision", "correlation_id", "payload"):
                        assert matches[0][field] == evidence["producer"][field]
                    evidence["consumers"][service] = matches[0]
                    return True
                return False
            wait_for(received, service + " must receive the same committed event")
        evidence["clean_status_after"] = subprocess.run(["git", "status", "--porcelain"], cwd=clone,
                                                        capture_output=True, text=True, check=True).stdout.strip()
        assert not evidence["clean_status_after"]
        evidence["result"] = "PASS"
    except Exception as error:
        evidence["result"] = "FAIL"
        evidence["failure"] = redact(str(error))
        diagnostics = run("logs", "--no-log-prefix", "--tail", "100", "message-broker")
        (folder / "diagnostics.log").write_text(redact(diagnostics))
        raise
    finally:
        try:
            evidence["cleanup"] = run("down", "--volumes", "--remove-orphans")
            evidence["existing_states_after"] = existing_states()
            assert evidence["existing_states_after"] == evidence["existing_states_before"], "existing project containers changed"
            evidence["cleanup_result"] = "PASS"
        except Exception as error:
            evidence["result"] = "FAIL"
            evidence["cleanup_result"] = "FAIL"
            evidence["cleanup_failure"] = redact(str(error))
            raise
        finally:
            evidence["finished_at"] = now()
            (folder / "clean-clone.json").write_text(redact(json.dumps(evidence, indent=2)) + "\n")
print("PASS: clean committed checkout boots nine healthy services; protected queries; matching fanout; isolated cleanup")
print(folder / "clean-clone.json")
