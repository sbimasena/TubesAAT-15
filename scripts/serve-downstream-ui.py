"""Serve the local downstream API console and relay fixed Auth/Client API routes.

Requires Python 3 standard library. Targets are loopback HTTP services only.
Local credentials are optional; passwords and sessions are never written by this tool.
The console can run downstream Go tests and a bounded burst, but does not alter
service lifecycle, source state, Compose configuration, or database contents.
"""
import argparse
from concurrent.futures import ThreadPoolExecutor
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
import subprocess
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parents[1]
ROLES = ("media", "field-team", "internal-ops")
ROUTES = {
    "auth": {("GET", "/health"), ("POST", "/login"), ("POST", "/refresh")},
    "client": {("GET", "/health"), ("GET", "/hazards"), ("GET", "/internal/provisional/hazards")},
}
BODY_LIMIT = 16 * 1024
RESPONSE_LIMIT = 10 * 1024 * 1024


def read_env(path):
    """Read the same simple, non-expanding environment format as the B checkers."""
    result = {}
    if path is not None:
        for line in path.read_text(encoding="utf-8-sig").splitlines():
            line = line.strip()
            if line and not line.startswith("#") and "=" in line:
                key, value = line.split("=", 1)
                result[key.strip()] = value.strip().strip("\"'")
    return result


def local_origin(value):
    parsed = urllib.parse.urlsplit(value)
    if (parsed.scheme != "http" or parsed.hostname not in ("127.0.0.1", "localhost", "::1")
            or parsed.username is not None or parsed.password is not None
            or parsed.path not in ("", "/") or parsed.query or parsed.fragment):
        raise ValueError("Target must be a loopback HTTP origin without credentials or path")
    port = parsed.port
    if port is not None and not 1 <= port <= 65535:
        raise ValueError("Invalid target port")
    return value.rstrip("/")


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def relay(origin, path, method="GET", payload=None, access=None, correlation=None):
    """Preserve service status and safe headers; never follow dependency redirects."""
    correlation = correlation or "b-ui-" + uuid.uuid4().hex
    if not isinstance(correlation, str) or len(correlation) > 128 or "\n" in correlation or "\r" in correlation:
        raise ValueError("Invalid correlation ID")
    headers = {"X-Correlation-ID": correlation}
    if access:
        if not isinstance(access, str) or "\n" in access or "\r" in access:
            raise ValueError("Invalid access token format")
        headers["Authorization"] = "Bearer " + access
    body = None
    if payload is not None:
        body = json.dumps(payload).encode("utf-8")
        headers["Content-Type"] = "application/json"
    request = urllib.request.Request(origin + path, body, headers, method=method)
    started = time.perf_counter()
    try:
        try:
            response = urllib.request.build_opener(NoRedirect()).open(request, timeout=20)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            raw = response.read(RESPONSE_LIMIT + 1)
            if len(raw) > RESPONSE_LIMIT:
                raise ValueError("Response exceeds the console limit")
            text = raw.decode("utf-8", errors="replace")
            try:
                data = json.loads(text)
            except ValueError:
                data = text
            return {"status": response.status, "body": data,
                    "headers": {key: response.headers.get(key) for key in
                                ("X-Correlation-ID", "Cache-Control", "Retry-After", "Content-Type", "X-Provisional-Endpoint")},
                    "sent_correlation_id": correlation,
                    "latency_ms": round((time.perf_counter() - started) * 1000, 2)}
    except (OSError, urllib.error.URLError, ValueError):
        # Relay failures have no service HTTP status and must not masquerade as a service 502.
        return {"status": None, "body": None, "headers": {}, "sent_correlation_id": correlation,
                "latency_ms": round((time.perf_counter() - started) * 1000, 2),
                "transport_error": "Target unreachable, timed out, or exceeded the console response limit"}


