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
    # Cloudflare rejects the default Python-urllib signature (error 1010), so
    # identify the sampler explicitly instead of spoofing a browser.
    headers = {
        "Authorization": f"Bearer {token}",
        "Accept": "application/json",
        "User-Agent": "bnpb-satellite-sample/1.0",
    }
    request = urllib.request.Request(base + "/v1/session", headers=headers)
    with open_feed(request, timeout=5) as response:
        session = response.read(1024 * 1024 + 1)
        if len(session) > 1024 * 1024:
            raise ValueError("session exceeds 1 MiB")
        session_state = json.loads(session)
    if not isinstance(session_state, dict):
        raise ValueError("session response is not an object")
    requested_offset = offset
    if offset == "latest":
        offset = session_state.get("latest_offset")
        if not isinstance(offset, int) or isinstance(offset, bool) or offset < 0:
            raise ValueError("session has no valid latest_offset")
    with (destination / "session.json").open("xb") as output:
        output.write(session)
    stream_path = destination / "stream.sse"
    counts, size, last_offset = {}, 0, None
    first_id = last_id = None
    connections = reconnects = duplicate_offsets = offset_gaps = 0
    invalid_offsets = stream_errors = retryable_http_errors = 0
    started = time.monotonic()
    backoff = 1.0
    with stream_path.open("xb") as output:
        while time.monotonic() - started < seconds:
            resume_from = last_id if last_id is not None else offset
            request = urllib.request.Request(
                base + f"/v1/stream?from={resume_from}",
                headers={**headers, "Accept": "text/event-stream"},
            )
            block_lines, event_name, event_id, has_data = [], "message", "", False
            retry_stream = False

            def finish_block():
                nonlocal size, last_offset, first_id, last_id
                nonlocal duplicate_offsets, offset_gaps, invalid_offsets
                nonlocal block_lines, event_name, event_id, has_data
                if not block_lines:
                    return
                block_bytes = sum(map(len, block_lines))
                if size + block_bytes > 64 * 1024 * 1024:
                    raise ValueError("sample exceeds line/file size limit")
                output.writelines(block_lines)
                size += block_bytes
                if has_data:
                    duplicate = False
                    if event_id:
                        try:
                            parsed_offset = int(event_id)
                            if parsed_offset < 0:
                                raise ValueError
                        except ValueError:
                            parsed_offset = None
                            invalid_offsets += 1
                        if parsed_offset is not None:
                            if last_offset is not None and parsed_offset <= last_offset:
                                duplicate = True
                                duplicate_offsets += 1
                            else:
                                if last_offset is not None and parsed_offset > last_offset + 1:
                                    offset_gaps += parsed_offset - last_offset - 1
                                last_offset = parsed_offset
                                if first_id is None:
                                    first_id = event_id
                                last_id = event_id
                    if not duplicate:
                        counts[event_name] = counts.get(event_name, 0) + 1
                block_lines = []
                event_name, event_id, has_data = "message", "", False

            try:
                with open_feed(request, timeout=5) as response:
                    if response.headers.get_content_type() != "text/event-stream":
                        raise ValueError("stream is not text/event-stream")
                    connections += 1
                    while time.monotonic() - started < seconds:
                        try:
                            line = response.readline(1024 * 1024 + 1)
                        except (OSError, TimeoutError):
                            stream_errors += 1
                            retry_stream = True
                            break
                        if not line:
                            finish_block()
                            break
                        if len(line) > 1024 * 1024:
                            raise ValueError("sample exceeds line/file size limit")
                        block_lines.append(line)
                        if line in (b"\n", b"\r\n"):
                            finish_block()
                            continue
                        if line.startswith(b"event:"):
                            event_name = line[6:].strip().decode("utf-8")
                        elif line.startswith(b"id:"):
                            event_id = line[3:].strip().decode("utf-8")
                        elif line.startswith(b"data:"):
                            has_data = True
            except urllib.error.HTTPError as error:
                error.close()
                if error.code != 429:
                    raise
                retryable_http_errors += 1
                retry_stream = True

            if time.monotonic() - started >= seconds:
                break
            reconnects += 1
            delay = min(backoff if retry_stream else 1.0, seconds - (time.monotonic() - started))
            if delay > 0:
                time.sleep(delay)
            if retry_stream:
                backoff = min(backoff * 2, 10.0)
            else:
                backoff = 1.0

    result = {
        "events": counts,
        "first_id": first_id,
        "last_id": last_id,
        "bytes": size,
        "elapsed_seconds": round(time.monotonic() - started, 3),
        "requested_from": requested_offset,
        "resolved_from": offset,
        "connections": connections,
        "reconnects": reconnects,
        "duplicate_offsets": duplicate_offsets,
        "offset_gaps": offset_gaps,
        "invalid_offsets": invalid_offsets,
        "stream_errors": stream_errors,
        "retryable_http_errors": retryable_http_errors,
    }
    with (destination / "summary.json").open("x") as output:
        json.dump(result, output, indent=2)
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--seconds", type=int, default=60, choices=range(1, 301), metavar="1..300")
    parser.add_argument("--from-offset", default="0",
                        help="nonnegative stream offset, or 'latest' to follow the live edge")
    args = parser.parse_args()
    token = os.environ.get("SATELLITE_TOKEN", "")
    if not token:
        parser.error("set SATELLITE_TOKEN locally; never put it in a command argument")
    if args.from_offset != "latest":
        try:
            args.from_offset = int(args.from_offset)
        except ValueError:
            parser.error("--from-offset must be a nonnegative integer or 'latest'")
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
