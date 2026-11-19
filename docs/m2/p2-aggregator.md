# P2: Aggregator leadership

Only the Kubernetes Lease holder starts BMKG/PVMBG polling, the reconciler and the outbox publisher. Other replicas continue serving reads. Field-report processors remain competing consumers on all replicas, as agreed in D-FR.

The Lease `aggregator-leader` uses resourceVersion compare-and-swap, a 15-second lease, a 10-second renewal deadline and a 2-second retry period. Expiry is measured from local observation of a changed Lease record, never by comparing clocks on different nodes. A fresh process identity prevents a restarted pod from reusing an old term. The Lease transition counter is the storage fencing epoch. Do not delete/reset the Lease while retaining database fences.

Before starting workers the holder calls `Fence(epoch)` and restores checkpoints. Every upstream HTTP request and ingestion write rechecks ownership against the API server. Term loss cancels workers, drains them and shuts down the process; Kubernetes restarts the pod. There remains a check-to-request race; the upstream APIs do not accept fencing tokens. Effectively-once storage relies on deterministic event IDs and store-side fencing, rather than an absolute exactly-once network guarantee.

Checkpoints contain source watermarks and BMKG event/warning correlation state. They are saved only after `UpsertReplicated` confirms every assigned data replica, including move targets. The write holds the placement lock so a concurrent map flip cannot discard an acknowledged target. With two copies acknowledged, one node loss cannot leave a surviving cursor ahead of its data. A missing replica pauses checkpoint advancement until replay/reconciliation can fill it; read APIs keep their partial-result behavior. A crash between the two operations replays data. Unresolved warnings and mapping failures do not advance their cursor. The existing 5,000-event BMKG cache pruning policy remains; this is not an unlimited historical join store.

Hazard reads use a per-instance cache with a one-second maximum age, 128 filters, and a 16 MiB budget. Entries above 1 MiB and errors bypass the cache. Audit reads query storage directly. Followers reload durable source status every polling interval (three seconds by default). If a leader stops updating a source, its status expires at the freshness deadline. Immediate error details are local to the leader until a later checkpoint. Client API closes one Aggregator connection per 32 reads so persistent connections can reach new replicas.

Kubernetes starts three replicas with hostname topology spread, a five-second preStop delay, maxUnavailable=0, and a PDB retaining two replicas. The HTTP readiness probe checks that at least one store answers. The TCP liveness probe allows the process to stay up during storage outages. Spread allows five pods across three nodes during scaling and rolling updates.

## Runbook and evidence

- `make p2-status`: Lease holder/epoch and pod placement. Match the holder's pod prefix to `X-Instance-ID` in BMKG/PVMBG logs.
- `make p2-failover`: asks for confirmation, runs Tester Agent `kill-leader`. Record Lease/poll logs before and after; elapsed takeover must be <=60 seconds. Verify checksum and duplicate source IDs.
- `make tester S=clock-skew`: capture the same logs, then verify stale epochs were rejected by storage. For freeze, the VM host operator pauses the current leader's worker VM for >15 seconds and resumes it. Never pause the control-plane node. Record failed writes from the old epoch and successful takeover.
- `TARGET_URL=http://<aggregator-LB> make p2-load`: 10,000 VU for 120 seconds, scale 3 -> 5 -> 2, restore original replica count on exit. TARGET_URL must route through the load balancer; a pod port-forward cannot measure scaling. Transcripts and JSON go to `artifacts/m2/runs/`.
- Set `ROLLOUT_IMAGE=<registry>/aggregator:<new-sha>` for a version change during the load test. This enables the zero-failure threshold for the full run, including controlled rejections. The script requires the rollout to finish during load. It restores the original replica count and leaves the new image deployed.
- Measure the 1-vs-3 ratio separately. Scale to one replica, wait for readiness, and run `TARGET_URL=... k6 run --summary-export one.json tests/k6/m2-aggregator.js`. Repeat with three replicas and save `three.json`, using the same data, host, and configuration. Restore the initial replica count. Divide the `responses_200.rate` in `three.json` by the rate in `one.json`. The target is ≥2.0.
- Before/after each failure: `make p1-checksum`, upstream logs and storage duplicate checks. A green k6 result alone cannot prove ingestion correctness. Save logs with UTC timestamps under `artifacts/m2/runs/`.

PostgreSQL verification on a disposable local instance passed with `TEST_DATABASE_URL=... go test -race ./services/aggregator/...`. Independent schemas model storage units; this verifies transactions, fencing, replica acknowledgements and replay, not physical node failure.

Local checks: `cd services/aggregator && go test -race ./...`; Client API tests; BMKG/PVMBG tests; `kubectl kustomize deploy/k8s/overlays/dev`; `bash -n scripts/m2/p2-load.sh`; `k6 inspect tests/k6/m2-aggregator.js`.

## Cluster acceptance

Cluster evidence requires the demo k3s context, access to suspend a worker VM, a load-balancer URL reachable from the load generator, and a distinct rollout image. Record the runs above on the same build. During partition and freeze tests, check the old leader's writes explicitly. Lease verification and SQL commit are separate operations, so an isolated store can still contain an unacknowledged write.

API fields follow the [Kubernetes Lease reference](https://kubernetes.io/docs/reference/kubernetes-api/coordination/lease-v1/).
