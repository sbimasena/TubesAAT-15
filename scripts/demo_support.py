"""Shared local demo helpers; no service business logic or credentials in evidence."""
import base64
import datetime
import json
from pathlib import Path
import subprocess
import tempfile
import time
import urllib.request

settings = dict(line.split("=", 1) for line in Path(".env.example").read_text().splitlines()
                if line and not line.startswith("#") and "=" in line)
compose = ["docker", "compose", "--env-file", ".env.example"]


def run(*args):
    return subprocess.check_output(compose + list(args), text=True, stderr=subprocess.STDOUT).strip()


def now():
    return datetime.datetime.now(datetime.timezone.utc).isoformat()


def sql(query):
    return run("exec", "-T", "canonical-store", "psql", "-U", settings["POSTGRES_USER"],
               "-d", settings["POSTGRES_DB"], "-At", "-c", query)


def broker(path, body=None):
    credentials = settings["RABBITMQ_DEFAULT_USER"] + ":" + settings["RABBITMQ_DEFAULT_PASS"]
    request = urllib.request.Request("http://127.0.0.1:" + settings["RABBITMQ_MANAGEMENT_PORT"] + "/api/" + path,
        data=None if body is None else json.dumps(body).encode(),
        headers={"Authorization": "Basic " + base64.b64encode(credentials.encode()).decode(),
                 "Content-Type": "application/json"})
    with urllib.request.urlopen(request, timeout=5) as response:
        return json.load(response)


def wait_for(check, description):
    for _ in range(30):
        try:
            if check():
                return
        except (OSError, subprocess.CalledProcessError):
            # Startup/restart can briefly refuse network requests or container execs.
            pass
        time.sleep(2)
    raise AssertionError(description)


def healthy(service):
    return subprocess.run(compose + ["exec", "-T", service, "/service", "--healthcheck"],
                          stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode == 0


def states():
    result = {}
    for line in run("ps", "--all", "--format", "{{.Service}} {{.ID}}").splitlines():
        service, identity = line.split()
        state = json.loads(subprocess.check_output(
            ["docker", "inspect", "--format", "{{json .State}}", identity], text=True))
        result[service] = {"id": identity, "running": state["Running"], "started_at": state["StartedAt"]}
    return result


def journal(service):
    with tempfile.TemporaryDirectory(prefix="tubesaat-stage3-") as folder:
        path = Path(folder) / "events.jsonl"
        run("cp", service + ":/data/events.jsonl", str(path))
        # A copy while the writer runs may include a partial last line; don't treat it as committed.
        data = path.read_bytes()
        return [json.loads(line) for line in data[:data.rfind(b"\n") + 1].splitlines()]


def received(service, message_id):
    return [r for r in journal(service) if r["message_id"] == str(message_id)]


def logs(service, since):
    records = []
    for line in run("logs", "--no-log-prefix", "--since", since, service).splitlines():
        try:
            records.append(json.loads(line))
        except json.JSONDecodeError:
            pass
    return records

