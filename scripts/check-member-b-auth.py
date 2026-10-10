"""Verify downstream identities, field scopes, safe source metadata, and token rotation.

Requires Auth, Client API, and Aggregator with records from both sources.
--natural-expiry waits for the configured access TTL without changing the system clock.
Service lifecycle and source status are preserved. Upstream P3 and P2 load are separate checks.
"""
import argparse
import base64
import datetime
import json
from pathlib import Path
import time
import urllib.error
import urllib.request
import uuid
from checker_progress import progress
from demo_support import options, settings, wait_for


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--auth-url", help="Published Auth origin; default localhost AUTH_PORT")
    parser.add_argument("--client-url", help="Published Client API origin; default localhost CLIENT_API_PORT")
    parser.add_argument("--natural-expiry", action="store_true", help="Wait for the actual access TTL (maximum 300s)")
    parser.add_argument("--output", type=Path, default=Path(options.evidence_dir) / "auth-check.json",
                        help="Evidence without tokens, credentials, or hazard payloads")
    args = parser.parse_args()
    progress("Starting configuration and prerequisite checks")
    env = settings
    auth = (args.auth_url or f"http://127.0.0.1:{env.get('AUTH_PORT', '8084')}").rstrip("/")
    client = (args.client_url or f"http://127.0.0.1:{env.get('CLIENT_API_PORT', '8080')}").rstrip("/")
    summary = {"hazard_id", "source", "hazard_type", "severity", "area_name", "occurred_at", "ingested_at"}
    canonical = summary | {"source_ref_id", "latitude", "longitude", "attributes"}
    source_fields = {"available", "last_ingested_at", "stale", "stale_since", "stale_after_seconds"}
    prefix = "member-b-auth-" + uuid.uuid4().hex
    checks = []

    def call(origin, path, payload=None, access=None):
        correlation = prefix + "-" + uuid.uuid4().hex[:8]
        headers = {"X-Correlation-ID": correlation}
        if access:
            headers["Authorization"] = "Bearer " + access
        body = None
        if payload is not None:
            body = json.dumps(payload).encode()
            headers["Content-Type"] = "application/json"
        request = urllib.request.Request(origin + path, data=body, headers=headers)
        try:
            response = urllib.request.urlopen(request, timeout=15)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            assert response.headers.get("X-Correlation-ID") == correlation, "Correlation ID changed"
            assert response.headers.get("Cache-Control") == "no-store", "Sensitive response may be cached"
            data = response.read()
            if origin == client and response.status >= 400:
                assert response.headers.get("Content-Type") == "application/json", "Client error is not JSON"
                failure = json.loads(data)
                assert set(failure) == {"error", "correlation_id"} and failure["correlation_id"] == correlation, "Invalid Client error envelope"
                assert set(failure["error"]) == {"code", "message"}, "Invalid Client error detail"
            return response.status, json.loads(data) if response.status == 200 else None

    def login(role):
        prefix = role.upper().replace("-", "_")
        client_id = env.get(prefix + "_CLIENT_ID", role)
        password = env.get(prefix + "_CLIENT_PASSWORD")
        assert password, f"Set {prefix}_CLIENT_PASSWORD in the local environment file"
        status, pair = call(auth, "/login", {"client_id": client_id, "password": password})
        assert status == 200, f"Login failed for {role}: HTTP {status}"
        assert pair["token_type"] == "Bearer" and pair["refresh_token"], "Incomplete token pair"
        assert pair["expires_in"] == int(env.get("ACCESS_TOKEN_TTL_SECONDS") or "60"), "Unexpected access TTL"
        return pair

    def ready():
        status, payload = call(client, "/hazards?limit=1000", access=login("field-team")["access_token"])
        return status == 200 and {r["source"] for r in payload["data"]} == {"BMKG", "PVMBG"}
    wait_for(ready, "both sources must be readable before authentication checks")

    assert call(client, "/hazards")[0] == 401, "Missing token accepted"
    assert call(client, "/hazards", access="invalid-token")[0] == 401, "Invalid token accepted"
    checks.append("missing/invalid token -> 401")
    progress("Checking three identities: scopes, raw-field rejection, refresh and replay")
    for role in ("media", "field-team", "internal-ops"):
        pair = login(role)
        for source in ("BMKG", "PVMBG"):
            status, payload = call(client, f"/hazards?source={source}&limit=5", access=pair["access_token"])
            assert status == 200 and payload["data"], f"No real {source} data for {role}"
            assert set(payload) == {"data", "count", "sources"} and payload["count"] == len(payload["data"]), "Unexpected envelope"
            assert set(payload["sources"]) == {"BMKG", "PVMBG"}, "Missing public source metadata"
            assert all(set(status) == source_fields for status in payload["sources"].values()), "Source diagnostic leak"
            expected = summary if role == "media" else canonical
            assert all(set(row) == expected and row["source"] == source for row in payload["data"]), "Field/scope leak"
        if role == "media":
            for query in ("include_raw=true", "fields=source_ref_id", "fields=latitude", "fields=longitude", "fields=attributes"):
                assert call(client, "/hazards?" + query, access=pair["access_token"])[0] == 403, "Explicit raw request accepted"
            assert call(client, "/hazards?fields=ingested_at", access=pair["access_token"])[0] == 200, "Summary ingestion timestamp denied"
        status, new = call(auth, "/refresh", {"refresh_token": pair["refresh_token"]})
        assert status == 200 and new["access_token"] != pair["access_token"] and new["refresh_token"] != pair["refresh_token"], "Rotation failed"
        assert call(client, "/hazards", access=pair["access_token"])[0] == 401, "Old unexpired access token accepted"
        assert call(auth, "/refresh", {"refresh_token": pair["refresh_token"]})[0] == 401, "Old refresh replay accepted"
        assert call(client, "/hazards", access=new["access_token"])[0] == 200, "New access rejected"
        checks.append(f"{role}: fields, safe source metadata, refresh, old access/replay rejection")

    elapsed = None
    progress("Checking natural token expiry when requested")
    if args.natural_expiry:
        pair = login("field-team")
        # Decode only to schedule the wait; Auth remains responsible for JWT validation.
        encoded = pair["access_token"].split(".")[1]
        claims = json.loads(base64.urlsafe_b64decode(encoded + "=" * (-len(encoded) % 4)))
        remaining = claims["exp"] - time.time() + 0.25
        assert 0 < remaining <= 300, "Choose an access TTL of at most 300s for this checkpoint"
        assert call(client, "/hazards", access=pair["access_token"])[0] == 200, "Token invalid before natural expiry"
        started = time.monotonic()
        deadline = time.monotonic() + remaining
        next_update = time.monotonic()
        while time.monotonic() < deadline:
            if time.monotonic() >= next_update:
                progress(f"Waiting for natural expiry: {max(0, deadline - time.monotonic()):.0f}s remaining")
                next_update += 10
            time.sleep(min(1, max(0, deadline - time.monotonic())))
        assert call(client, "/hazards", access=pair["access_token"])[0] == 401, "Naturally expired token accepted"
        status, new = call(auth, "/refresh", {"refresh_token": pair["refresh_token"]})
        assert status == 200, "Refresh after natural expiry failed"
        assert call(client, "/hazards", access=new["access_token"])[0] == 200, "Refreshed token rejected"
        assert call(client, "/hazards", access=pair["access_token"])[0] == 401, "Old token accepted after refresh"
        elapsed = round(time.monotonic() - started, 3)
        checks.append("natural Field Team expiry -> refresh without login -> old token rejected")

    evidence = {"stage": "Member B Stage 4", "status": "PASS",
                "checked_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
                "checks": checks, "natural_expiry_checked": args.natural_expiry,
                "expiry_wait_seconds": elapsed, "correlation_prefix": prefix}
    if args.output:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(json.dumps(evidence, indent=2) + "\n", encoding="utf-8")
        progress("Result file saved")
    progress("PASS: authentication checks")
    print(json.dumps(evidence, indent=2))


if __name__ == "__main__":
    try:
        main()
    except (AssertionError, OSError, ValueError, KeyError) as error:
        # URLs and configuration may contain secrets; never dump requests or a traceback.
        detail = str(error) if isinstance(error, AssertionError) else type(error).__name__
        print(f"FAIL: {detail}; check local configuration and service logs.")
        raise SystemExit(1)
