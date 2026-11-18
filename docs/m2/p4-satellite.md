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

The course specification gives the detection fields and SSE event names but leaves JSON envelopes unspecified. The adapter accepts a direct VIIRS object or one under `detection`, with `frp` and decimal strings or numbers. Updates can carry `previous_key` for an identity change. Retractions can carry `key`, `detection_key`, or the five identity fields. These are implementation assumptions tested against a synthetic server. Confirm each shape with a real group sample before claiming course compatibility. Unexpected frames stop the worker and leave the durable offset at the last successful batch. Database failures trigger recovery from the durable checkpoint before reconnecting.

D-LATE is **provisional: 24 virtual/event-time hours**. Watermark = maximum observed detection time minus this allowance. The 360-minute clustering edge rule is separate. All over-limit detections remain in durable state and are counted once per detection identity. Late arrivals still recompute affected results, so the watermark does not cause silent loss. No historical state is evicted yet; completion/window-retention policy needs the observed workload and update/retract horizon.

Consumer messages still have a 1 MiB bound; exceptionally large detection-key lists need a reviewed transport strategy before that limit is reached. Full checkpoints are capped at **16 MiB**; exceeding it stops before advancing the offset. The processor copies active state once per batch and recomputes affected components per mutation. Replicated checkpoints still contain the full retained history. Measure checkpoint size and burst behavior on the course feed before testing the storage budget. Fithra and Sakti should move to incremental replicated state if the workload exceeds that budget.

Ingestion timestamps advance monotonically from the preceding checkpoint when host wall time moves backwards; satellite acquisition time remains unchanged. This prevents a newer update/tombstone from losing to an older replica during timestamp-based anti-entropy. Full cluster clock-skew/freeze acceptance remains separate.

The 38-province table is a **project reference**, derived from area-weighted planar WGS84 centroids of the licensed [province boundary dataset](https://github.com/ardian28/GeoJson-Indonesia-38-Provinsi/tree/486e89ca57c9f9910991dbf00afca26297b3baa3). The original data publisher attributes boundaries to BIG. The pinned input URL, SHA-256 and derivation method are in `provinces.json`; [the MIT notice is retained](data/province-centres-LICENSE.txt). These are approximate project centres, not official survey-grade centroid coordinates or a substitute for point-in-polygon classification.

## Observe the real source

The assigned source is `https://satellite.naufarrel.dev`. On 17 November, unauthenticated `/v1/session` returned HTTP 401 (`unauthorized`, `token tidak valid`). The service is reachable, but no group token is currently available locally. No authenticated feed behavior is claimed.

Run `make p4-sample` with `SATELLITE_TOKEN` in the environment or the ignored `.env`. Stop any other group stream consumer first: only one connection per group is permitted. The recorder captures session JSON and raw SSE for 60 seconds (plus up to a five-second blocked-read timeout), preserving coordinate/acquisition-time tokens. It writes `session.json`, `stream.sse` and a credential-free summary under ignored `artifacts/m2/runs/`. Limits are 1 MiB per line and 64 MiB per sample; existing files are never overwritten. A failed capture exits nonzero and may retain partial files.

Inspect actual detection/update/retract shapes, identity keys, heartbeat interval, burst sizes, event-time delay, virtual time and offsets. Validate the adapter with recorded fixtures. Use `python3 scripts/m2/satellite-sample.py --seconds 120 --from-offset <offset>` for a second sample. Do not start an exam session or submit a video during development.

## Repeatable checks

`make p4-check` runs race-tested satellite/storage tests and Python recorder tests, recording output under `artifacts/m2/runs/`. Set `TEST_DATABASE_URL` to a disposable PostgreSQL first to enable database tests; without it, the script explicitly reports that database tests are skipped. Tests create isolated schemas and remove them afterwards. Never use this as a physical-node failure claim.

Coverage includes raw SSE preservation, reconnect, offset dedup/gaps, idle cancellation, failed commits, transitive clustering, confidence/severity thresholds, late retention, stable update/retract/merge/split identities and serialized restart/replay. The real-PostgreSQL recovery test interrupts processing after the pending recovery plan is durable, takes over with a new epoch, restores it, checks Audit checksum and verifies withdrawal/read/analytics behavior.

On the local Apple M3, five iterations of the clustering benchmark averaged **5.4 ms for 1,000 dispersed detections** and **48.7 ms for 1,000 detections across 50 groups**. The previous single-run dispersed baseline was 2.64 seconds. A separate SSE-to-PostgreSQL integration run materialized 1,000 unique detections into 50 fires **1.17 seconds after the final frame was sent**, with the feed connection still open. The reconnect test independently verifies checkpoint resume, fire withdrawal, raw keys and that malformed updates leave the offset unchanged. Run the checks with `make p4-check` and a disposable `TEST_DATABASE_URL`.

## Live acceptance

1. Set the token in the ignored `.env`. For a local sample, run `make p4-sample` before starting the worker. Inspect all event shapes and confirm the coordinate and acquisition-time representations used for detection keys.
2. Deploy through the team’s Kubernetes script. Check `GET /internal/v1/status`, the Aggregator logs, and the Audit API with `source=SATELIT`. Confirm the offset advances under the leader and that ingest resumes from the last committed offset after a disconnect.
3. Run the course 60× profile. Record the peak offset lag, time between the last burst frame and zero lag, p95 frame-to-commit latency, and the final detection and fire totals. The catch-up target is ≤60 seconds after the burst ends. Repeat the same feed with a worker interruption and compare the final checksum and fire memberships against the uninterrupted run.
4. Export the paginated SATELIT Audit result and compare it with the assistant’s reference. Match fires by sorted `detection_keys`, then compare the canonical counts, severity, coordinates and other fields supplied by the reference. Report missing, extra and mismatched groups separately. Run the live feed for the demo and retain the outputs with the assignment evidence.

## Remaining work and owners

| Part | Next action | Owner |
|---|---|---|
| Course feed compatibility | Set the group token and verify the deployed adapter against real detection, update and retract payloads. | Fithra, with the token from the assistants |
| Production state/performance | Review the replicated checkpoint/tombstone contract; replace full-state copies if observed workload exceeds the 16 MiB/CPU budget. Measure dense bursts, not only distributed synthetic points. | Fithra (processor), Sakti (storage review) |
| Lifecycle assumptions | Confirm deterministic split/merge lineage and provisional lateness against observed behavior/reference expectations; finalize the state-retention/window-completion policy. | Fithra; assistants clarify ambiguous cases |
| Real acceptance | Reachable cluster/VM access and live token: run 60x, kill, reconnect and checksum comparison; capture p95, lag and <=60-second catch-up evidence. | Sakti (platform/access), Fithra (P4 execution) |
| Reference result | Run the full-stream comparison when assistant references are released. | Assistants (reference), Fithra (comparison) |

Course-feed compatibility, cluster capacity and official acceptance still need evidence.
