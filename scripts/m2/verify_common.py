#!/usr/bin/env python3
"""Dependency-free helpers shared by the M2 verification scripts.

The helpers here are deliberately small and pure where possible so the
unittest suite can exercise them without Docker, Kubernetes or a cluster.
HTTP failures are reduced to short codes: no response body, URL or token is
ever copied into evidence.
"""
from __future__ import annotations

import datetime
import json
import math
import os
import re
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path


UTC = datetime.timezone.utc
WINDOW_RE = re.compile(r"^[1-9][0-9]*[smhdwy]$")
MESSAGE_ID_RE = re.compile(r"^pvmbg:[0-9a-f]{64}$")
EXIT_CODES = {"PASS": 0, "FAIL": 1, "BLOCKED": 3, "INVENTORY_ONLY": 0, "PARTIAL": 1}


def utcnow():
    return datetime.datetime.now(UTC)


def isoformat(value=None):
    return (value or utcnow()).isoformat()


def parse_timestamp(value):
    """Parse an ISO-8601 timestamp, accepting the trailing Z used by the API."""
    if not value:
        return None
    text = str(value).strip()
    if text.endswith("Z"):
        text = text[:-1] + "+00:00"
    try:
        stamp = datetime.datetime.fromisoformat(text)
    except ValueError:
        return None
    if stamp.tzinfo is None:
        stamp = stamp.replace(tzinfo=UTC)
    return stamp.astimezone(UTC)


def require(condition, message):
    if not condition:
        raise AssertionError(message)


def parse_window(value):
    """Return (seconds, canonical text) for a Prometheus duration."""
    text = str(value).strip()
    require(WINDOW_RE.match(text), "window must look like 30s, 5m, 1h or 1d")
    amount = int(text[:-1])
    factor = {"s": 1, "m": 60, "h": 3600, "d": 86400, "w": 604800, "y": 31536000}[text[-1]]
    return amount * factor, text


def percentile(values, fraction):
    """Nearest-rank percentile, deterministic for the small samples used here."""
    data = sorted(float(value) for value in values)
    require(data, "percentile needs at least one sample")
    require(0 < fraction <= 1, "percentile fraction must be in (0, 1]")
    index = max(0, math.ceil(fraction * len(data)) - 1)
    return data[index]


def summarize_latencies(samples, target_ms):
    values = [float(sample) for sample in samples if sample is not None]
    require(values, "latency summary needs at least one sample")
    summary = {
        "count": len(values),
        "min_ms": round(min(values), 3),
        "p50_ms": round(percentile(values, 0.50), 3),
        "p95_ms": round(percentile(values, 0.95), 3),
        "max_ms": round(max(values), 3),
        "target_ms": float(target_ms),
    }
    summary["pass"] = summary["p95_ms"] < float(target_ms)
    return summary


def resolve_token(env_name=None, file_path=None, label="token"):
    """Read a bearer token from an environment variable or a token file."""
    require(bool(env_name) != bool(file_path),
            label + ": pass exactly one of --*-token-env or --*-token-file")
    if env_name:
        value = os.environ.get(env_name, "")
        source = "env:" + env_name
    else:
        path = Path(file_path)
        require(path.is_file(), label + ": token file does not exist")
        value = path.read_text(encoding="utf-8")
        source = "file"
    value = value.strip()
    require(value, label + ": resolved token is empty")
    return value, source


def summarize_error(error):
    """Return a short, secret-free description of a transport failure."""
    if isinstance(error, urllib.error.HTTPError):
        return "http_" + str(error.code)
    if isinstance(error, urllib.error.URLError):
        reason = error.reason
        if isinstance(reason, TimeoutError):
            return "timeout"
        return "network_" + type(reason).__name__
    if isinstance(error, TimeoutError):
        return "timeout"
    return type(error).__name__


