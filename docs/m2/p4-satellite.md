# P4: satellite implementation handoff (Fithra)

## Implemented

- `StreamClient` authenticates session/SSE requests, forbids redirects, bounds headers and read-idle time, cancels blocked reads, and reconnects serially with jittered exponential backoff (0.5-15 seconds). HTTP 429/503 and disconnects retry; authentication/protocol errors and failed commits stop the worker. SSE data is limited to 1 MiB per frame and retains raw lexical coordinates.
- Resume requests the **committed offset itself** using `from` and Last-Event-ID. The parser skips offsets already committed and rejects gaps. This supports either inclusive or exclusive resume semantics without guessing `offset+1`. An initial connection accepts the feed's first offset; subsequent offsets must be contiguous. Interrupted final frames are replayed, not dispatched at EOF.
- `State` applies ordered detection/update/retract mutations, deduplicates identities, retains late data, computes event-time clusters and counts singletons. `Fire.Hazard` maps WILDFIRE fields and the required five attributes. Raw detection keys remain unchanged.
- Fire identity survives updates and loss of the original minimum key. On a split, the old ID stays with the component with greatest key overlap; ties use the smallest key. A merge keeps the smallest nominated ID. Other surviving split components get deterministic distinct IDs. Identity ownership persists even when a fire disappears. Applying mutations sequentially inside each batch makes different checkpoint boundaries produce the same final state/IDs.
- `Processor` stores raw detection state, identity history, offset and pending projection in `ingest_cursor` through `SaveCursorReplicated`. It saves this recovery plan on at least RF=2 units **before** canonical effects, materializes through `UpsertReplicated`, then clears pending effects durably. Restore finishes interrupted effects before streaming. Retries are idempotent; an old epoch is fenced.
- Withdrawn fires leave internal replicated tombstones. Hazard reads, Audit events/checksum/location and analytics exclude them, while replication/export retains them to prevent resurrection. Consumers accept M2 sources and journal withdrawals; notifications do not simulate a new alert for a withdrawal. Satellite dashboard ordering uses the checkpoint's monotonic ingestion timestamp, avoiding divergent replica revision numbers.
- Metrics include processed offset, offset/event-time lag, retained late detections, singletons, handler-to-commit latency and processor readiness. `ObserveSession` uses the feed's virtual clock. The Grafana Overview has corresponding panels; zero readiness means no recovered live processor and is not evidence of zero lag. `GET /internal/v1/status` reads persisted satellite progress on followers without opening another stream.

## Explicit decisions and limitations

Fithra confirmed D-FIRE: a leader-only module inside Aggregator. The processing and transport components are implemented, but **main does not yet start a live satellite worker**. The adapter between real SSE JSON and internal `Mutation` still requires an authenticated sample. `Mutation` is our internal contract and must not be presented as the assistant's wire format. This intentionally prevents unverified payload assumptions from advancing a real feed offset.

D-LATE is **provisional: 24 virtual/event-time hours**. Watermark = maximum observed detection time minus this allowance. The 360-minute clustering edge rule is separate. All over-limit detections remain in durable state and are counted once per detection identity. Late arrivals still recompute affected results, so the watermark does not cause silent loss. No historical state is evicted yet; completion/window-retention policy needs the observed workload and update/retract horizon.

Consumer messages still have a 1 MiB bound; exceptionally large detection-key lists need a reviewed transport strategy before that limit is reached. Full checkpoints are capped at **16 MiB**; exceeding it stops before advancing the offset. The current implementation copies/recomputes active state per mutation to make recovery/batching deterministic. This is a correctness baseline, not a proven 60x processor: Fithra/Sakti must measure real state/burst sizes and move to incremental replicated rows/indexes before the cap or CPU cost becomes limiting. The required <=60-second catch-up and p95 target are unmeasured.

Ingestion timestamps advance monotonically from the preceding checkpoint when host wall time moves backwards; satellite acquisition time remains unchanged. This prevents a newer update/tombstone from losing to an older replica during timestamp-based anti-entropy. Full cluster clock-skew/freeze acceptance remains separate.

