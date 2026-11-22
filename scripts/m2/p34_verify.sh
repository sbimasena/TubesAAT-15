#!/usr/bin/env bash
# P3.4 non-destructive evidence bundle. Runs replay only in disposable Compose,
# then inventories cluster DLQ WITHOUT replaying any cluster message.
set -euo pipefail
cd "$(dirname "$0")/../.."
[[ -f scripts/m2/p3-check.py && -f scripts/m2/p3-replay-cluster.py ]] || {
  echo 'Run from repository with existing P3 verification scripts' >&2; exit 2;
}
command -v python3 >/dev/null || { echo 'python3 not installed' >&2; exit 2; }
command -v docker >/dev/null || { echo 'docker not installed for isolated checks' >&2; exit 2; }
: "${AUDIT_URL:?Set AUDIT_URL to the Aggregator Audit API base URL (e.g., http://127.0.0.1:18083)}"
: "${AUDIT_TOKEN:?Set AUDIT_TOKEN in the environment (never put it on the command line)}"
OUT="${OUTPUT_DIR:-artifacts/m2/runs/$(date -u +%Y%m%dT%H%M%SZ)-p34-handoff}"
mkdir -p "$OUT"
OUT="$(cd "$OUT" && pwd)"
[[ ! -e "$OUT/isolated" && ! -e "$OUT/cluster" ]] || { echo "Output already exists: $OUT" >&2; exit 2; }
echo "P3.4: isolated replay test -> $OUT/isolated"
set +e
python3 -B scripts/m2/p3-check.py --output-dir "$OUT/isolated" > "$OUT/isolated.log" 2>&1
isolated_rc=$?
set -e
echo "P3.4: cluster inventory (READ ONLY) -> $OUT/cluster"
set +e
python3 -B scripts/m2/p3-replay-cluster.py --audit-url "$AUDIT_URL" --output-dir "$OUT/cluster" > "$OUT/cluster.log" 2>&1
cluster_rc=$?
set -e
ISOLATED_RC="$isolated_rc" CLUSTER_RC="$cluster_rc" REPORT_DIR="$OUT" python3 - <<'PY'
import json, os, pathlib
root=pathlib.Path(os.environ['REPORT_DIR'])
def load(path):
    try: return json.loads(path.read_text())
    except (OSError, ValueError): return {}
iso=load(root/'isolated'/'p3-check.json')
cluster=load(root/'cluster'/'report.json')
info={
 'test':'P3.4', 'mode':'isolated replay + read-only cluster inventory',
 'isolated_exit_code':int(os.environ['ISOLATED_RC']),
 'cluster_exit_code':int(os.environ['CLUSTER_RC']),
 'isolated_result':iso.get('result'),
 'cluster_inventory':cluster.get('inventory_before',{}),
 'cluster_mutated':False,
 'claim':'Isolated replay may pass; cluster recovery is NOT verified by read-only inventory.',
 'status':'PARTIAL' if os.environ['ISOLATED_RC']=='0' and os.environ['CLUSTER_RC']=='0' else 'FAIL',
 'remaining':'Mapper compatibility and recovery of parked cluster DLQ still require separate approved demonstration.'
}
(root/'summary.json').write_text(json.dumps(info,indent=2,ensure_ascii=False)+'\n')
print(json.dumps(info,indent=2,ensure_ascii=False))
PY
echo "Evidence: $OUT"
[[ "$isolated_rc" == 0 && "$cluster_rc" == 0 ]] || exit 1
