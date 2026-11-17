# P2: Aggregator leadership (Fithra)

Only the Kubernetes Lease holder starts BMKG/PVMBG polling, the reconciler and the outbox publisher. Other replicas continue serving reads. Field-report processors remain competing consumers on all replicas, as agreed in D-FR.

The Lease `aggregator-leader` uses resourceVersion compare-and-swap, a 15-second lease, a 10-second renewal deadline and a 2-second retry period. Expiry is measured from local observation of a changed Lease record, never by comparing clocks on different nodes. A fresh process identity prevents a restarted pod from reusing an old term. The Lease transition counter is the storage fencing epoch. Do not delete/reset the Lease while retaining database fences.

Before starting workers the holder calls `Fence(epoch)` and restores checkpoints. Every upstream HTTP request and ingestion write rechecks ownership against the API server. Term loss cancels workers, drains them and shuts down the process; Kubernetes restarts the pod. There remains a check-to-request race; the upstream APIs do not accept fencing tokens. Effectively-once storage relies on deterministic event IDs and store-side fencing, rather than an absolute exactly-once network guarantee.

Checkpoints contain source watermarks and BMKG event/warning correlation state. They are saved only after Upsert succeeds. A crash between the two operations replays data. Unresolved warnings and mapping failures do not advance their cursor. The existing 5,000-event BMKG cache pruning policy remains; this is not an unlimited historical join store.

Fithra approved D-READ/D-LB on 17 November: one-second maximum cache age for hazard reads, with at most 128 filters and 16 MiB of serialized data per instance (entries above 1 MiB bypass it). Errors are not cached; Audit APIs bypass the cache. Source freshness is separate from the read cache. Followers reload the last durable status every polling interval (three seconds by default); a failed leader stops advancing it, so it expires stale after the normal freshness deadline. Immediate error details are local to the leader until a later checkpoint. Client API closes one Aggregator connection per 32 reads so persistent connections can reach new replicas. This does not yet prove the required throughput ratio.

Kubernetes starts three replicas with hostname topology spread, readiness/liveness TCP probes, a five-second preStop delay, maxUnavailable=0 and a PDB retaining two replicas. Spread allows five pods across three nodes during scaling and rolling updates.

## Runbook and evidence

- `make p2-status`: Lease holder/epoch and pod placement. Match the holder's pod prefix to `X-Instance-ID` in BMKG/PVMBG logs.
- `make p2-failover`: asks for confirmation, runs Tester Agent `kill-leader`. Record Lease/poll logs before and after; elapsed takeover must be <=60 seconds. Verify checksum and duplicate source IDs.
- `make tester S=clock-skew`: capture the same logs, then verify stale epochs were rejected by storage. For freeze, the VM host operator pauses the current leader's worker VM for >15 seconds and resumes it. Never pause the control-plane node. Record failed writes from the old epoch and successful takeover.
- `TARGET_URL=http://<aggregator-LB> make p2-load`: 10,000 VU for 120 seconds, scale 3 -> 5 -> 2, restore original replica count on exit. TARGET_URL must route through the load balancer; a pod port-forward cannot measure scaling. Transcripts and JSON go to `artifacts/m2/runs/`.
- Set `ROLLOUT_IMAGE=<registry>/aggregator:<new-sha>` for a version change during the load test. The new image stays deployed; the script restores replica count only. For P2.8 inspect failed requests as well as the overall error threshold: the criterion requires zero failed requests.
- Measure the 1-vs-3 ratio separately: scale to 1, wait for rollout, run `TARGET_URL=... k6 run tests/k6/m2-aggregator.js`; repeat at 3 with identical data, host and configuration. Compare `responses_200` rates, not total requests including rejection. This script measures the Aggregator directly; repeat end-to-end via Client API/Auth with Hasbullah.
- Before/after each failure: `make p1-checksum`, upstream logs and storage duplicate checks. A green k6 result alone cannot prove ingestion correctness. Save logs with UTC timestamps under `artifacts/m2/runs/`.

Local checks: `cd services/aggregator && go test -race ./...`; Client API tests; BMKG/PVMBG tests; `kubectl kustomize deploy/k8s/overlays/dev`; `bash -n scripts/m2/p2-load.sh`; `k6 inspect tests/k6/m2-aggregator.js`.

## Remaining blockers and owners

| Part | Evidence / required next step | Owner |
|---|---|---|
| Live P2.1-8 evidence | Current kubecontext `kind-xops-cluster` refuses connections at localhost. Provide the intended reachable k3s kubeconfig and VM-host access before running destructive scenarios, 10k VU or a rollout. No cluster performance/failure result is claimed. | Sakti (cluster/access), Fithra (execute P2 tests) |
| Partition fencing | `Router.Fence` skips unreachable units and succeeds when one answers. A partitioned zombie can still write an unfenced isolated unit; API verification narrows but cannot remove the race after verification. Reconciler map changes also need term verification after a pause. Review the store barrier/quorum policy before claiming P2.4/5. | Sakti (store), Fithra (coordination integration) |
| Cursor/data durability | `Upsert` and `SaveCursor` are separate, each permits one-copy success. A cursor may survive on a different unit from its data after node/disk loss. Need a shared durable commit/checkpoint protocol or replay-safe retention policy before claiming no gaps under combined failures. | Sakti (store API), Fithra (caller) |
| Loader epoch | `httpapi/load.go` still writes epoch 0 and will be rejected once a leader fences storage. Route operator loads through a current verified term, including requests arriving at followers. | Sakti (P1 loader), Fithra (leader interface) |
| PVMBG poison messages | DLQ primitives exist, but the normal ingestion loop still retries mapping failures and does not park them. Wire the durable DLQ/replay path without advancing past unprotected records. | Hasbullah (P3), Fithra (polling integration) |
| End-to-end scale | Auth sessions remain a separate scaling/restart concern. Measured >=2x throughput and zero-request-failure rollout are still pending, not promised by the cache. | Hasbullah (Auth/API), Fithra (load tests) |
| P4 | Group token absent; live session returns 401 without it. Need sampled wire payloads, resume semantics, replicated detection/update/retract storage and assistant reference data. | Fithra (token/sample/processor), Sakti (store), assistants (token/reference) |

API fields follow the [Kubernetes Lease reference](https://kubernetes.io/docs/reference/kubernetes-api/coordination/lease-v1/). These are implementation notes, not a completed academic report or measured acceptance evidence.