def http_json(base_url, path, token=None, method="GET", body=None, timeout=30):
    """Perform one HTTP request and return a structured, secret-safe result."""
    url = str(base_url).rstrip("/") + path
    data = None if body is None else json.dumps(body).encode("utf-8")
    request = urllib.request.Request(url, data=data, method=method)
    request.add_header("Accept", "application/json")
    if data is not None:
        request.add_header("Content-Type", "application/json")
    if token:
        request.add_header("Authorization", "Bearer " + token)
    started = time.monotonic()
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            payload = response.read()
            parsed = json.loads(payload) if payload.strip() else None
            return {"status": response.status, "body": parsed,
                    "latency_ms": round((time.monotonic() - started) * 1000, 3), "error": None}
    except urllib.error.HTTPError as error:
        try:
            payload = error.read()
            parsed = json.loads(payload) if payload.strip() else None
        except Exception:
            parsed = None
        return {"status": error.code, "body": parsed,
                "latency_ms": round((time.monotonic() - started) * 1000, 3),
                "error": summarize_error(error)}
    except Exception as error:  # noqa: BLE001 - deliberately summarized, never re-raised
        return {"status": 0, "body": None,
                "latency_ms": round((time.monotonic() - started) * 1000, 3),
                "error": summarize_error(error)}


def login(auth_url, client_id, password, timeout=10):
    """Return a fresh access token from Auth POST /login."""
    response = http_json(auth_url, "/login", method="POST",
                         body={"client_id": client_id, "password": password}, timeout=timeout)
    body = response["body"] if isinstance(response["body"], dict) else {}
    require(response["status"] == 200 and body.get("access_token"),
            "login failed: " + str(response["error"] or response["status"]))
    return body["access_token"]


class Session:
    """Bearer token that logs in again after a 401: access tokens expire (60 s in
    the cluster), so a fixed token cannot cover a multi-minute measurement."""

    def __init__(self, token=None, auth_url=None, client_id=None, password=None):
        self.auth = (auth_url, client_id, password) if auth_url else None
        self.token = token or login(*self.auth)
        self.relogins = 0

    def get(self, base_url, path):
        response = http_json(base_url, path, token=self.token)
        if response["status"] != 401 or not self.auth:
            return response
        try:
            self.token = login(*self.auth)
        except AssertionError:
            return response
        self.relogins += 1
        return http_json(base_url, path, token=self.token)


def client_session(args, label="Field Team token"):
    """Build a Session from --auth-url plus credential env vars, or from a fixed token."""
    if getattr(args, "auth_url", None):
        client_id = os.environ.get(args.client_id_env, "")
        password = os.environ.get(args.client_password_env, "")
        require(client_id and password, label + ": " + args.client_id_env + " and "
                + args.client_password_env + " must be set with --auth-url")
        return Session(auth_url=args.auth_url, client_id=client_id, password=password), "login:" + args.client_id_env
    token_env = None if args.client_token_file else args.client_token_env
    token, source = resolve_token(token_env, args.client_token_file, label)
    return Session(token=token), source


def add_client_auth_args(parser):
    parser.add_argument("--client-token-env", default="FIELD_TEAM_TOKEN",
                        help="Environment variable holding a Field Team access token")
    parser.add_argument("--client-token-file", help="File holding a Field Team access token")
    parser.add_argument("--auth-url", help="Auth base URL; logs in and logs in again on 401")
    parser.add_argument("--client-id-env", default="FIELD_TEAM_CLIENT_ID")
    parser.add_argument("--client-password-env", default="FIELD_TEAM_CLIENT_PASSWORD")


def http_text(base_url, path, token=None, timeout=15):
    """Fetch a text endpoint such as /metrics; never raises raw errors."""
    url = str(base_url).rstrip("/") + path
    request = urllib.request.Request(url, method="GET")
    if token:
        request.add_header("Authorization", "Bearer " + token)
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            return {"status": response.status, "text": response.read().decode("utf-8", "replace"), "error": None}
    except Exception as error:  # noqa: BLE001
        return {"status": 0, "text": "", "error": summarize_error(error)}


def read_jsonl(path):
    records = []
    with Path(path).open("r", encoding="utf-8") as stream:
        for line in stream:
            line = line.strip()
            if not line:
                continue
            try:
                records.append(json.loads(line))
            except json.JSONDecodeError:
                continue
    return records