The 38-province table is a **project reference**, derived from area-weighted planar WGS84 centroids of the licensed [province boundary dataset](https://github.com/ardian28/GeoJson-Indonesia-38-Provinsi/tree/486e89ca57c9f9910991dbf00afca26297b3baa3). The original data publisher attributes boundaries to BIG. The pinned input URL, SHA-256 and derivation method are in `provinces.json`; [the MIT notice is retained](data/province-centres-LICENSE.txt). These are approximate project centres, not official survey-grade centroid coordinates or a substitute for point-in-polygon classification.

## Observe the real source

The assigned source is `https://satellite.naufarrel.dev`. On 17 November, unauthenticated `/v1/session` returned HTTP 401 (`unauthorized`, `token tidak valid`). The service is reachable, but no group token is currently available locally. No authenticated feed behavior is claimed.

Export `SATELLITE_TOKEN` locally, then run `make p4-sample`. Stop any other group stream consumer first: only one connection per group is permitted. The recorder captures session JSON and raw SSE for 60 seconds (plus up to a five-second blocked-read timeout), preserving coordinate/acquisition-time tokens. It writes `session.json`, `stream.sse` and a credential-free summary under ignored `artifacts/m2/runs/`. Limits are 1 MiB per line and 64 MiB per sample; existing files are never overwritten. A failed capture exits nonzero and may retain partial files.

Inspect actual detection/update/retract shapes, identity keys, heartbeat interval, burst sizes, event-time delay, virtual time and offsets. Validate the adapter with recorded fixtures. Use `python3 scripts/m2/satellite-sample.py --seconds 120 --from-offset <offset>` for a second sample. Do not start an exam session or submit a video during development.

## Repeatable checks

`make p4-check` runs race-tested satellite/storage tests and Python recorder tests, recording output under `artifacts/m2/runs/`. Set `TEST_DATABASE_URL` to a disposable PostgreSQL first to enable database tests; without it, the script explicitly reports that database tests are skipped. Tests create isolated schemas and remove them afterwards. Never use this as a physical-node failure claim.

Coverage includes raw SSE preservation, reconnect, offset dedup/gaps, idle cancellation, failed commits, transitive clustering, confidence/severity thresholds, late retention, stable update/retract/merge/split identities and serialized restart/replay. The real-PostgreSQL recovery test interrupts processing after the pending recovery plan is durable, takes over with a new epoch, restores it, checks Audit checksum and verifies withdrawal/read/analytics behavior.

For the synthetic CPU baseline: `go test ./services/aggregator/internal/satellite -run '^$' -bench BenchmarkStateThousandDetections -benchtime=1x`. This excludes network and database costs and does not establish 60x acceptance. The current single-run, 1,000 geographically dispersed detection baseline on Apple M3 was 2.64 seconds CPU/handler time (`artifacts/m2/runs/satellite-1000-benchmark.log`); dense clusters and larger states can cost more. This is a local baseline, not a live feed measurement.

## Remaining work and owners

| Part | Next action | Owner |
|---|---|---|
| Actual wire adapter/live worker | Set group token, capture real payloads, implement/validate translation into Mutation, then wire StreamClient + Processor + session metrics into the leader term. Add the runtime Secret/config when the worker is enabled. | Fithra; assistants supply token |
| Production state/performance | Review the replicated checkpoint/tombstone contract; replace full-state copies if observed workload exceeds the 16 MiB/CPU budget. Measure dense bursts, not only distributed synthetic points. | Fithra (processor), Sakti (storage review) |
| Lifecycle assumptions | Confirm deterministic split/merge lineage and provisional lateness against observed behavior/reference expectations; finalize the state-retention/window-completion policy. | Fithra; assistants clarify ambiguous cases |
| Real acceptance | Reachable cluster/VM access and live token: run 60x, kill, reconnect and checksum comparison; capture p95, lag and <=60-second catch-up evidence. | Sakti (platform/access), Fithra (P4 execution) |
| Reference result | Run the full-stream comparison when assistant references are released. | Assistants (reference), Fithra (comparison) |

P4 is not complete until the live adapter, capacity and acceptance evidence are finished. These implementation notes are not a completed academic report.
