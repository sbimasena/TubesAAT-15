"""BNPB Control Console (context-m2.md §8.2 L2): a local page with live cluster state,
the shared action list, and a per-role API client.

Python 3 standard library only. Binds to 127.0.0.1 and uses the caller's kubeconfig.
Only actions listed in scripts/m2/actions.json can run, as `make <target>` without a
shell; parameter values are restricted to characters make cannot expand. Every run
writes a transcript to artifacts/m2/runs/. Credentials are read from the cluster's
Secret into memory and never returned to the page.

    make console            # http://127.0.0.1:8090
"""
import argparse
import base64
import json
import os
import re
import subprocess
import threading
import time
import urllib.error
import urllib.request
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
RUNS = ROOT / "artifacts" / "m2" / "runs"
PAGE = Path(__file__).with_name("console.html")
NS = "sistem-informasi-kebencanaan-bnpb"
# Local ports for the port-forwards the console keeps alive.
FORWARDS = {"aggregator": ("svc/aggregator", 18183, 8083), "client-api": ("svc/client-api", 18180, 8080),
            "auth": ("svc/auth", 18184, 8084)}
ROLES = {"media": "MEDIA_CLIENT_PASSWORD", "field-team": "FIELD_TEAM_CLIENT_PASSWORD",
         "internal-ops": "INTERNAL_OPS_CLIENT_PASSWORD"}
# make expands $(...) inside command-line variables, so values may not contain $ ( ) ` ; or quotes.
SAFE_VALUE = re.compile(r"^[A-Za-z0-9._/:@ ,=+-]{0,200}$")
SECRET_LINE = re.compile(r"(?i)(bearer\s+)[A-Za-z0-9._~+/=-]+|((?:token|password|secret|pass)[\"']?\s*[:=]\s*[\"']?)[^\s\"',]+")

jobs, jobs_lock = {}, threading.Lock()
secrets = {}
forward_procs = {}


def redact(text):
    text = SECRET_LINE.sub(lambda m: (m.group(1) or m.group(2) or "") + "[REDACTED]", text)
    for value in secrets.values():
        if value:
            text = text.replace(value, "[REDACTED]")
    return text


def kubectl_json(*args, timeout=10):
    out = subprocess.run(["kubectl", *args, "-o", "json"], capture_output=True, text=True, timeout=timeout)
    if out.returncode != 0:
        raise RuntimeError(out.stderr.strip()[:300])
    return json.loads(out.stdout)


def load_secrets():
    try:
        data = kubectl_json("-n", NS, "get", "secret", "auth-credentials")["data"]
        for key in ROLES.values():
            secrets[key] = base64.b64decode(data.get(key, "")).decode()
        audit = kubectl_json("-n", NS, "get", "secret", "audit-token")["data"]
        secrets["AUDIT_TOKEN"] = base64.b64decode(audit.get("token", "")).decode()
    except Exception as error:  # the page still works without the client panel
        print("credentials unavailable:", error)


