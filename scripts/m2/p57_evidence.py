#!/usr/bin/env python3
"""P5.7: read-only evidence collector for a real fault window.

Run during/after a controlled game day or a P5 breaker-timeline test. Collects:
(1) Prometheus service metrics, (2) a Jaeger multi-service trace and span durations,
(3) correlated/error log excerpts, (4) a concise checklist for screen recording.
No faults are injected, no application state is changed. This is not an automatic PASS.
"""
from __future__ import annotations

import argparse
import datetime
import json
from pathlib import Path
import re
import secrets
import subprocess
import sys
import urllib.parse

sys.path.insert(0, str(Path(__file__).resolve().parent))
import verify_common as common

ROOT=Path(__file__).resolve().parents[2]
NS='sistem-informasi-kebencanaan-bnpb'
LOG_KEYWORDS=('error','warn','stale','breaker','failed','oomkill','retry','timeout',
              'correlation_id','trace_id','outbox','consume')


def kube(*args,timeout=45):
    result=subprocess.run(['kubectl',*args],capture_output=True,text=True,
                          encoding='utf-8',errors='replace',timeout=timeout)
    return {'returncode':result.returncode,'stdout':result.stdout,'stderr':result.stderr[:250]}


def get_json(base,path):
    r=common.http_json(base,path,timeout=20)
    return r['body'] if r['status']==200 and isinstance(r['body'],dict) else None


def safe_log(line):
    # Even if a service accidentally logs credentials, do not write them to evidence.
    line=re.sub(r'(?i)(authorization\s*[:=]\s*bearer\s+)\S+',r'\1[REDACTED]',line)
    line=re.sub(r'(?i)("(?:token|password|secret|api_key)"\s*:\s*")[^"]*',r'\1[REDACTED]',line)
    return line[:2000]


def selected_logs(ns,app,since_minutes,max_lines):
    r=kube('-n',ns,'logs','-l','app='+app,'--all-containers=true',
           '--since='+str(since_minutes)+'m','--prefix=true',
           '--max-log-requests=15','--tail='+str(max_lines),timeout=70)
    if r['returncode']:
        return {'error':'kubectl logs failed: '+r['stderr'],'lines':[]}
    lines=[safe_log(s) for s in r['stdout'].splitlines()
           if any(w in s.lower() for w in LOG_KEYWORDS)]
    return {'lines':lines[-max_lines:],'matched_count':len(lines)}


