#!/usr/bin/env python3
"""Verify P3 on an isolated Compose stack; never mutate the operator's stack.

Real PostgreSQL/RabbitMQ checks and live API failures are included. This is
single-node acceptance evidence, not physical Kubernetes/quorum-failover proof.
"""
import argparse
import base64
import datetime
import hashlib
import json
import os
from pathlib import Path
import secrets
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid


ROOT = Path(__file__).resolve().parents[2]
SERVICES = {"bmkg", "pvmbg", "auth", "client-api", "aggregator", "canonical-store",
            "message-broker", "notification-consumer", "dashboard-consumer"}
MODES = {"rename_field": "schema_incompatible", "change_type": "schema_incompatible",
         "drop_required": "schema_incompatible", "broken_json": "malformed_json",
         "oversize_json": "too_large", "deep_json": "too_deep", "invalid_utf8": "invalid_utf8"}
PUBLIC_DLQ_FIELDS = {"message_id", "source", "received_at", "reason", "retryable", "attempts", "status"}


def now():
    return datetime.datetime.now(datetime.timezone.utc).isoformat()


def require(condition, message):
    if not condition:
        raise AssertionError(message)


def literal(value):
    require("\0" not in value, "SQL literal contains NUL")
    return "'" + value.replace("'", "''") + "'"


def wire_items(body):
    """Split raw array items without decoding invalid UTF-8 or nested JSON."""
    body = body.strip()
    require(body.startswith(b"[") and body.endswith(b"]"), "upstream array is incomplete")
    items, start, stack, quoted, escaped = [], 1, [], False, False
    for index, byte in enumerate(body[1:-1], 1):
        if quoted:
            if escaped:
                escaped = False
            elif byte == 92:
                escaped = True
            elif byte == 34:
                quoted = False
        elif byte == 34:
            quoted = True
        elif byte in (91, 123):
            stack.append(byte)
        elif byte in (93, 125):
            require(stack and stack.pop() == {93: 91, 125: 123}[byte], "unbalanced upstream array")
        elif byte == 44 and not stack:
            items.append(body[start:index].strip())
            start = index + 1
    require(not quoted and not stack, "unbalanced upstream item")
    last = body[start:-1].strip()
    if last:
        items.append(last)
    return items


def free_ports(count):
    handles = []
    try:
        for _ in range(count):
            handle = socket.socket()
            handle.bind(("127.0.0.1", 0))
            handles.append(handle)
        return [handle.getsockname()[1] for handle in handles]
    finally:
        for handle in handles:
            handle.close()


