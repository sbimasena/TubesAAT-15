#!/usr/bin/env python3
"""P5.3: test BMKG-only p95<300ms with PVMBG delay=3s while Pantai is down.

Requires existing scripts/m2/verify_common.py in the repository.
The cluster-changing mode is opt-in (--apply-faults --context EXACT_KUBECTL_CONTEXT).
Always attempts to restore the original Pantai replicas and PVMBG env override.
"""
from __future__ import annotations

import argparse
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import secrets
import socket
import subprocess
import sys
import time
import urllib.parse

sys.path.insert(0, str(Path(__file__).resolve().parent))
import verify_common as common

ROOT = Path(__file__).resolve().parents[2]
NS = "sistem-informasi-kebencanaan-bnpb"


def kube(*args, timeout=90):
    r = subprocess.run(["kubectl", *args], text=True, stdout=subprocess.PIPE,
                       stderr=subprocess.PIPE, timeout=timeout)
    if r.returncode:
        raise RuntimeError("kubectl " + " ".join(args[:5]) + ": " + r.stderr.strip()[:300])
    return r.stdout.strip()


def deploy(name, ns):
    return json.loads(kube("-n", ns, "get", "deployment", name, "-o", "json"))


def wait_rollout(name, ns):
    kube("-n", ns, "rollout", "status", "deployment/" + name, "--timeout=150s", timeout=165)


def ready(name, ns):
    d=deploy(name,ns)
    return int(d.get("status",{}).get("readyReplicas") or 0)


def wait_ready(name, ns, count):
    deadline=time.monotonic()+100
    while time.monotonic()<deadline:
        if ready(name,ns)==count:
            return
        time.sleep(2)
    raise RuntimeError(f"{name} did not reach {count} ready replicas")


def local_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1",0))
        return sock.getsockname()[1]


