# P4 satellite processing and acceptance

## Implemented

- `StreamClient` authenticates session/SSE requests, forbids redirects, bounds headers and read-idle time, cancels blocked reads, and reconnects serially with jittered exponential backoff (0.5-15 seconds). HTTP 429/503 and disconnects retry; authentication/protocol errors and failed commits stop the worker. SSE data is limited to 1 MiB per frame and retains raw lexical coordinates. The leader applies batches of up to 128 frames or 2 MiB. A 200 ms timer flushes partial batches even while the stream remains open.
- Resume requests the **committed offset itself** using `from` and Last-Event-ID. The parser skips offsets already committed and rejects gaps. This supports either inclusive or exclusive resume semantics without guessing `offset+1`. An initial connection accepts the feed's first offset; subsequent offsets must be contiguous. Interrupted final frames are replayed, not dispatched at EOF.
- `State` applies ordered detection/update/retract mutations, deduplicates identities, retains late data, computes event-time clusters and counts singletons. `Fire.Hazard` maps WILDFIRE fields and the required five attributes. Raw detection keys remain unchanged.
- Fire identity survives updates and loss of the original minimum key. On a split, the old ID stays with the component with greatest key overlap; ties use the smallest key. A merge keeps the smallest nominated ID. Other surviving split components get deterministic distinct IDs. Identity ownership persists even when a fire disappears. Applying mutations sequentially inside each batch preserves final fire identities across checkpoint boundaries. A grid limits reclustering to components affected by the current mutation. The prior checkpoint remains available until the batch succeeds.
- `Processor` stores raw detection state, identity history, offset and pending projection in `ingest_cursor` through `SaveCursorReplicated`. It saves this recovery plan on at least RF=2 units **before** canonical effects, materializes through `UpsertReplicated`, then clears pending effects durably. Restore finishes interrupted effects before streaming. Retries are idempotent; an old epoch is fenced.
- Withdrawn fires leave internal replicated tombstones. Hazard reads, Audit events/checksum/location and analytics exclude them, while replication/export retains them to prevent resurrection. Consumers accept M2 sources and journal withdrawals; notifications do not simulate a new alert for a withdrawal. Satellite dashboard ordering uses the checkpoint's monotonic ingestion timestamp, avoiding divergent replica revision numbers.
- Metrics include processed offset, offset/event-time lag, retained late detections, singletons, frame-to-commit latency and processor readiness. `ObserveSession` uses the feed's virtual clock. The latency histogram uses the first received frame of the batch and the replicated commit completion time once per materialized effect. The Grafana Overview shows offset lag, p95, late and singleton counts, and readiness; zero readiness means no recovered live processor and is not evidence of zero lag. `GET /internal/v1/status` reads persisted satellite progress on followers without opening another stream.

## Explicit decisions and limitations

The Aggregator starts the worker only for the current leader when `SATELLITE_TOKEN` is present. Docker Compose reads the ignored `.env`. Kubernetes mounts the optional `satellite-token` Secret, which `scripts/k8s/deploy.sh` creates from `.env`. Updating the token through the deploy script triggers a rollout.

The authenticated practice feed sends new VIIRS records in `detection`, updates as `key` plus a partial `changes` object, and retractions as `key` plus `reason`. The adapter checks a supplied payload offset against the SSE id. A patch changes the stored FRP or confidence without replacing unrelated fields or the fire identity. A brightness-only patch commits its offset without materializing a new fire revision because brightness is outside the canonical record. Retractions remove the referenced detection. Payloads that cannot be interpreted stop the worker at the last successful batch. Database failures trigger checkpoint recovery before reconnection.

D-LATE is **provisional: 24 virtual/event-time hours**. Watermark = maximum observed detection time minus this allowance. The 360-minute clustering edge rule is separate. All over-limit detections remain in durable state and are counted once per detection identity. Late arrivals still recompute affected results, so the watermark does not cause silent loss. No historical state is evicted yet; completion/window-retention policy needs the observed workload and update/retract horizon.

Consumer messages still have a 1 MiB bound; exceptionally large detection-key lists need a reviewed transport strategy before that limit is reached. Full checkpoints are capped at **16 MiB**; exceeding it stops before advancing the offset. The processor copies active state once per batch and recomputes affected components per mutation. Replicated checkpoints still contain the full retained history. Measure checkpoint size and burst behavior on the course feed before testing the storage budget. Move to incremental replicated state if the workload exceeds that budget.

Ingestion timestamps advance monotonically from the preceding checkpoint when host wall time moves backwards; satellite acquisition time remains unchanged. This prevents a newer update/tombstone from losing to an older replica during timestamp-based anti-entropy. Full cluster clock-skew/freeze acceptance remains separate.