def burst(origin, access, count):
    """Use real concurrent local requests; browser connection limits do not shape the burst."""
    if type(count) is not int or not 2 <= count <= 128:
        raise ValueError("Choose 2 to 128 concurrent requests")
    if not isinstance(access, str) or not access:
        raise ValueError("Login before running a burst")
    prefix = "b-ui-burst-" + uuid.uuid4().hex
    started = time.perf_counter()
    with ThreadPoolExecutor(max_workers=count) as pool:
        futures = [pool.submit(relay, origin, "/hazards?source=BMKG&limit=1", access=access,
                               correlation=f"{prefix}-{i}") for i in range(count)]
        results = [f.result() for f in futures]
    counts = {}
    for result in results:
        key = str(result["status"]) if result["status"] is not None else "transport_error"
        counts[key] = counts.get(key, 0) + 1
    values = sorted(r["latency_ms"] for r in results)
    return {"connections": count, "status_counts": counts,
            "total_ms": round((time.perf_counter() - started) * 1000, 2),
            "p50_ms": values[(count - 1) // 2], "p95_ms": values[min(count - 1, int(count * .95))],
            "correlation_prefix": prefix, "controlled_429_observed": "429" in counts,
            "unexpected_status_observed": any(key not in ("200", "429") for key in counts),
            "sustained_p2_load_checked": False}


def run_unit_tests():
    """Run only the two B modules with the race detector and uncached results."""
    command = ["go", "test", "-race", "-count=1", "-timeout", "90s",
               "./services/auth/...", "./services/client-api/..."]
    try:
        result = subprocess.run(command, cwd=ROOT, capture_output=True, timeout=180)
    except FileNotFoundError:
        return {"passed": False, "output": "Go is not available on PATH", "command": " ".join(command)}
    except subprocess.TimeoutExpired:
        return {"passed": False, "output": "Go test exceeded the console deadline", "command": " ".join(command)}
    # Package summaries suffice for UI evidence; raw test diagnostics may contain fixture tokens.
    lines = result.stdout.decode("utf-8", errors="replace").splitlines()
    summaries = [line for line in lines if line.startswith(("ok ", "FAIL", "? "))]
    return {"passed": result.returncode == 0, "exit_code": result.returncode,
            "output": "\n".join(summaries) or "No package summary; inspect go test locally for diagnostics",
            "command": " ".join(command)}


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_):
        # Never log credential-bearing request bodies or service responses.
        pass

    def reply(self, status, value, content_type="application/json"):
        data = value if isinstance(value, bytes) else json.dumps(value).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Cache-Control", "no-store")
        self.send_header("X-Content-Type-Options", "nosniff")
        self.send_header("Referrer-Policy", "no-referrer")
        self.send_header("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        try:
            self.wfile.write(data)
        except (BrokenPipeError, ConnectionResetError):
            pass

    def trusted(self):
        port = self.server.server_port
        host = self.headers.get("Host")
        if host not in (f"127.0.0.1:{port}", f"localhost:{port}"):
            self.reply(403, {"error": "Use the local console URL"})
            return False
        origin = self.headers.get("Origin")
        if origin is not None and origin != f"http://{host}":
            self.reply(403, {"error": "Cross-origin console requests are rejected"})
            return False
        return True

    def do_GET(self):
        if not self.trusted():
            return
        if self.path == "/":
            self.reply(200, (ROOT / "tests/downstream-console.html").read_bytes(), "text/html; charset=utf-8")
        elif self.path == "/tool/info":
            env = self.server.settings["env"]
            self.reply(200, {"auth_url": self.server.settings["auth"], "client_url": self.server.settings["client"],
                             "roles": {role: {"client_id": env.get(role.upper().replace("-", "_") + "_CLIENT_ID", role),
                                              "local_password_available": bool(env.get(role.upper().replace("-", "_") + "_CLIENT_PASSWORD"))}
                                       for role in ROLES},
                             "expected_ttl": int(env.get("ACCESS_TOKEN_TTL_SECONDS") or "60"),
                             "legacy_enabled": env.get("ENABLE_PROVISIONAL_HAZARD_ENDPOINT", "false") == "true"})
        else:
            self.reply(404, {"error": "Unknown console route"})

    def do_POST(self):
        if not self.trusted():
            return
        if self.headers.get("Content-Type") != "application/json" or self.headers.get("X-Tool-Request") != "1":
            self.reply(403, {"error": "Use console JSON requests"})
            return
        try:
            size = int(self.headers.get("Content-Length", "0"))
            if not 0 < size <= BODY_LIMIT:
                raise ValueError("Invalid console request size")
            data = json.loads(self.rfile.read(size))
            if not isinstance(data, dict):
                raise ValueError("Console request must be an object")
            settings = self.server.settings
            if self.path == "/tool/call":
                target, path, method = data.get("target"), data.get("path", ""), data.get("method", "GET")
                parsed = urllib.parse.urlsplit(path)
                if (not isinstance(target, str) or target not in ROUTES or parsed.scheme or parsed.netloc or parsed.fragment
                        or (method, parsed.path) not in ROUTES[target]):
                    raise ValueError("Only public Auth and Client API routes are available")
                self.reply(200, relay(settings[target], path, method, data.get("payload"),
                                      data.get("access"), data.get("correlation")))
            elif self.path == "/tool/login":
                role = data.get("role")
                if role not in ROLES:
                    raise ValueError("Unknown role")
                prefix = role.upper().replace("-", "_")
                client_id = data.get("client_id") or settings["env"].get(prefix + "_CLIENT_ID", role)
                password = data.get("password") or settings["env"].get(prefix + "_CLIENT_PASSWORD")
                if not password:
                    raise ValueError("Enter a password or supply a local environment file")
                self.reply(200, relay(settings["auth"], "/login", "POST",
                                      {"client_id": client_id, "password": password}, correlation=data.get("correlation")))
            elif self.path in ("/tool/burst", "/tool/unit-tests"):
                if not self.server.operation_lock.acquire(blocking=False):
                    self.reply(409, {"error": "A burst or unit test is already running"})
                    return
                try:
                    if self.path == "/tool/burst":
                        result = burst(settings["client"], data.get("access"), data.get("count"))
                    else:
                        result = run_unit_tests()
                    self.reply(200, result)
                finally:
                    self.server.operation_lock.release()
            else:
                self.reply(404, {"error": "Unknown console route"})
        except (ValueError, TypeError, KeyError):
            self.reply(400, {"error": "Invalid console input; check the route, role, count, or local password"})
        except Exception:
            self.reply(500, {"error": "Console operation failed; inspect configuration locally"})


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--env-file", type=Path, help="Optional uncommitted credentials and published ports")
    parser.add_argument("--auth-url", help="Loopback Auth origin; defaults to AUTH_PORT or 8084")
    parser.add_argument("--client-url", help="Loopback Client API origin; defaults to CLIENT_API_PORT or 8080")
    parser.add_argument("--port", type=int, default=18765, help="Console port on 127.0.0.1")
    args = parser.parse_args()
    try:
        if not 1 <= args.port <= 65535:
            raise ValueError("Invalid console port")
        env = read_env(args.env_file)
        ttl = int(env.get("ACCESS_TOKEN_TTL_SECONDS") or "60")
        if ttl < 1:
            raise ValueError("Invalid access TTL")
        auth = local_origin(args.auth_url or f"http://127.0.0.1:{env.get('AUTH_PORT') or '8084'}")
        client = local_origin(args.client_url or f"http://127.0.0.1:{env.get('CLIENT_API_PORT') or '8080'}")
        server = ThreadingHTTPServer(("127.0.0.1", args.port), Handler)
    except (OSError, ValueError):
        parser.exit(1, "Cannot start console; check local file, ports, TTL, and loopback target URLs.\n")
    server.daemon_threads = True
    server.settings = {"env": env, "auth": auth, "client": client}
    server.operation_lock = threading.Lock()
    print(f"downstream API console: http://127.0.0.1:{args.port}", flush=True)
    print(f"Auth: {auth}; Client API: {client}. Stop with Ctrl+C.", flush=True)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()


if __name__ == "__main__":
    main()
