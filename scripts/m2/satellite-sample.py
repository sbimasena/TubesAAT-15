#!/usr/bin/env python3
"""Capture the actual session and raw SSE bytes without assuming payload fields."""
import argparse
import json
import os
from pathlib import Path
import sys
import time
import urllib.error
import urllib.request

BASE = "https://satellite.naufarrel.dev"


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


open_feed = urllib.request.build_opener(NoRedirect()).open


def capture(base, token, destination, seconds, offset):
    destination.mkdir(parents=True, exist_ok=True)
    headers = {"Authorization": f"Bearer {token}", "Accept": "application/json"}
    request = urllib.request.Request(base + "/v1/session", headers=headers)
    with open_feed(request, timeout=5) as response:
        session = response.read(1024 * 1024 + 1)
        if len(session) > 1024 * 1024:
            raise ValueError("session exceeds 1 MiB")
        json.loads(session)
    with (destination / "session.json").open("xb") as output:
        output.write(session)
    request = urllib.request.Request(base + f"/v1/stream?from={offset}",
                                     headers={**headers, "Accept": "text/event-stream"})
    counts, ids, size = {}, [], 0
    started = time.monotonic()
    with open_feed(request, timeout=5) as response:
        if response.headers.get_content_type() != "text/event-stream":
            raise ValueError("stream is not text/event-stream")
        with (destination / "stream.sse").open("xb") as output:
            while time.monotonic() - started < seconds:
                line = response.readline(1024 * 1024 + 1)
                if not line:
                    break
                size += len(line)
                if len(line) > 1024 * 1024 or size > 64 * 1024 * 1024:
                    raise ValueError("sample exceeds line/file size limit")
                output.write(line)  # Coordinates and acquisition time stay byte-for-byte intact.
                if line.startswith(b"event:"):
                    event = line[6:].strip().decode("utf-8")
                    counts[event] = counts.get(event, 0) + 1
                elif line.startswith(b"id:"):
                    # Only retain the endpoints: memory stays bounded during a burst.
                    value = line[3:].strip().decode("utf-8")
                    ids = [ids[0], value] if ids else [value]
    result = {"events": counts, "first_id": ids[0] if ids else None,
              "last_id": ids[-1] if ids else None, "bytes": size,
              "elapsed_seconds": round(time.monotonic() - started, 3),
              "requested_from": offset}
    with (destination / "summary.json").open("x") as output:
        json.dump(result, output, indent=2)
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--seconds", type=int, default=60, choices=range(1, 301), metavar="1..300")
    parser.add_argument("--from-offset", type=int, default=0)
    args = parser.parse_args()
    token = os.environ.get("SATELLITE_TOKEN", "")
    if not token:
        parser.error("set SATELLITE_TOKEN locally; never put it in a command argument")
    if args.from_offset < 0:
        parser.error("offset must be nonnegative")
    destination = Path("artifacts/m2/runs") / (time.strftime("%Y%m%dT%H%M%SZ", time.gmtime()) + "-satellite")
    try:
        result = capture(BASE, token, destination, args.seconds, args.from_offset)
    except urllib.error.HTTPError as error:
        error.close()
        print(f"Feed HTTP {error.code}; partial files: {destination}", file=sys.stderr)
        return 1
    except (OSError, ValueError) as error:
        # Do not print request objects, headers or credential-bearing environment values.
        print(f"Capture failed ({type(error).__name__}); partial files: {destination}", file=sys.stderr)
        return 1
    print(json.dumps({"directory": str(destination), **result}, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
