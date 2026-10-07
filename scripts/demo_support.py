"""Shared local demo helpers; no service business logic or credentials in evidence."""
import argparse
import base64
import datetime
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import urllib.request
import urllib.error
import uuid
from checker_progress import progress as _progress, heartbeat

parser = argparse.ArgumentParser(add_help=False)
parser.add_argument("--env-file", default=".env")
parser.add_argument("--project-name")
parser.add_argument("--evidence-dir", help="Directory for new demo evidence; preserves historical defaults when omitted")
options, remaining = parser.parse_known_args()
# Leave each checker its own flags while sharing the operator configuration.
sys.argv[1:] = remaining
compose = ["docker", "compose", "--env-file", str(Path(options.env_file).resolve())]
if options.project_name:
    compose += ["--project-name", options.project_name]
compose += ["-f", "docker-compose.yml", "-f", "tests/compose-operator.yml"]
_progress("Reading selected Compose configuration")
result = subprocess.run(compose + ["config", "--format", "json"], capture_output=True, text=True)
if result.returncode:
    raise SystemExit("Invalid demo configuration: fill the local environment file; inspect Compose locally.")
config = json.loads(result.stdout)
settings = {key: str(value) for service in config["services"].values()
            for key, value in service.get("environment", {}).items() if value is not None}
for service, variable in (("bmkg", "BMKG_PORT"), ("pvmbg", "PVMBG_PORT"),
                          ("aggregator", "AGGREGATOR_PORT"), ("auth", "AUTH_PORT"),
                          ("client-api", "CLIENT_API_PORT"), ("message-broker", "RABBITMQ_MANAGEMENT_PORT")):
    settings[variable] = str(config["services"][service]["ports"][0]["published"])


def redact(text):
    for key, value in settings.items():
        if value and (key.endswith(("_SECRET", "_PASSWORD", "_PASS", "_TOKEN", "_KEY")) or key in ("DATABASE_URL", "BROKER_URL")):
            text = text.replace(value, "[REDACTED]")
    return text


def progress(message):
    _progress(redact(message))


progress("Selected project: " + config.get("name", options.project_name or "Compose default"))


def run(*args):
    if args and args[0] in ("up", "stop", "start", "restart", "build", "rm", "down"):
        progress("Compose " + args[0] + ": running selected lifecycle operation")
    result = subprocess.run(compose + list(args), capture_output=True, text=True)
    if result.returncode:
        raise subprocess.CalledProcessError(result.returncode, compose + list(args),
                                            output=redact(result.stdout), stderr=redact(result.stderr))
    return redact(result.stdout).strip()


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
    for attempt in range(30):
        heartbeat(redact(description), attempt)
        try:
            if check():
                progress("Ready: " + description)
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


def http_call(port, path, body=None, token=None):
    correlation = "c-check-" + uuid.uuid4().hex
    headers = {"X-Correlation-ID": correlation}
    if token:
        headers["Authorization"] = "Bearer " + token
    if body is not None:
        headers["Content-Type"] = "application/json"
    request = urllib.request.Request("http://127.0.0.1:" + str(port) + path,
        data=None if body is None else json.dumps(body).encode(), headers=headers)
    started = time.monotonic()
    try:
        response = urllib.request.urlopen(request, timeout=15)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        assert response.headers.get("X-Correlation-ID") == correlation
        payload = response.read()
        return {"status": response.status, "correlation_id": correlation,
                "body": json.loads(payload) if payload and response.headers.get_content_type() == "application/json" else None,
                "elapsed_ms": (time.monotonic() - started) * 1000}


sessions = {}


def login(role):
    response = http_call(settings["AUTH_PORT"], "/login", {
        "client_id": settings[role + "_CLIENT_ID"], "password": settings[role + "_CLIENT_PASSWORD"]})
    assert response["status"] == 200, role + " login failed"
    pair = response["body"]
    sessions[role] = (pair, time.monotonic() + pair["expires_in"] - 5)
    return pair["access_token"]


def client_read(path, role="FIELD_TEAM"):
    if role not in sessions:
        login(role)
    pair, expiry = sessions[role]
    if time.monotonic() >= expiry:
        response = http_call(settings["AUTH_PORT"], "/refresh", {"refresh_token": pair["refresh_token"]})
        assert response["status"] == 200, role + " refresh failed"
        pair = response["body"]
        sessions[role] = (pair, time.monotonic() + pair["expires_in"] - 5)
    return http_call(settings["CLIENT_API_PORT"], path, token=pair["access_token"])