def parse_traces(result):
    candidates=[]
    for tr in (result or {}).get('data',[]):
        processes=tr.get('processes') or {}
        spans=[]
        for sp in tr.get('spans',[]):
            proc=processes.get(sp.get('processID'),{})
            svc=proc.get('serviceName') or sp.get('processID') or 'unknown'
            tags={t.get('key'):t.get('value') for t in sp.get('tags',[]) if isinstance(t,dict)}
            spans.append({'service':svc,'operation':sp.get('operationName'),
                          'duration_ms':round(float(sp.get('duration',0))/1000,3),
                          'start_time_us':sp.get('startTime'),
                          'span_id':sp.get('spanID'),
                          'parent_count':len(sp.get('references',[])),
                          'correlation_id':tags.get('correlation_id'),
                          'error':tags.get('error')})
        services=sorted({sp['service'] for sp in spans})
        candidate={'trace_id':tr.get('traceID'), 'services':services,
                   'span_count':len(spans), 'spans':sorted(spans,key=lambda sp:sp['start_time_us'] or 0)}
        candidates.append(candidate)
    return sorted(candidates,key=lambda t:(len(t['services']),t['span_count']),reverse=True)


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--jaeger-url',required=True,help='Jaeger query base URL (e.g. http://127.0.0.1:16686)')
    parser.add_argument('--prometheus-url',required=True,help='Prometheus base URL (e.g. http://127.0.0.1:9090)')
    parser.add_argument('--namespace',default=NS)
    parser.add_argument('--since-minutes',type=int,default=20)
    parser.add_argument('--trace-service',default='aggregator')
    parser.add_argument('--limit',type=int,default=100)
    parser.add_argument('--max-log-lines',type=int,default=250)
    parser.add_argument('--output-dir',type=Path)
    args=parser.parse_args()
    if args.since_minutes<1 or args.limit<1: parser.error('positive since-minutes and limit required')
    stamp=datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%dT%H%M%SZ')
    out=(args.output_dir or ROOT/'artifacts/m2/runs'/f'{stamp}-p57-evidence-{secrets.token_hex(3)}').resolve()
    out.mkdir(parents=True,exist_ok=False)
    query=urllib.parse.urlencode({'service':args.trace_service,
                                  'lookback':str(args.since_minutes)+'m','limit':str(args.limit)})
    traces=parse_traces(get_json(args.jaeger_url,'/api/traces?'+query))
    common.write_json(out/'jaeger-traces-summary.json',traces[:20])
    best=next((tr for tr in traces if len(tr['services'])>=2),None)
    services=get_json(args.jaeger_url,'/api/services')
    prom_queries={
      'up':'sum by(job) (up)',
      'request_rate':'sum by(job,code) (rate(http_requests_total[5m]))',
      'request_p95':'histogram_quantile(0.95,sum by(job,le)(rate(http_request_duration_seconds_bucket[5m])))'
    }
    metrics={}
    for key,expr in prom_queries.items():
        response=common.http_json(args.prometheus_url,
            '/api/v1/query?'+urllib.parse.urlencode({'query':expr}),timeout=20)
        body=response.get('body') or {}
        values=(body.get('data') or {}).get('result') or [] if isinstance(body,dict) else []
        metrics[key]={'http_status':response['status'],'series':values[:50]}
    common.write_json(out/'prometheus.json',metrics)
    logs={}
    for app in ('aggregator','notification-consumer','dashboard-consumer','client-api','auth'):
        logs[app]=selected_logs(args.namespace,app,args.since_minutes,args.max_log_lines)
    common.write_json(out/'logs-filtered.json',logs)
    correlated_ids=[]
    if best:
        correlated_ids=sorted({str(sp['correlation_id']) for sp in best['spans'] if sp.get('correlation_id')})
    report={'test':'P5.7','timestamp':common.isoformat(),'since_minutes':args.since_minutes,
       'services_available':(services or {}).get('data') or [],
       'candidate_traces':len(traces),'best_multiservice_trace':best,
       'correlation_ids_on_trace':correlated_ids,
       'metric_series':{k:len(v['series']) for k,v in metrics.items()},
       'matched_logs':{k:v.get('matched_count',0) for k,v in logs.items()},
       'status':'EVIDENCE_COLLECTED' if best else 'PARTIAL',
       'caveat':'Must demonstrate one *specific ongoing failure* correlated across metrics, trace and logs; a multi-service trace alone is not proof.'}
    common.write_json(out/'report.json',report)
    # Human-readable handoff can be copied into the video checklist.
    md=['# P5.7 — Observability verification','',f'- Window: {args.since_minutes} minutes',
        f'- Traces returned: {len(traces)}',f'- Selected trace: {best["trace_id"] if best else "NONE"}',
        f'- Spanned services: {", ".join(best["services"]) if best else "NONE"}',
        '- Status: '+report['status'],'',
        '## Capture during demo','1. Trigger an observable fault using an existing safe P5 test.',
        '2. Show Grafana panel (error rate/latency/upstream breaker) with the same timestamp.',
        '3. Show Jaeger trace ID and span durations (from jaeger-traces-summary.json).',
        '4. Show log lines carrying matching correlation ID / trace ID and explain root cause.',
        '5. State recovery outcome; do not claim a trace explains the fault unless IDs and timing match.','']
    (out/'DEMO-CHECKLIST.md').write_text('\n'.join(md),encoding='utf-8')
    print('Evidence:',out)
    print('Trace:',best['trace_id'] if best else 'not found')
    print('Services:',best['services'] if best else [])
    print('STATUS:',report['status'],'— manual correlation still required')
    return 0 if best else 1

if __name__=='__main__':
    sys.exit(main())