def keep_forwards():
    procs = forward_procs
    while True:
        for name, (target, local, remote) in FORWARDS.items():
            proc = procs.get(name)
            if proc is None or proc.poll() is not None:
                procs[name] = subprocess.Popen(["kubectl", "-n", NS, "port-forward", target, f"{local}:{remote}"],
                                               stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        time.sleep(3)


def http_json(url, method="GET", body=None, headers=None, timeout=8):
    request = urllib.request.Request(url, method=method, data=json.dumps(body).encode() if body is not None else None,
                                     headers={"Content-Type": "application/json", **(headers or {})})
    started = time.perf_counter()
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            status, raw, hdrs = response.status, response.read(), response.headers
    except urllib.error.HTTPError as error:
        status, raw, hdrs = error.code, error.read(), error.headers
    elapsed = round((time.perf_counter() - started) * 1000, 1)
    try:
        payload = json.loads(raw or b"null")
    except ValueError:
        payload = raw.decode(errors="replace")[:2000]
    return status, payload, elapsed, hdrs


def aggregator(path):
    return http_json(f"http://127.0.0.1:{FORWARDS['aggregator'][1]}{path}")[1]


def cluster():
    nodes = kubectl_json("get", "nodes")["items"]
    pods = kubectl_json("get", "pods", "-A")["items"]
    keep = {NS, "tester-agent", "observability"}
    out = {"nodes": [], "pods": []}
    for node in nodes:
        ready = next((c["status"] for c in node["status"]["conditions"] if c["type"] == "Ready"), "Unknown")
        out["nodes"].append({"name": node["metadata"]["name"], "ready": ready == "True"})
    for pod in pods:
        meta, status = pod["metadata"], pod.get("status", {})
        if meta["namespace"] not in keep or status.get("phase") in ("Succeeded", "Failed"):
            continue
        cs = status.get("containerStatuses") or []
        out["pods"].append({"name": meta["name"], "ns": meta["namespace"], "node": pod["spec"].get("nodeName", ""),
                            "role": meta.get("labels", {}).get("role", meta.get("labels", {}).get("app", "")),
                            "ready": bool(cs) and all(c.get("ready") for c in cs),
                            "restarts": sum(c.get("restartCount", 0) for c in cs),
                            "terminating": "deletionTimestamp" in meta})
    lease = kubectl_json("-n", NS, "get", "lease", "aggregator-leader")["spec"]
    out["leader"] = {"holder": (lease.get("holderIdentity") or "").split("_")[0], "epoch": lease.get("leaseTransitions")}
    return out


def actions():
    return json.loads((ROOT / "scripts" / "m2" / "actions.json").read_text(encoding="utf-8"))["actions"]


def start_job(action_id, params, confirmed):
    action = next((a for a in actions() if a["id"] == action_id), None)
    if action is None:
        return 400, {"error": "unknown action"}
    if action.get("destructive") and not confirmed:
        return 409, {"error": "confirmation required"}
    argv = ["make", action["make"]]
    for name in action.get("params", []):
        value = str(params.get(name, "")).strip()
        if not SAFE_VALUE.match(value):
            return 400, {"error": f"invalid characters in {name}"}
        if value:
            argv.append(f"{name}={value}")
    with jobs_lock:
        if action.get("destructive") and any(j["destructive"] and j["exit"] is None for j in jobs.values()):
            return 409, {"error": "another destructive action is still running"}
        job_id = uuid.uuid4().hex[:10]
        RUNS.mkdir(parents=True, exist_ok=True)
        transcript = RUNS / f"{time.strftime('%Y%m%dT%H%M%SZ', time.gmtime())}-console-{action_id}.log"
        job = {"id": job_id, "action": action_id, "argv": " ".join(argv), "destructive": bool(action.get("destructive")),
               "output": "", "exit": None, "started": time.time(), "transcript": str(transcript.relative_to(ROOT))}
        jobs[job_id] = job
    env = {**os.environ, "YES": "1"}  # the page's modal is the confirmation

    def run():
        with open(transcript, "w", encoding="utf-8") as log:
            log.write(f"$ {job['argv']}\n# started {time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())}\n")
            proc = subprocess.Popen(argv, cwd=ROOT, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                    text=True, encoding="utf-8", errors="replace")
            job["proc"] = proc
            for line in proc.stdout:
                line = redact(line)
                job["output"] += line
                log.write(line)
            job["exit"] = proc.wait()
            log.write(f"# exit {job['exit']} after {time.time() - job['started']:.1f} s\n")
    threading.Thread(target=run, daemon=True).start()
    return 200, {"job": job_id}


def client_call(role, op, source):
    if role not in ROLES:
        return 400, {"error": "unknown role"}
    auth = FORWARDS["auth"][1]
    status, login, login_ms, _ = http_json(f"http://127.0.0.1:{auth}/login", "POST",
                                           {"client_id": role, "password": secrets.get(ROLES[role], "")})
    if status != 200:
        return 200, {"step": "login", "status": status, "ms": login_ms, "body": login}
    headers = {"Authorization": "Bearer " + login["access_token"], "X-Correlation-ID": "console-" + uuid.uuid4().hex[:12]}
    api = f"http://127.0.0.1:{FORWARDS['client-api'][1]}"
    source = source if source in ("", "BMKG", "PVMBG", "SATELIT", "SENSOR_PANTAI") else ""
    if op == "hazards":
        status, body, ms, hdrs = http_json(api + "/hazards?limit=5" + (f"&source={source}" if source else ""), headers=headers)
    elif op == "field-report":
        report = {"report_id": str(uuid.uuid4()), "note": "Laporan uji dari Control Console", "area_name": "Jawa Barat"}
        status, body, ms, hdrs = http_json(api + "/field-reports", "POST", report, headers)
    elif op == "unprocessed":
        status, body, ms, hdrs = http_json(api + "/internal/audit/unprocessed?limit=5", headers=headers)
    else:
        return 400, {"error": "unknown operation"}
    return 200, {"step": op, "status": status, "ms": ms, "correlation_id": hdrs.get("X-Correlation-ID"),
                 "body": json.loads(redact(json.dumps(body))) if not isinstance(body, str) else redact(body)}


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def send(self, status, payload, content_type="application/json"):
        data = payload if isinstance(payload, bytes) else json.dumps(payload, default=str).encode()
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        try:
            path = self.path.split("?")[0]
            if path == "/":
                return self.send(200, PAGE.read_bytes(), "text/html; charset=utf-8")
            if path == "/api/cluster":
                return self.send(200, cluster())
            if path == "/api/status":
                return self.send(200, aggregator("/internal/v1/status"))
            if path == "/api/events":
                return self.send(200, aggregator("/internal/v1/hazards?limit=12"))
            if path == "/api/actions":
                return self.send(200, actions())
            if path == "/api/history":
                files = sorted(RUNS.glob("*"), key=lambda p: p.stat().st_mtime, reverse=True)[:15] if RUNS.exists() else []
                return self.send(200, [{"name": f.name, "time": time.strftime("%m-%d %H:%M", time.localtime(f.stat().st_mtime))} for f in files])
            if path.startswith("/api/job/"):
                job = jobs.get(path.rsplit("/", 1)[1])
                if not job:
                    return self.send(404, {"error": "unknown job"})
                return self.send(200, {k: v for k, v in job.items() if k != "proc"} | {"elapsed": round(time.time() - job["started"], 1)})
            self.send(404, {"error": "not found"})
        except Exception as error:
            self.send(502, {"error": redact(str(error))[:300]})

    def do_POST(self):
        try:
            length = min(int(self.headers.get("Content-Length", 0)), 16 * 1024)
            body = json.loads(self.rfile.read(length) or b"{}")
            path = self.path.split("?")[0]
            if path == "/api/run":
                return self.send(*start_job(body.get("id", ""), body.get("params", {}), body.get("confirmed") is True))
            if path.startswith("/api/stop/"):
                job = jobs.get(path.rsplit("/", 1)[1])
                if job and job.get("proc") and job["exit"] is None:
                    job["proc"].terminate()
                return self.send(200, {"ok": True})
            if path == "/api/client":
                return self.send(*client_call(body.get("role", ""), body.get("op", ""), body.get("source", "")))
            self.send(404, {"error": "not found"})
        except Exception as error:
            self.send(502, {"error": redact(str(error))[:300]})


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--port", type=int, default=8090)
    args = parser.parse_args()
    load_secrets()
    threading.Thread(target=keep_forwards, daemon=True).start()
    server = ThreadingHTTPServer(("127.0.0.1", args.port), Handler)
    print(f"BNPB Control Console: http://127.0.0.1:{args.port}  (Ctrl+C untuk berhenti)")
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        for proc in forward_procs.values():  # Windows does not stop child port-forwards for us
            proc.terminate()


if __name__ == "__main__":
    main()