class Check:
    def __init__(self, folder, temporary, reports, go_image):
        self.folder, self.temporary = folder, temporary
        self.reports, self.go_image = reports, go_image
        self.project = "tubesaat-p3-" + secrets.token_hex(5)
        self.started = time.monotonic()
        self.values, self.pairs = {}, {}
        self.test_images, self.test_containers = [], []
        self.sensitive = []
        self.evidence = {"started_at": now(), "result": "RUNNING", "project": self.project,
                         "scope": "ISOLATED_COMPOSE_SINGLE_NODE", "physical_cluster_acceptance": False,
                         "tested_source": "WORKSPACE", "checks": {}, "contract_tests": {}}

    def redact(self, text):
        for value in self.sensitive:
            if value:
                text = text.replace(value, "[REDACTED]")
        return text

    def progress(self, message):
        print(self.redact(f"[p3 +{time.monotonic() - self.started:.0f}s] {message}"), flush=True)

    def save(self):
        (self.folder / "p3-check.json").write_text(
            self.redact(json.dumps(self.evidence, indent=2)) + "\n", encoding="utf-8")

    def command(self, args, *, log=None, check=True, timeout=180):
        # Commands contain paths and variable names, never credential values.
        self.progress("+ " + " ".join(map(str, args)))
        result = subprocess.run(args, cwd=ROOT, env=self.environment, capture_output=True,
                                text=True, encoding="utf-8", errors="replace", timeout=timeout)
        output = self.redact(result.stdout + result.stderr)
        if log:
            (self.folder / log).write_text(output, encoding="utf-8")
        if check and result.returncode:
            raise RuntimeError(f"{args[0]} exited {result.returncode}; inspect {log or 'diagnostics.log'}")
        return result

    def compose(self, *args, **kwargs):
        return self.command(self.compose_args + list(args), **kwargs)

    def wait(self, label, probe, seconds=60):
        self.progress("Waiting: " + label)
        end = time.monotonic() + seconds
        last_error = None
        while time.monotonic() < end:
            try:
                value = probe()
                if value:
                    self.progress("PASS: " + label)
                    return value
            except (OSError, RuntimeError) as error:
                last_error = type(error).__name__
            time.sleep(1)
        raise AssertionError(label + (f" ({last_error})" if last_error else ""))

    def prepare(self):
        for line in (ROOT / ".env.example").read_text(encoding="utf-8").splitlines():
            if line and not line.startswith("#") and "=" in line:
                key, value = line.split("=", 1)
                self.values[key] = value
        for key in ("JWT_SIGNING_SECRET", "AUTH_INTERNAL_SECRET", "MEDIA_CLIENT_PASSWORD",
                    "FIELD_TEAM_CLIENT_PASSWORD", "INTERNAL_OPS_CLIENT_PASSWORD", "POSTGRES_PASSWORD",
                    "RABBITMQ_DEFAULT_PASS", "BMKG_API_KEY", "PVMBG_TOKEN", "AUDIT_TOKEN"):
            self.values[key] = secrets.token_urlsafe(32)
            self.sensitive.append(self.values[key])
        port_names = ("BMKG_PORT", "PVMBG_PORT", "AUTH_PORT", "CLIENT_API_PORT", "RABBITMQ_MANAGEMENT_PORT")
        self.values.update(dict(zip(port_names, map(str, free_ports(len(port_names))))))
        # Admin calls emit each fixture record explicitly; disable periodic additions.
        self.values.update(BMKG_GENERATE_INTERVAL_SECONDS="3600", PVMBG_GENERATE_INTERVAL_SECONDS="3600",
                           POLL_INTERVAL_SECONDS="1", PVMBG_DELAY_MS="500", ACCESS_TOKEN_TTL_SECONDS="60",
                           REFRESH_TOKEN_TTL_SECONDS="3600", SATELLITE_TOKEN="", AUTH_INTROSPECTION_CACHE_TTL_MS="0")
        self.values["DATABASE_URL"] = ("postgres://" + self.values["POSTGRES_USER"] + ":" + self.values["POSTGRES_PASSWORD"]
                                       + "@canonical-store:5432/" + self.values["POSTGRES_DB"] + "?sslmode=disable")
        self.values["BROKER_URL"] = ("amqp://" + self.values["RABBITMQ_DEFAULT_USER"] + ":"
                                     + self.values["RABBITMQ_DEFAULT_PASS"] + "@message-broker:5672/")
        self.sensitive.extend((self.values["DATABASE_URL"], self.values["BROKER_URL"]))
        env = self.temporary / "test.env"
        descriptor = os.open(env, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(descriptor, "w", encoding="utf-8") as stream:
            stream.write("\n".join(key + "=" + value for key, value in self.values.items()) + "\n")
        bootstrap = self.temporary / "start-broker.sh"
        bootstrap.write_bytes((ROOT / "infrastructure/message-broker/start-with-topology.sh").read_bytes().replace(b"\r\n", b"\n"))
        override = self.temporary / "override.json"
        override.write_text(json.dumps({"services": {"message-broker": {"volumes": [{
            "type": "bind", "source": str(bootstrap), "target": "/etc/rabbitmq/start-with-topology.sh", "read_only": True}]}}}), encoding="utf-8")
        self.environment = {key: value for key, value in os.environ.items()
                            if key not in self.values and key not in ("COMPOSE_FILE", "COMPOSE_PROJECT_NAME")}
        self.compose_args = ["docker", "compose", "--project-name", self.project, "--env-file", str(env),
                             "-f", str(ROOT / "docker-compose.yml"), "-f", str(override)]
        self.evidence.update(source_commit=self.command(["git", "rev-parse", "HEAD"]).stdout.strip(),
                             configuration={key: self.values[key] for key in (
                                 "POLL_INTERVAL_SECONDS", "PVMBG_DELAY_MS", "BMKG_GENERATE_INTERVAL_SECONDS",
                                 "PVMBG_GENERATE_INTERVAL_SECONDS", "ACCESS_TOKEN_TTL_SECONDS", "AUTH_INTROSPECTION_CACHE_TTL_MS")})
        self.save()

    def sql(self, query):
        result = self.compose("exec", "-T", "canonical-store", "psql", "-U", self.values["POSTGRES_USER"],
                              "-d", self.values["POSTGRES_DB"], "-At", "-v", "ON_ERROR_STOP=1", "-c", query)
        return result.stdout.strip()

    def http(self, service, path, body=None, role=None, raw=False):
        headers = {"X-Correlation-ID": "p3-" + uuid.uuid4().hex}
        if role:
            headers["Authorization"] = "Bearer " + self.access(role)
        if service == "pvmbg" and path.startswith("/volcanic-reports"):
            headers["Authorization"] = "Bearer " + self.values["PVMBG_TOKEN"]
        if service == "broker":
            credentials = self.values["RABBITMQ_DEFAULT_USER"] + ":" + self.values["RABBITMQ_DEFAULT_PASS"]
            headers["Authorization"] = "Basic " + base64.b64encode(credentials.encode()).decode()
        data = None if body is None else json.dumps(body).encode()
        if data is not None:
            headers["Content-Type"] = "application/json"
        key = {"client": "CLIENT_API_PORT", "auth": "AUTH_PORT", "pvmbg": "PVMBG_PORT", "broker": "RABBITMQ_MANAGEMENT_PORT"}[service]
        request = urllib.request.Request("http://127.0.0.1:" + self.values[key] + path, data=data, headers=headers)
        started = time.monotonic()
        try:
            response = urllib.request.urlopen(request, timeout=20)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            payload = response.read((32 << 20) + 1)
            require(len(payload) <= 32 << 20, "response exceeds the evidence capture budget")
            if service == "client":
                require(response.headers.get("X-Correlation-ID") == headers["X-Correlation-ID"], "correlation ID changed")
            return {"status": response.status, "elapsed_ms": round((time.monotonic() - started) * 1000, 3),
                    "body": payload if raw else (json.loads(payload) if payload.startswith((b"{", b"[")) else None)}

    def access(self, role):
        pair = self.pairs.get(role)
        if pair and time.monotonic() < pair["refresh_at"]:
            return pair["access_token"]
        if pair:
            response = self.http("auth", "/refresh", {"refresh_token": pair["refresh_token"]})
        else:
            prefix = role.upper().replace("-", "_")
            response = self.http("auth", "/login", {"client_id": self.values[prefix + "_CLIENT_ID"],
                                                     "password": self.values[prefix + "_CLIENT_PASSWORD"]})
        require(response["status"] == 200, role + " token acquisition failed")
        pair = response["body"]
        self.sensitive.extend((pair["access_token"], pair["refresh_token"]))
        pair["refresh_at"] = time.monotonic() + pair["expires_in"] - 5
        self.pairs[role] = pair
        return pair["access_token"]

    def states(self):
        ids = self.compose("ps", "--all", "-q").stdout.split()
        records = json.loads(self.command(["docker", "inspect", *ids]).stdout)
        return {record["Config"]["Labels"]["com.docker.compose.service"]:
                {"id": record["Id"], "started_at": record["State"]["StartedAt"],
                 "restart_count": record["RestartCount"], "running": record["State"]["Running"]} for record in records}

    def existing_states(self):
        ids = self.command(["docker", "ps", "--all", "--quiet", "--filter", "label=com.docker.compose.project"]).stdout.split()
        if not ids:
            return {}
        records = json.loads(self.command(["docker", "inspect", *ids]).stdout)
        return {record["Id"]: {"running": record["State"]["Running"], "started_at": record["State"]["StartedAt"]}
                for record in records if record["Config"]["Labels"].get("com.docker.compose.project") != self.project}

    def contract_tests(self):
        suites = {
            "aggregator": ("storage", ["./internal/deadletter", "./internal/source", "./internal/ingest", "./internal/reporting", "./internal/store"],
                           {"TEST_DATABASE_URL": self.values["DATABASE_URL"]},
                           {"TestPVMBGPostgresCheckpointWaitsForDeadLetter", "TestPVMBGReplayKeepsSourceOrder", "TestParkReportPreservesBytesAndDeduplicates", "TestReportReplicationRequiresAllTargets"}),
            "client-api": ("events", ["./internal/httpapi", "./internal/aggregator", "./internal/reportqueue"],
                           {"TEST_FIELD_REPORT_BROKER_URL": self.values["BROKER_URL"]},
                           {"TestPublisherKeepsSessionAfterSetupContextCleanup", "TestUnprocessedInternalOpsOnlyAndMetadataOnly", "TestReplayReroutesOnlyOnExplicitFollowerResponse"}),
            "pvmbg": ("default", ["./cmd/server"], {}, {"TestIncompatibleModesOnlyAffectNewReports", "TestBrokenJSONTruncatesResponse"}),
        }
        for service, (network, packages, environment, required) in suites.items():
            # Storage is an internal network. Download modules before entering
            # that network rather than weakening its isolation for the test.
            dockerfile = self.temporary / (service + "-tests.Dockerfile")
            source = ROOT / "services" / service
            recipe = "FROM " + self.go_image + "\nWORKDIR /src\nCOPY go.mod ./\n"
            if (source / "go.sum").exists():
                recipe += "COPY go.sum ./\n"
            recipe += "RUN go mod download\n"
            dockerfile.write_text(recipe, encoding="utf-8")
            image = self.project + "-tests-" + service
            self.test_images.append(image)
            self.command(["docker", "build", "--file", str(dockerfile), "--tag", image, str(source)],
                         log=service + "-test-build.log", timeout=600)
            env = self.temporary / (service + "-tests.env")
            env.write_text("\n".join(key + "=" + value for key, value in environment.items()) + "\n", encoding="utf-8")
            env.chmod(0o600)
            container = self.project + "-tests-" + service
            self.test_containers.append(container)
            args = ["docker", "run", "--rm", "--name", container, "--network", self.project + "_" + network,
                    "--env-file", str(env), "--mount", "type=bind,source=" + str(ROOT / "services" / service) + ",target=/src,readonly",
                    "--workdir", "/src", image, "go", "test", "-json", "-race", "-p", "1", "-count=1", *packages]
            result = self.command(args, log=service + "-tests.jsonl", check=False, timeout=600)
            passed, skipped = set(), []
            for line in result.stdout.splitlines():
                try:
                    event = json.loads(line)
                except json.JSONDecodeError:
                    continue
                if event.get("Action") == "pass" and event.get("Test"):
                    passed.add(event["Test"])
                if event.get("Action") == "skip":
                    skipped.append(event.get("Test", event.get("Package")))
            self.evidence["contract_tests"][service] = {"exit_code": result.returncode, "passed_tests": sorted(passed),
                                                         "skipped_tests": skipped, "required_tests_passed": sorted(required & passed)}
            self.save()
            require(result.returncode == 0 and required <= passed, service + " contract/integration checks failed or were skipped")

    def dlq(self, message_id):
        query = ("SELECT row_to_json(t) FROM (SELECT message_id, source, reason, status, attempts, "
                 "octet_length(raw) AS bytes, encode(sha256(raw),'hex') AS sha256, received_at "
                 "FROM unprocessed_messages WHERE message_id=" + literal(message_id) + ") t")
        value = self.sql(query)
        return json.loads(value) if value else None

    def public_item(self, message_id):
        cursor = None
        seen = set()
        while True:
            query = {"source": "PVMBG", "limit": "1"}
            if cursor:
                query["cursor"] = cursor
            response = self.http("client", "/internal/audit/unprocessed?" + urllib.parse.urlencode(query), role="internal-ops")
            require(response["status"] == 200, "Internal Ops inspection failed")
            for item in response["body"]["items"]:
                require(set(item) == PUBLIC_DLQ_FIELDS, "DLQ metadata leaked or changed")
                if item["message_id"] == message_id:
                    return item
            cursor = response["body"]["next_cursor"]
            if not cursor:
                return None
            require(cursor not in seen, "DLQ pagination repeated a cursor")
            seen.add(cursor)

    def canonical_count(self, reference):
        return int(self.sql("SELECT count(*) FROM hazard_events WHERE source='PVMBG' AND source_ref_id=" + literal(reference)))

    def source_since(self):
        raw = self.sql("SELECT cursor_value FROM ingest_cursor WHERE source='PVMBG'")
        cursor = json.loads(raw).get("ReportCursor") if raw else None
        if not cursor:
            return ""
        stamp = datetime.datetime.fromisoformat(cursor.replace("Z", "+00:00")) - datetime.timedelta(minutes=1)
        return "?since=" + urllib.parse.quote(stamp.isoformat())

    def malformed_and_replay(self):
        before = self.states()
        checks = self.evidence["checks"]
        checks["schema_and_malformed"] = []
        for mode, reason in MODES.items():
            self.progress("Checking live PVMBG mode: " + mode)
            changed = self.http("pvmbg", "/admin/schema-version", {"mode": mode})
            require(changed["status"] == 200, "schema trigger failed")
            reference = changed["body"]["report_id"]
            raw = self.http("pvmbg", "/volcanic-reports" + self.source_since(), raw=True)["body"]
            if mode == "broken_json":
                expected = raw
            else:
                candidates = [item for item in wire_items(raw) if reference.encode() in item]
                require(len(candidates) == 1, "could not isolate the emitted wire report")
                expected = candidates[0]
            digest = hashlib.sha256(expected).hexdigest()
            message_id = "pvmbg:" + digest
            saved = self.wait(mode + " retained byte-for-byte", lambda: self.dlq(message_id))
            require(saved["sha256"] == digest and saved["bytes"] == len(expected), "DLQ changed wire bytes")
            require(saved["reason"] == reason and saved["status"] == "parked", "unexpected failure classification")
            public = self.public_item(message_id)
            require(public is not None and public["retryable"] is False, "permanent failure not exposed as parked")
            require(self.canonical_count(reference) == 0, "incompatible payload became a hazard")
            case = {"mode": mode, "reference": reference, "wire_bytes": len(expected), "wire_sha256": digest,
                    "retained": saved, "public_metadata": public}
            checks["schema_and_malformed"].append(case)
            self.save()
            self.http("pvmbg", "/admin/schema-version", {"version": 1})
            if mode in ("rename_field", "change_type"):
                body = {"message_ids": [message_id]}
                require(self.http("client", "/internal/audit/unprocessed/replay", body, role="internal-ops")["status"] == 202,
                        "replay was not accepted")
                self.wait(mode + " replay recovered", lambda: self.dlq(message_id)["status"] == "recovered")
                require(self.canonical_count(reference) == 1 and self.public_item(message_id) is None, "replay did not recover exactly one hazard")
                require(self.http("client", "/internal/audit/unprocessed/replay", body, role="internal-ops")["status"] == 202,
                        "repeated replay was not accepted")
                require(self.canonical_count(reference) == 1, "replay duplicated a hazard")
                case["replay"] = "PASS: recovered once; repeated request remains idempotent"
            good = self.http("pvmbg", "/admin/schema-version", {"version": 2})["body"]["report_id"]
            self.wait("valid sibling progresses after " + mode, lambda: self.canonical_count(good) == 1)
            require(self.http("client", "/hazards?source=BMKG&limit=1", role="field-team")["status"] == 200, "other source stopped serving")
            self.http("pvmbg", "/admin/schema-version", {"version": 1})
        require(self.states() == before, "malformed modes restarted a service")
        checks["no_restart_during_malformed"] = True

    def permissions(self):
        cases = []
        for role in (None, "media", "field-team", "internal-ops"):
            wanted = 401 if role is None else (200 if role == "internal-ops" else 403)
            response = self.http("client", "/internal/audit/unprocessed?source=PVMBG", role=role)
            require(response["status"] == wanted, "DLQ inspection authorization failed")
            cases.append({"role": role or "anonymous", "inspection_status": response["status"]})
            if role != "internal-ops":
                response = self.http("client", "/internal/audit/unprocessed/replay", {"message_ids": ["pvmbg:" + "0" * 64]}, role=role)
                require(response["status"] == wanted, "DLQ replay authorization failed")
                cases[-1]["replay_status"] = response["status"]
        self.evidence["checks"]["internal_ops_only"] = cases

    def report_count(self, ids):
        return int(self.sql("SELECT count(*) FROM field_reports WHERE report_id IN (" + ",".join(map(literal, ids)) + ")"))

    def send_report(self, report_id, role="field-team"):
        return self.http("client", "/field-reports", {"report_id": report_id, "note": "P3 isolated acceptance report"}, role=role)

    def reports_survive(self):
        for role in ("media", "internal-ops"):
            require(self.send_report(str(uuid.uuid4()), role)["status"] == 403, "non-Field-Team submitted a report")
        ids = [str(uuid.uuid4()) for _ in range(self.reports)]
        checks = self.evidence["checks"]
        queue = self.http("broker", "/api/queues/%2F/field-reports")
        require(queue["status"] == 200 and queue["body"]["durable"] is True
                and queue["body"]["arguments"].get("x-queue-type") == "quorum", "field-reports is not a durable quorum queue")
        checks["field_report_queue"] = {"name": "field-reports", "durable": True, "type": "quorum"}
        checks["field_reports"] = {"ids": ids, "responses": []}
        self.compose("pause", "canonical-store")
        try:
            for report_id in ids:
                response = self.send_report(report_id)
                require(response["status"] == 202, "report was not durably accepted with storage paused")
                checks["field_reports"]["responses"].append({"report_id": report_id, "status": response["status"], "elapsed_ms": response["elapsed_ms"]})
            self.save()
            # All 202s preceded the receiver crash; storage could not commit first.
            self.compose("kill", "--signal", "SIGKILL", "aggregator")
        finally:
            self.compose("unpause", "canonical-store")
        self.compose("up", "-d", "--no-deps", "--wait", "aggregator")
        self.wait("all accepted reports survive receiver SIGKILL", lambda: self.report_count(ids) == len(ids))
        for report_id in ids:
            require(self.send_report(report_id)["status"] == 202, "duplicate submission not accepted")

        def duplicate_receipts():
            records = []
            for line in self.compose("logs", "--no-log-prefix", "aggregator").stdout.splitlines():
                try:
                    record = json.loads(line)
                except json.JSONDecodeError:
                    continue
                if record.get("msg", record.get("message")) == "field report duplicate skipped" and record.get("report_id") in ids:
                    records.append(record)
            return records if set(ids) <= {r["report_id"] for r in records} else None

        duplicates = self.wait("each duplicate submission was consumed and skipped", duplicate_receipts)
        require(self.report_count(ids) == len(ids), "accepted reports were duplicated")
        checks["field_reports"].update(committed_count=len(ids), receiver_crash_after_202=True,
                                        duplicate_receipts=duplicates)

        report_id = str(uuid.uuid4())
        self.compose("stop", "message-broker")
        try:
            response = self.send_report(report_id)
            require(response["status"] == 503, "broker failure returned a false 202")
            checks["broker_failure"] = {"report_id": report_id, "status": response["status"], "elapsed_ms": response["elapsed_ms"]}
        finally:
            self.compose("up", "-d", "--no-deps", "--wait", "message-broker")
        require(self.send_report(report_id)["status"] == 202, "report retry did not recover")
        self.wait("report retry commits once after broker recovery", lambda: self.report_count([report_id]) == 1)
        checks["broker_failure"]["committed_after_retry"] = 1

    def start(self):
        self.prepare()
        self.evidence["existing_states_before"] = self.existing_states()
        self.compose("up", "--build", "-d", "--wait", "--wait-timeout", "180", log="build.log", timeout=600)
        require(set(self.states()) == SERVICES, "fixture must contain exactly nine services")

    def stop(self):
        self.progress("Removing only the isolated P3 fixture")
        for container in self.test_containers:
            self.command(["docker", "rm", "--force", container], check=False)
        self.compose("unpause", "canonical-store", check=False)
        cleanup = self.compose("down", "--volumes", "--remove-orphans", log="cleanup.log", check=False)
        for image in self.test_images:
            self.command(["docker", "image", "rm", image], check=False)
        self.evidence.update(cleanup="PASS" if cleanup.returncode == 0 else "FAIL", finished_at=now())
        self.evidence["existing_states_after"] = self.existing_states()
        if self.evidence["existing_states_after"] != self.evidence.get("existing_states_before", {}):
            self.evidence.update(result="FAIL", cleanup="FAIL",
                                 cleanup_failure="existing project containers changed during the run")
        if cleanup.returncode:
            self.evidence["result"] = "FAIL"
        self.save()

    def run(self):
        self.start()
        try:
            self.contract_tests()
            self.wait("live canonical reads are ready", lambda: self.http("client", "/hazards?source=PVMBG&limit=1", role="field-team")["status"] == 200)
            self.permissions()
            self.malformed_and_replay()
            self.reports_survive()
            self.evidence["result"] = "PASS"
        except Exception as error:
            self.evidence.update(result="FAIL", failure=self.redact(str(error)))
            self.compose("logs", "--no-log-prefix", "--tail", "150", log="diagnostics.log", check=False)
        finally:
            self.stop()
        self.progress(self.evidence["result"] + ": " + str(self.folder / "p3-check.json"))
        return 0 if self.evidence["result"] == "PASS" else 1


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--reports", type=int, default=5, help="Number of 202 reports before receiver SIGKILL (1..50)")
    parser.add_argument("--output-dir", type=Path, help="New, empty evidence directory")
    parser.add_argument("--go-image", default="golang:1.27.1", help="Go integration-test image")
    args = parser.parse_args()
    if not 1 <= args.reports <= 50:
        parser.error("--reports must be between 1 and 50")
    if any(character.isspace() for character in args.go_image):
        parser.error("--go-image must be one image reference without whitespace")
    folder = (args.output_dir or ROOT / "artifacts/m2/runs" / (datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%M%SZ") + "-p3-" + secrets.token_hex(3))).resolve()
    folder.mkdir(parents=True, exist_ok=False)
    with tempfile.TemporaryDirectory(prefix="tubesaat-p3-") as temporary:
        checker = Check(folder, Path(temporary), args.reports, args.go_image)
        with (folder / "run.log").open("w", encoding="utf-8") as log:
            class Tee:
                def write(self, text):
                    sys.__stdout__.write(text)
                    log.write(text)
                def flush(self):
                    sys.__stdout__.flush()
                    log.flush()
            previous = sys.stdout
            sys.stdout = Tee()
            try:
                return checker.run()
            finally:
                sys.stdout = previous


if __name__ == "__main__":
    raise SystemExit(main())