def health_pvmbg(ns):
    port=local_port()
    p=subprocess.Popen(["kubectl","-n",ns,"port-forward","svc/pvmbg",f"{port}:8082"],
                       stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
    try:
        for _ in range(50):
            if p.poll() is not None:
                raise RuntimeError("PVMBG port-forward stopped early")
            res=common.http_json(f"http://127.0.0.1:{port}","/health",timeout=1)
            if res['status']==200 and isinstance(res['body'],dict):
                return res['body']
            time.sleep(0.2)
        raise RuntimeError("PVMBG health endpoint is not reachable")
    finally:
        p.terminate()
        try: p.wait(timeout=5)
        except subprocess.TimeoutExpired: p.kill()


def make_session(args):
    # Each request stream has its own session; the helper automatically refreshes 60s tokens.
    return common.client_session(args)[0]


def sample_stream(source, args, deadline):
    session=make_session(args)
    path="/hazards?"+urllib.parse.urlencode({"source":source,"limit":20})
    samples=[]
    while time.monotonic()<deadline:
        res=session.get(args.client_api_url,path)
        samples.append({"time_utc":common.isoformat(),"status":res['status'],
                        "latency_ms":res['latency_ms'],"error":res['error']})
        if args.interval: time.sleep(args.interval)
    return {"samples":samples,"relogins":session.relogins}


def summarize(samples, threshold):
    ok=[v['latency_ms'] for v in samples if v['status']==200]
    status_counts={}
    for x in samples: status_counts[str(x['status'])]=status_counts.get(str(x['status']),0)+1
    return {"count":len(samples),"statuses":status_counts,
            "p50_ms":round(common.percentile(ok,0.5),2) if ok else None,
            "p95_ms":round(common.percentile(ok,0.95),2) if ok else None,
            "p99_ms":round(common.percentile(ok,0.99),2) if ok else None,
            "max_ms":round(max(ok),2) if ok else None,
            "all_http_200":len(ok)==len(samples) and bool(samples),
            "pass_target":len(ok)==len(samples) and bool(samples) and common.percentile(ok,0.95)<threshold}


def phase(name,args,out):
    deadline=time.monotonic()+args.phase_seconds
    with ThreadPoolExecutor(max_workers=2) as pool:
        bmkg=pool.submit(sample_stream,"BMKG",args,deadline)
        pvmbg=pool.submit(sample_stream,"PVMBG",args,deadline)
        b,p=bmkg.result(),pvmbg.result()
    common.write_json(out/(name+"-bmkg.json"),b)
    common.write_json(out/(name+"-pvmbg.json"),p)
    return {"bmkg":summarize(b['samples'],args.target_ms),
            "pvmbg":summarize(p['samples'],args.target_ms),
            "bmkg_relogins":b['relogins'],"pvmbg_relogins":p['relogins']}


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--client-api-url",required=True)
    common.add_client_auth_args(parser)
    parser.add_argument("--namespace",default=NS)
    parser.add_argument("--context",help="Exact expected kubectl context; REQUIRED with --apply-faults")
    parser.add_argument("--apply-faults",action="store_true",help="Temporarily override PVMBG delay and scale Pantai to 0")
    parser.add_argument("--phase-seconds",type=int,default=45)
    parser.add_argument("--interval",type=float,default=0.1)
    parser.add_argument("--target-ms",type=float,default=300.0)
    parser.add_argument("--output-dir",type=Path)
    args=parser.parse_args()
    if args.phase_seconds<10 or args.interval<0: parser.error("phase >=10 s, interval >=0")
    if args.apply_faults and not args.context: parser.error("--context required for mutations")
    context=kube("config","current-context")
    if args.context and context!=args.context:
        parser.error(f"context mismatch: active {context!r}, expected {args.context!r}")
    stamp=datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%SZ')
    out=(args.output_dir or ROOT/'artifacts/m2/runs'/f'{stamp}-p53-concurrent-{secrets.token_hex(3)}').resolve()
    out.mkdir(parents=True,exist_ok=False)
    report={"test":"P5.3","context":context,"started_at":common.isoformat(),
            "faults_requested":args.apply_faults,"result":"BLOCKED","phases":{},"restore":{}}
    ns=args.namespace
    pvmbg=deploy('pvmbg',ns)
    containers=pvmbg['spec']['template']['spec']['containers']
    if len(containers)!=1: raise RuntimeError("Expected one PVMBG container")
    original_env=next((v for v in containers[0].get('env',[]) if v.get('name')=='PVMBG_DELAY_MS'),None)
    if original_env and 'valueFrom' in original_env:
        raise RuntimeError('PVMBG_DELAY_MS uses valueFrom; automatic restoration is not supported')
    original_pantai=int(deploy('sensor-pantai',ns)['spec'].get('replicas') or 0)
    if original_pantai<1: raise RuntimeError('Pantai already down; refusing to alter existing outage')
    pvmbg_changed=False
    pantai_changed=False
    try:
        before_health=health_pvmbg(ns)
        report['pvmbg_delay_ms_before']=before_health.get('simulated_delay_ms')
        if args.apply_faults:
            kube('-n',ns,'set','env','deployment/pvmbg','PVMBG_DELAY_MS=3000')
            pvmbg_changed=True
            wait_rollout('pvmbg',ns)
        active=health_pvmbg(ns)
        report['pvmbg_delay_ms_during']=active.get('simulated_delay_ms')
        if active.get('simulated_delay_ms')!=3000:
            report['reason']='PVMBG is not configured at 3000 ms. Use --apply-faults.'
            return 3
        report['phases']['baseline']=phase('baseline',args,out)
        if not args.apply_faults:
            report['reason']='Sensor outage was not created; use --apply-faults on a controlled cluster.'
            return 3
        kube('-n',ns,'scale','deployment/sensor-pantai','--replicas=0')
        pantai_changed=True
        wait_ready('sensor-pantai',ns,0)
        report['outage_confirmed']=True
        report['phases']['during_outage']=phase('during-outage',args,out)
        kube('-n',ns,'scale','deployment/sensor-pantai','--replicas='+str(original_pantai))
        pantai_changed=False
        wait_ready('sensor-pantai',ns,original_pantai)
        report['phases']['after_recovery']=phase('after-recovery',args,out)
        bmkg_results=[report['phases'][key]['bmkg']['pass_target'] for key in
                      ('baseline','during_outage','after_recovery')]
        pvmbg_present=report['phases']['during_outage']['pvmbg']['count']>0
        report['result']='PASS' if all(bmkg_results) and pvmbg_present else 'FAIL'
        if not pvmbg_present: report['reason']='No concurrent PVMBG requests completed'
        return 0 if report['result']=='PASS' else 1
    except Exception as error:
        report['result']='FAIL'
        report['reason']=str(error)[:300]
        return 1
    finally:
        if pantai_changed:
            try:
                kube('-n',ns,'scale','deployment/sensor-pantai','--replicas='+str(original_pantai))
                wait_ready('sensor-pantai',ns,original_pantai)
                report['restore']['pantai']='OK'
            except Exception as error:
                report['restore']['pantai']='FAILED: '+str(error)[:150]
        else:
            report['restore']['pantai']='not changed or already restored'
        if pvmbg_changed:
            try:
                if original_env is None:
                    kube('-n',ns,'set','env','deployment/pvmbg','PVMBG_DELAY_MS-')
                else:
                    kube('-n',ns,'set','env','deployment/pvmbg',
                         'PVMBG_DELAY_MS='+original_env['value'])
                wait_rollout('pvmbg',ns)
                report['restore']['pvmbg_delay_ms']=health_pvmbg(ns).get('simulated_delay_ms')
            except Exception as error:
                report['restore']['pvmbg_delay_ms']='FAILED: '+str(error)[:150]
        else:
            report['restore']['pvmbg_delay_ms']='not changed'
        report['completed_at']=common.isoformat()
        common.write_json(out/'report.json',report)
        print('P5.3 evidence:',out)
        for name,p in report['phases'].items():
            print(name, 'BMKG p95=',p['bmkg']['p95_ms'],'ms, PVMBG completed=',p['pvmbg']['count'])
        print('RESULT:',report['result'],report.get('reason',''))
        if any(str(v).startswith('FAILED:') for v in report['restore'].values()):
            print('WARNING: cleanup failed; manually inspect deployments!',file=sys.stderr)


if __name__=='__main__':
    sys.exit(main())