def decode_json(value):
    if isinstance(value, (dict, list)):
        return value
    if isinstance(value, (bytes, bytearray)):
        value = value.decode("utf-8", "replace")
    if isinstance(value, str):
        try:
            return json.loads(value)
        except json.JSONDecodeError:
            return None
    return None


def critical_alerts(records):
    """Return unique critical Sensor Pantai notifications from journal records."""
    found = {}
    for record in records:
        if not isinstance(record, dict):
            continue
        payload = decode_json(record.get("payload"))
        if not isinstance(payload, dict) or payload.get("source") != "SENSOR_PANTAI":
            continue
        attributes = payload.get("attributes")
        if not isinstance(attributes, dict) or attributes.get("critical") is not True:
            continue
        message_id = record.get("message_id")
        if not isinstance(message_id, str) or not message_id:
            continue
        found[message_id] = {
            "message_id": message_id,
            "hazard_id": record.get("hazard_id") or payload.get("hazard_id"),
            "source_ref_id": payload.get("source_ref_id"),
            "ingested_at": record.get("ingested_at") or payload.get("ingested_at"),
            "received_at": record.get("received_at"),
            "event_age_ms": record.get("event_age_ms"),
        }
    return [found[key] for key in sorted(found)]


def within(records, since=None, until=None):
    selected = []
    for record in records:
        stamp = parse_timestamp(record.get("ingested_at") or record.get("received_at"))
        if stamp is None:
            continue
        if since is not None and stamp < since:
            continue
        if until is not None and stamp > until:
            continue
        selected.append(record)
    return selected


def reconcile_ids(observed, expected):
    observed, expected = set(observed), set(expected)
    return {"missing": sorted(expected - observed), "unexpected": sorted(observed - expected),
            "observed": len(observed), "expected": len(expected)}


def parse_ids(text):
    """Parse a JSON array, a JSON object with ids/message_ids, or one ID per line."""
    text = (text or "").strip()
    if not text:
        return []
    if text.startswith("["):
        value = json.loads(text)
        require(isinstance(value, list), "expected a JSON array of IDs")
        return [str(item).strip() for item in value if str(item).strip()]
    if text.startswith("{"):
        value = json.loads(text)
        require(isinstance(value, dict), "expected a JSON object with ids")
        ids = value.get("ids") or value.get("message_ids") or []
        require(isinstance(ids, list), "expected ids to be a list")
        return [str(item).strip() for item in ids if str(item).strip()]
    return [line.strip() for line in text.splitlines()
            if line.strip() and not line.strip().startswith("#")]


def parse_id_file(path):
    return parse_ids(Path(path).read_text(encoding="utf-8"))


def parse_csv_ids(text):
    return [part.strip() for part in str(text or "").split(",") if part.strip()]


def chunked(items, size):
    require(size > 0, "chunk size must be positive")
    return [items[index:index + size] for index in range(0, len(items), size)]


def prom_result(payload):
    """Classify one Prometheus instant-query response without inventing zeros."""
    if not isinstance(payload, dict):
        return {"state": "ERROR", "error": "invalid_response"}
    if payload.get("status") != "success":
        return {"state": "ERROR", "error": str(payload.get("errorType") or "prometheus_error")}
    result = payload.get("data", {}).get("result", [])
    if not result:
        return {"state": "NO_DATA"}
    value = result[0].get("value", [None, None])[1]
    if value is None or str(value) in ("NaN", "+Inf", "-Inf"):
        return {"state": "NO_DATA", "raw": value}
    try:
        number = float(value)
    except (TypeError, ValueError):
        return {"state": "INVALID", "raw": value}
    if math.isnan(number) or math.isinf(number):
        return {"state": "NO_DATA", "raw": value}
    return {"state": "VALUE", "value": number}


def prom_query(base_url, query, timeout=15):
    path = "/api/v1/query?query=" + urllib.parse.quote(query, safe="")
    response = http_json(base_url, path, timeout=timeout)
    return {"http_status": response["status"], "error": response["error"],
            "result": prom_result(response["body"])}


def write_json(path, value):
    Path(path).write_text(json.dumps(value, indent=2, sort_keys=False) + "\n", encoding="utf-8")
