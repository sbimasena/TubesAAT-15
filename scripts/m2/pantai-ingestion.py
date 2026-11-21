#!/usr/bin/env python3
"""Check coastal ingestion/restart/outage on an isolated official-image fixture.

Uses the P3 operator fixture for temporary credentials, Compose commands and
cleanup checks. This is single-node evidence, not P5 critical-delivery proof.
"""
import argparse
import contextlib
import datetime
import importlib.util
import json
from pathlib import Path
import secrets
import sys
import tempfile
import time


ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location("p3_fixture", Path(__file__).with_name("p3-check.py"))
fixture = importlib.util.module_from_spec(spec)
spec.loader.exec_module(fixture)
IMAGE = "sustuna/sensor-pantai@sha256:51fbd90d3891ad6e9f5768ad330ffb65205644cb29e50da65c7538f2e0b4397d"


class Check(fixture.Check):
    def __init__(self, folder, temporary):
        super().__init__(folder, temporary, 5, "golang:1.27.1")
        self.project = "tubesaat-coastal-" + secrets.token_hex(5)
        self.evidence.update(project=self.project, purpose="COASTAL_INGESTION", p5_acceptance=False,
                             sensor_image=IMAGE, sensor_profile="practice", sensor_speed=60)

    def progress(self, message):
        print(self.redact(f"[coastal +{time.monotonic()-self.started:.0f}s] {message}"), flush=True)

    def save(self):
        (self.folder / "pantai-ingestion.json").write_text(self.redact(json.dumps(self.evidence, indent=2)) + "\n", encoding="utf-8")

    def prepare(self):
        super().prepare()
        token = secrets.token_urlsafe(32)
        self.values["PANTAI_AUTH_TOKEN"] = token
        self.sensitive.append(token)
        (self.temporary / "test.env").write_text("\n".join(k+"="+v for k,v in self.values.items())+"\n", encoding="utf-8")
        override = self.temporary / "override.json"
        config = json.loads(override.read_text(encoding="utf-8"))
        config["services"].update({
            "sensor-pantai": {"image": IMAGE, "environment": {"PANTAI_PROFILE": "practice", "PANTAI_SPEED": "60", "PANTAI_SEED": "42", "PANTAI_AUTH_TOKEN": "${PANTAI_AUTH_TOKEN}"}},
            "aggregator": {"environment": {"PANTAI_BASE_URL": "http://sensor-pantai:8080", "PANTAI_AUTH_TOKEN": "${PANTAI_AUTH_TOKEN}"}},
        })
        override.write_text(json.dumps(config), encoding="utf-8")
        self.evidence["coastal_configuration"] = {"PANTAI_BASE_URL":"http://sensor-pantai:8080", **{
            name:self.values[name] for name in ("PANTAI_REQUEST_TIMEOUT_MS","PANTAI_POLL_INTERVAL_MS","PANTAI_PROGRESS_TIMEOUT_MS","PANTAI_PAGE_LIMIT")}}
        self.save()

    def coastal(self, role="field-team"):
        return self.http("client", "/hazards?source=SENSOR_PANTAI&hazard_type=COASTAL&limit=100", role=role)

    def live_checks(self):
        def initial():
            response = self.coastal()
            if response["status"] != 200:
                return None
            status = response["body"]["sources"].get("SENSOR_PANTAI", {})
            return response if response["body"]["data"] and status.get("available") and not status.get("stale") else None
        first = self.wait("official coastal observations reach the protected API", initial, seconds=90)
        rows = first["body"]["data"]
        for row in rows:
            fixture.require(row["source"] == "SENSOR_PANTAI" and row["hazard_type"] == "COASTAL", "wrong coastal identity")
            fixture.require(row["severity"] == ("AWAS" if row["attributes"]["critical"] else "NORMAL"), "provisional severity mapping changed")
        ids = [row["hazard_id"] for row in rows[:5]]
        def counts():
            literals = ",".join(map(fixture.literal, ids))
            return self.sql("SELECT (SELECT count(*) FROM hazard_events WHERE hazard_id IN ("+literals+"))||','||(SELECT count(*) FROM hazard_outbox WHERE hazard_id IN ("+literals+"))")
        before = counts()
        media = self.coastal("media")
        expected = {"hazard_id","source","hazard_type","severity","area_name","occurred_at","ingested_at"}
        fixture.require(media["status"] == 200 and media["body"]["data"] and all(set(row) == expected for row in media["body"]["data"]), "Media coastal projection leaked raw fields")
        self.evidence["checks"]["coastal_api"] = {"initial_count": len(rows), "sample_ids": ids, "media_summary_fields": sorted(expected)}
        self.compose("restart", "aggregator")
        self.wait("coastal ingestion restores after Aggregator restart", initial, seconds=90)
        fixture.require(counts() == before, "restart duplicated sampled canonical rows/outbox effects")
        self.evidence["checks"]["restart"] = {"rows_outbox_before":before,"rows_outbox_after":counts(),"result":"PASS"}
        try:
            self.compose("pause", "sensor-pantai")
            def unavailable():
                response = self.coastal()
                source = response["body"]["sources"].get("SENSOR_PANTAI", {}) if response["status"] == 200 else {}
                return response if source.get("available") is False and source.get("stale") is True else None
            cached = self.wait("paused sensor produces stale stored reads", unavailable, seconds=45)
            fixture.require(bool(cached["body"]["data"]), "sensor outage hid stored coastal hazards")
            samples = []
            for _ in range(5):
                for path in ("/hazards?source=BMKG", "/hazards?source=SENSOR_PANTAI"):
                    result = self.http("client", path, role="field-team")
                    fixture.require(result["status"] == 200, "source outage broke canonical reads")
                    samples.append({"path":path,"status":result["status"],"elapsed_ms":result["elapsed_ms"]})
            self.evidence["checks"]["outage"] = {"cached_count":len(cached["body"]["data"]),"source":cached["body"]["sources"]["SENSOR_PANTAI"],"read_samples":samples,"load_test":False}
        finally:
            self.compose("unpause", "sensor-pantai")
        self.wait("coastal worker recovers without operator reset", initial, seconds=90)
        self.evidence["checks"]["automatic_recovery"] = "PASS"
        self.save()

    def contract_tests(self):
        suites = {
            "aggregator": ("storage", ["./internal/coastal","./internal/ingest","./internal/store","./internal/source/pantai","./cmd/server"],
                           {"TEST_DATABASE_URL":self.values["DATABASE_URL"]},
                           {"TestCoastalPostgresReplayAfterCheckpointFailure","TestCoastalDLQRequiresAllReplicasAndFence","TestCoastalWriteFailuresNeverAdvanceCursorAndRetryIsIdempotent"}),
            "client-api": ("default", ["./internal/aggregator","./internal/httpapi"], {},
                           {"TestDecodeEnvelopePreservesOptionalCoastalStatus","TestCoastalCachedReadKeepsStaleMetadataWithoutLeakingDiagnostics"}),
        }
        for service,(network,packages,environment,required) in suites.items():
            dockerfile = self.temporary / (service+"-tests.Dockerfile")
            dockerfile.write_text("FROM "+self.go_image+"\nWORKDIR /src\nCOPY go.mod go.sum ./\nRUN go mod download\n", encoding="utf-8")
            tag = self.project+"-tests-"+service
            self.test_images.append(tag)
            self.command(["docker","build","--file",str(dockerfile),"--tag",tag,str(ROOT/"services"/service)],log=service+"-test-build.log",timeout=600)
            env = self.temporary / (service+"-tests.env")
            env.write_text("\n".join(k+"="+v for k,v in environment.items())+"\n", encoding="utf-8")
            env.chmod(0o600)
            name = self.project+"-tests-"+service
            self.test_containers.append(name)
            result = self.command(["docker","run","--rm","--name",name,"--network",self.project+"_"+network,"--env-file",str(env),"--mount","type=bind,source="+str(ROOT/"services"/service)+",target=/src,readonly","--workdir","/src",tag,"go","test","-json","-race","-p","1","-count=1","-skip","^TestOfficialSensorContract$",*packages],log=service+"-tests.jsonl",check=False,timeout=600)
            passed, skipped = [], []
            for line in result.stdout.splitlines():
                try: event = json.loads(line)
                except ValueError: continue
                if event.get("Action") == "pass" and event.get("Test"): passed.append(event["Test"])
                if event.get("Action") == "skip": skipped.append(event.get("Test",event.get("Package")))
            self.evidence["contract_tests"][service] = {"exit_code":result.returncode,"passed_tests":passed,"skipped_tests":skipped}
            self.save()
            fixture.require(result.returncode == 0 and required <= set(passed) and not skipped, service+" coastal contracts failed or were skipped")

    def run(self):
        self.prepare()
        try:
            self.evidence["existing_states_before"] = self.existing_states()
            self.compose("up","--build","-d","--wait","--wait-timeout","180",log="build.log",timeout=600)
            fixture.require(set(self.states()) == fixture.SERVICES | {"sensor-pantai"}, "expected ten isolated fixture services")
            self.live_checks()
            self.contract_tests()
            self.evidence["result"] = "PASS"
        except Exception as error:
            self.evidence.update(result="FAIL",failure=self.redact(str(error)))
            self.compose("logs","--no-log-prefix","--tail","120",log="diagnostics.log",check=False)
        finally:
            self.compose("unpause","sensor-pantai",check=False)
            for name in self.test_containers: self.command(["docker","rm","--force",name],check=False)
            cleanup = self.compose("down","--volumes","--remove-orphans",log="cleanup.log",check=False)
            for tag in self.test_images: self.command(["docker","image","rm",tag],check=False)
            self.evidence["existing_states_after"] = self.existing_states()
            self.evidence["cleanup"] = "PASS" if cleanup.returncode == 0 and self.evidence["existing_states_before"] == self.evidence["existing_states_after"] else "FAIL"
            if self.evidence["cleanup"] != "PASS": self.evidence["result"] = "FAIL"
            self.evidence["finished_at"] = fixture.now()
            self.save()
        self.progress(self.evidence["result"]+": "+str(self.folder/"pantai-ingestion.json"))
        return 0 if self.evidence["result"] == "PASS" else 1


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir",type=Path,help="New evidence directory")
    args = parser.parse_args()
    stamp = datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    folder = (args.output_dir or ROOT/"artifacts/m2/runs"/(stamp+"-coastal-"+secrets.token_hex(3))).resolve()
    folder.mkdir(parents=True,exist_ok=False)
    with tempfile.TemporaryDirectory(prefix="coastal-check-") as temporary, (folder/"run.log").open("w",encoding="utf-8") as log:
        class Tee:
            def write(self,text):
                sys.__stdout__.write(text)
                log.write(text)
            def flush(self):
                sys.__stdout__.flush()
                log.flush()
        with contextlib.redirect_stdout(Tee()):
            return Check(folder,Path(temporary)).run()


if __name__ == "__main__":
    raise SystemExit(main())
