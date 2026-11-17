# P4: satellite integration handoff (Fithra)

The assigned source is `https://satellite.naufarrel.dev`. On 17 November, an unauthenticated `GET /v1/session` returned HTTP 401 with `error.code=unauthorized` and `message=token tidak valid`; the root path returned 404. The service is reachable. The group token is absent from this checkout's `.env` and process environment; therefore no authenticated feed behavior is claimed.

Fithra confirmed the proposed D-FIRE design: a leader-only module inside Aggregator. D-LATE remains open until the feed has been observed. This PR implements the independent clustering core and the recorder needed for that observation; it does not start a production SSE connection.

## Observe the source

Set `SATELLITE_TOKEN` locally, without committing or pasting it into command arguments. Run `make p4-sample`. This opens the session endpoint and **one** SSE connection for at most 60 seconds plus the five-second socket timeout. Stop any other group stream consumer first: the assistant permits only one connection per group.

The recorder writes `session.json`, byte-preserved `stream.sse` and `summary.json` under a timestamped `artifacts/m2/runs/` directory. These artifacts are ignored by Git. Limits are 1 MiB per line and 64 MiB per capture; existing files are never overwritten. A failed or timed-out capture may leave partial raw files and exits nonzero. Tokens are sent only in Authorization headers and excluded from logs/summaries.

Use `python3 scripts/m2/satellite-sample.py --seconds 120 --from-offset <offset>` for a second capture. Compare the first returned event ID against the requested offset to establish inclusive/exclusive resume semantics. Do not start the exam or submit a video during development.

Inspect the sample for detection/update/retract payloads, lexical coordinate/acquisition-time representation, heartbeat interval, burst size, event-time delay, virtual time and offsets. Record the observations before selecting the lateness allowance or writing the wire decoder. This recorder intentionally counts SSE event/id lines without guessing detection JSON fields.

## Offline clustering

`services/aggregator/internal/satellite.Cluster` accepts a current active detection snapshot. It validates coordinates, UTC acquisition date/time, confidence and FRP; excludes low confidence; deduplicates exact detection keys; joins points at <=1 km and <=360 minutes using transitive connected components. A spatial/time grid limits candidates; Cartesian cells handle longitude wrap and poles. Dense cells can still produce quadratic candidate comparisons; benchmark the observed burst distribution before claiming the 60x target.

Output includes deterministic snapshot IDs, exact raw detection keys, sorted satellites, highest confidence, FRP, centroid, earliest event time and severity. Singletons are counted. Input order does not change output. Conflicting duplicate keys are rejected: the caller must first apply ordered updates to its durable snapshot.

**Snapshot IDs are not a complete lifecycle policy.** Removing the smallest key changes a component's computed ID. Merge/split/retract handling must preserve existing HazardEvent identity through a durable identity/alias ledger. That is still required before wiring these results to Upsert. The core does not publish hazards, choose a lateness threshold, or drop late detections.

Validation: `go test -race ./services/aggregator/internal/satellite`; `python3 -m unittest discover -s tests -p test_satellite_sample.py -v`. Tests cover transitive cross-satellite clustering, replay determinism, exact raw keys, dedup, singles, low confidence, six-hour/distance boundaries, severity thresholds, invalid data, recorder authentication, byte preservation and overwrite protection.

## Blockers and next responsibilities

| Part | Next action | Owner |
|---|---|---|
| Authenticated observation | Obtain/set group token from assistants, run recorder, retain real sample. No token exists locally yet. | Fithra; assistants supply credentials |
| Wire decoder, heartbeat, resume | Determine actual payload shapes and inclusive/exclusive `from`; implement SSE client against captured fixtures and verify reconnect behavior. | Fithra |
| Durable processing | Provide replicated raw detections, ordered update/retract state, offset checkpoint, identity/alias ledger and atomic publication/checkpoint semantics, including removal of vanished hazards. Existing Store API only has Upsert and source cursors. | Sakti (store), Fithra (processing/interface) |
| Lateness and province mapping | Choose D-LATE from observed delay; add 38-province reference and final WILDFIRE mapping. Track/report over-limit detections. | Fithra |
| Worker lifecycle | Integrate after the P2 leadership PR is reviewed; all streaming workers must stop on term loss. | Fithra |
| 60x and crash evidence | Reachable cluster/VM access plus live token; run burst, kill, reconnect and checksum comparison, add lag metrics/p95 dashboard and runbook evidence. | Sakti (platform), Fithra (P4 tests/metrics) |
| Reference result | Compare the full stream against assistant reference output once released. | Assistants (reference), Fithra (comparison) |

P4.1-8 are not marked complete by these unit tests. This is an implementation handoff, not a completed academic report or fabricated live result.