The 38-province table is a **project reference**, derived from area-weighted planar WGS84 centroids of the licensed [province boundary dataset](https://github.com/ardian28/GeoJson-Indonesia-38-Provinsi/tree/486e89ca57c9f9910991dbf00afca26297b3baa3). The original data publisher attributes boundaries to BIG. The pinned input URL, SHA-256 and derivation method are in `provinces.json`; [the MIT notice is retained](data/province-centres-LICENSE.txt). These are approximate project centres, not official survey-grade centroid coordinates or a substitute for point-in-polygon classification.

## Observe the real source

The authenticated group session reported the `practice` profile at 60×. Its first recorded prefix had 147 detections, three updates, and one retraction through offset 150. The course-provided curl client and the Go worker authenticated successfully. The Python sampler received Cloudflare HTTP 403 in this workspace.

Run `make p4-sample` where the Python sampler is accepted. If the site rejects Python's client signature, use the course-provided curl command without starting a second consumer. One stream per group is permitted. To capture a bounded practice prefix while keeping the token out of command arguments:

```bash
source scripts/k8s/lib.sh
load_env
mkdir -p artifacts/m2/runs
printf 'header = "Authorization: Bearer %s"\n' "$SATELLITE_TOKEN" |
  curl --config - --no-buffer --silent --show-error --fail --max-time 45 \
    --output artifacts/m2/runs/practice-stream.sse https://satellite.naufarrel.dev/v1/stream
```

Curl exits with code 28 when the capture time elapses. Complete events in the saved prefix can still be replayed. The replay parser discards a final frame that ends mid-record.

## Repeatable checks

`make p4-check` runs race-tested satellite/storage tests and Python recorder tests, recording output under `artifacts/m2/runs/`. Set `TEST_DATABASE_URL` to a disposable PostgreSQL first to enable database tests; without it, the script explicitly reports that database tests are skipped. Tests create isolated schemas and remove them afterwards. Never use this as a physical-node failure claim.

Coverage includes raw SSE preservation, reconnect, offset dedup/gaps, idle cancellation, failed commits, transitive clustering, confidence/severity thresholds, late retention, stable update/retract/merge/split identities and serialized restart/replay. The real-PostgreSQL recovery test interrupts processing after the pending recovery plan is durable, takes over with a new epoch, restores it, checks Audit checksum and verifies withdrawal/read/analytics behavior.

On the local Apple M3, five iterations of the clustering benchmark averaged **5.4 ms for 1,000 dispersed detections** and **48.7 ms for 1,000 detections across 50 groups**. The previous single-run dispersed baseline was 2.64 seconds. A separate SSE-to-PostgreSQL integration run materialized 1,000 unique detections into 50 fires **1.17 seconds after the final frame was sent**, with the feed connection still open. The reconnect test independently verifies checkpoint resume, fire withdrawal, raw keys and that malformed updates leave the offset unchanged. Run the checks with `make p4-check` and a disposable `TEST_DATABASE_URL`.


On 19 November WIB, the Go worker caught up from the beginning of the practice stream to offset 2300, past the session’s starting offset of 2285, with 36 active fires. After replacing the worker and incrementing its database fence epoch, it resumed through offset 2341 with 37 active fires. The measured frame-to-commit p95 fell in the **≤0.50-second** histogram bucket across 117 effects. The recorded 151-frame prefix yielded exactly the same final state and Audit checksum with and without a restart. Both tests used local disposable PostgreSQL schemas, so Kubernetes scheduling and node failover remain separate checks.

To repeat these checks, set `TEST_DATABASE_URL` to a disposable PostgreSQL instance. Pass `SATELLITE_LIVE_TEST=1` with `SATELLITE_TOKEN` in the environment to run `TestCoursePracticeFeed`. Pass `SATELLITE_CAPTURE` as the absolute path to a saved SSE prefix to run `TestRecordedPracticeReplay` offline.

```bash
go test -race -count=1 -v ./services/aggregator/internal/satellite \
  -run 'TestCoursePracticeFeed|TestRecordedPracticeReplay'
```

## Live acceptance

1. Set the token in the ignored `.env` for deployment. Keep only one practice feed connection open.
2. Deploy through the team’s Kubernetes script. Check `GET /internal/v1/status`, the Aggregator logs, and the Audit API with `source=SATELIT`. Confirm the offset advances under the leader and that ingest resumes from the last committed offset after a disconnect.
3. Run the course 60× profile. Record the peak offset lag, time between the last burst frame and zero lag, p95 frame-to-commit latency, and the final detection and fire totals. The catch-up target is ≤60 seconds after the burst ends. Repeat the same feed with a worker interruption and compare the final checksum and fire memberships against the uninterrupted run.
4. Export the paginated SATELIT Audit result and compare it with the assistant’s reference. Match fires by sorted `detection_keys`, then compare the canonical counts, severity, coordinates and other fields supplied by the reference. Report missing, extra and mismatched groups separately. Run the live feed for the demo and retain the outputs with the assignment evidence.

## Remaining validation

| Part | Next action |
|---|---|
| Production state/performance | Review the replicated checkpoint/tombstone contract; replace full-state copies if observed workload exceeds the 16 MiB/CPU budget. Measure dense bursts, not only distributed synthetic points. |
| Lifecycle assumptions | Confirm deterministic split/merge lineage and provisional lateness against observed behavior/reference expectations; finalize the state-retention/window-completion policy. |
| Real acceptance | Reachable cluster/VM access and live token: run 60x, kill, reconnect and checksum comparison; capture p95, lag and <=60-second catch-up evidence. |
| Reference result | Run the full-stream comparison when assistant references are released. |

Course practice compatibility and local recovery are verified. Cluster capacity, Kubernetes failover, the full-stream reference comparison and the demo still need evidence.
