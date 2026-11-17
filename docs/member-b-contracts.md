# Member B integration contracts — IF4031 Milestone 1

**Status:** Updated 6 October 2026 after reviewing the official M1 specification supplied by the user. The user confirmed the current Aggregator contract as the agreed M1 interface. Stage 4 now wires identity verification, login/JWT, refresh/session rotation, Auth introspection, and the protected public hazard route. The user selected memory sessions, introspection without caching, and seven Media Summary fields (1A/2A/3A). Other open decisions are recorded below.

**Repository evidence:** `system-architecture.md` is now present in the checkout. It establishes the service boundaries but leaves several detailed choices open. `context.md` and current code give newer, more specific direction for some older `TBD` entries, while remaining deployment/resilience decisions below are recorded separately. This document records observed behavior, minimum stated requirements, and proposals labeled for review.

**Boundaries:** Client API reads canonical hazards from Aggregator over HTTP. Only Aggregator owns the Canonical Store. Auth, Client API, and Aggregator remain separate services; no service imports another service's business logic. BMKG and PVMBG credentials are separate from downstream client credentials.

The intended request paths are:

```text
Client -> Auth Service         obtain or refresh credentials
Client -> Client API           request authorized hazard data
Client API -> Aggregator       read canonical hazards over HTTP (agreed M1 route below)
```

Auth is a separate token service, **not a reverse proxy** for Client API. Client API calls Auth introspection for every protected request without caching, following the approved M1 decision.

### Architecture cross-check

| Topic | `system-architecture.md` | Newer context and current code | Stage 1 treatment |
| --- | --- | --- | --- |
| Synchronous protocol | Internal protocol is `TBD` (`§12.1`, OD-03). | `context.md` names HTTP/REST as the intended synchronous path; Aggregator currently exposes `net/http` routes. | Use HTTP for the provisional Client API-to-Aggregator contract; do not claim every future internal protocol is frozen. |
| Canonical Store | Technology is `TBD` (`§7.4`, OD-04). | PostgreSQL + JSONB, migrations, idempotent upserts, and atomic outbox are implemented. | Follow current context/code; only Aggregator owns the canonical database. Auth/Client API must not join the storage network. |
| Message broker | Technology is `TBD` (OD-05). | RabbitMQ, an outbox publisher, and independent consumers are implemented. | Preserve C's event infrastructure; Client API still reads hazards through HTTP. |
| Logging | Technology is to be selected (`§18`). | `context.md` favors `log/slog`; Aggregator currently uses it. | Match existing structured-log conventions provisionally. |
| Token validation and internal API | Local verification versus Auth introspection and Aggregator API contract are open (OD-06, OD-08). | The user confirmed the current Aggregator HTTP contract for M1 on 5 October. Token handling is implemented in Stage 4. | The Aggregator read contract is agreed; Auth introspection is approved; Aggregator internal-route protection remains open. |

The architecture document does not settle the Media summary allowlist, explicit raw-request syntax, stale-data response, or Auth port. The official specification now supplies the canonical field classifications; public syntax, stale-data responses, and the final Auth port still require the decisions recorded below.

## 1. Current Aggregator interface for Client API

**Agreed M1 integration route:** `GET /internal/v1/hazards` at `http://aggregator:8083`. It aliases `GET /hazards`; neither route currently authenticates callers. `GET /health` is also available. Route protection and final network exposure remain open; agreement on the read contract does not select an authentication mechanism.

| Input | Current behavior |
| --- | --- |
| `source` | Optional `BMKG` or `PVMBG`; values are normalized to uppercase. |
| `hazard_type` | Optional `SEISMIC` or `VOLCANIC`; values are normalized to uppercase. |
| `since` | Optional RFC3339/RFC3339Nano timestamp; filters on `occurred_at`. |
| `limit` | Optional integer 1–1000; default 100. |
| `X-Correlation-ID` | Optional request header; Aggregator returns and logs the supplied value, or generates one when missing or longer than 128 characters. |

Current success shape for both hazard-list routes:

```json
{
  "data": [
    {
      "hazard_id": "string",
      "source": "BMKG or PVMBG",
      "source_ref_id": "string",
      "hazard_type": "SEISMIC or VOLCANIC",
      "severity": "NORMAL, WASPADA, SIAGA, or AWAS",
      "area_name": "string",
      "latitude": 0.0,
      "longitude": 0.0,
      "occurred_at": "RFC3339 timestamp",
      "ingested_at": "RFC3339 timestamp",
      "attributes": {}
    }
  ],
  "count": 1,
  "sources": {
    "BMKG": {
      "available": true,
      "last_success_at": "RFC3339 timestamp",
      "last_ingested_at": "RFC3339 timestamp",
      "stale": false,
      "stale_after_seconds": 15
    },
    "PVMBG": {
      "available": false,
      "last_success_at": "RFC3339 timestamp",
      "last_ingested_at": "RFC3339 timestamp",
      "last_error": "string",
      "stale": true,
      "stale_since": "RFC3339 timestamp",
      "stale_after_seconds": 15
    }
  }
}
```

`data` is ordered by descending `occurred_at`; `count` is the number of returned rows after `limit`. `attributes` may gain additive source-specific fields. There is no pagination parameter. Unknown query keys are currently ignored; stricter rejection would require a future agreed change.

| Source metadata | Agreed current meaning |
| --- | --- |
| `available`, `last_success_at`, `last_error` | Upstream fetch state; fetch success is not proof of a database commit. Optional timestamps/errors are omitted when unset. |
| `last_ingested_at` | Last complete successfully normalized and committed ingestion cycle, including an empty poll that successfully checks storage. |
| `ingestion_error` | Outstanding storage, mapping, or correlation error; omitted after complete recovery. |
| `stale`, `stale_since` | Initially stale, stale on failure, or stale when the last complete ingestion passes the freshness deadline. `stale_since` is omitted after recovery. |
| `stale_after_seconds` | Maximum of 15 seconds and three polling intervals. This is pipeline freshness, not record age or broker delivery. |

Invalid known filters return HTTP 400 with plain-text `http.Error` output. A repository query failure returns HTTP 503 with `canonical store unavailable`. An upstream outage can still return HTTP 200 with PostgreSQL's last stored data and stale source metadata. A never-ingested source is stale with no `last_ingested_at`; a source-filtered query can return an empty array. The final public Client API treatment of that case remains open.

`GET /health` returns `storage.available` plus `sources`. Database failure produces HTTP 503 and `status=degraded`; upstream outage alone can still produce HTTP 200 with `status=ok` and stale source status. Source metadata does not prove broker/consumer readiness.

Source references: `services/aggregator/internal/httpapi/server.go`, `services/aggregator/internal/domain/hazard.go`, `services/aggregator/internal/ingest/status.go`, and `services/aggregator/internal/store/postgres.go`.

## 2. Downstream field and authorization contract

Required identities/scopes are `media`, `field-team`, and `internal-ops`. Client API must enforce authorization and project the response server-side. The official specification (pages 11–12 and 14–15) classifies `source_ref_id`, `latitude`, `longitude`, and `attributes` as raw fields; Media must never receive them, including fields nested in `attributes`. An explicit Media request for raw fields must be denied with 401 or 403; the approved syntax below is enforced by the protected public endpoint.

**Approved by the user on 6 October 2026 (Stage 4 decision 3A), aligned with the official specification:** Media receives `hazard_id`, `source`, `hazard_type`, `severity`, `area_name`, `occurred_at`, and `ingested_at`. Field Team and Internal Ops may receive all eleven canonical fields. Preserve `count`; the Stage 5 decision 1A adds the safe public `sources` projection described below.

The agreed public route is `GET /hazards`. An explicit Media request with `include_raw=true`, or `fields` containing a known field outside the Media allowlist, returns 403. Unknown requested fields return 400; missing/invalid access tokens return 401. The route, Auth middleware, and field projection are implemented and covered by HTTP/unit tests. Authentication precedes authorization; explicit field-denial checks precede Aggregator calls. Runtime evidence is recorded separately below.

## 3. Auth and token interface to define

Auth Service exposes `GET /health`, `POST /login`, `POST /refresh`, and `POST /internal/v1/introspect`; identity verification, JWT, and memory sessions are wired at startup. JWT signing uses HS256 with a secret of at least 32 bytes from environment, fixed issuer/audience, algorithm checks, mandatory expiry, and required claims. The default access TTL remains 60 seconds. Login/refresh/introspection are covered by HTTP tests using the actual Auth identity/token/session components.

| Claim | Intended meaning | Status |
| --- | --- | --- |
| `sub` | Downstream client identity | Issuer/parser component implemented |
| `scope` | One of `media`, `field-team`, `internal-ops` | Issuer/parser component implemented |
| `iat`, `exp` | Issue and expiry times | Configurable TTL and expiry boundary tested |
| `jti` | Random access-token identifier | Issuer component implemented |
| `sid` | Session identifier | Created at login and validated against Auth-owned memory state |
| `generation` | Current session version | Incremented atomically on refresh and checked by introspection |

### B-09/B-10 approved M1 strategy — user decision 1A/2A

The handover recommends an Auth-owned session containing `session_id`, `client_id`, `scope`, `generation`, `refresh_token_hash`, `refresh_expiry`, and `revoked`. The memory session store, generation claim, refresh endpoint, and introspection call are now implemented. The user approved this strategy after the mechanism and specification were explained.

Implemented atomic refresh:

1. Prepare new random access/refresh material; store only a SHA-256 hash of the high-entropy refresh token.
2. Under one store lock/transaction, verify that the presented refresh token is the current token, the session is not revoked, and refresh expiry has not passed.
3. Increment the generation and replace the refresh hash as one state transition; retain the fixed session expiry from the first login; expose the new token pair only after the transition succeeds. Token preparation/signing failure must not consume the old refresh token.
4. Introspection checks JWT signature/expiry and compares its `sid`/generation with current Auth state. A validation performed after the refresh transition rejects the old access token. An already-authorized in-flight request is not retroactively canceled.
5. Two refresh calls using the same old token cannot both succeed. The losing call observes a replaced hash and is rejected.

The approved M1 validation path is Client API → Auth introspection on every request, without caching. It keeps the JWT signing key in Auth and provides immediate validation against current session state, at the cost of one HTTP round trip and dependence on Auth availability. The implementation returns a controlled 503 when Auth cannot be reached, rather than authorizing an unchecked token. `AUTH_BASE_URL`, `AUTH_REQUEST_TIMEOUT_MS`, correlation ID, and `AUTH_INTERNAL_SECRET` are wired. The internal secret is at least 32 bytes, separate from the JWT signing key, and shared only between Auth and Client API. Introspection uses `X-Auth-Service-Key` plus JSON `token`; invalid access returns HTTP 200 with `active=false` (Client API -> 401), while an invalid internal credential returns 401 (Client API -> 503). Active responses contain only `active`, `client_id`, `scope`, and `session_id`. No redirects are followed by the Auth client.

The official specification (pages 14–15 and 20–21) requires distinct downstream identities/credentials, server-side scopes, configurable access TTL defaulting to 60 seconds, natural Field Team token expiry, refresh without manual login, and rejection of the old token after refresh. It does not explicitly require Auth sessions/refresh state to survive an Auth restart or prescribe a session store, generation counter, or introspection protocol. P4 requires independent service restart and service-owned storage; it does not add an explicit Auth-session survival criterion.

B-09 permits an in-memory PoC and labels persistent session storage `TEAM DECISION REQUIRED`; B-07 says a persistent identity DB is not required for M1. The user selected an Auth-owned memory store for M1 (1A): Auth restart invalidates existing sessions and clients must log in again, a limitation to document. Persistent state, if selected, must be owned by Auth and separate from the canonical database.

The official P3 demo refreshes after natural access-token expiry. Generation-based rejection of an old access token that is still unexpired, refresh-token rotation/replay checks, and atomic concurrency handling are additional mechanisms required by the revised B handover or proposed to implement it safely; the official specification does not mandate these particular mechanisms. A generation check alone does not replace the natural-expiry demo.

## 4. Correlation and dependency calls

The observed header is `X-Correlation-ID`. Aggregator currently accepts or generates it, includes it in the response, and uses it in structured request logs. The working proposal is for Client API to generate an ID at ingress when absent, propagate the same ID to Aggregator and any Auth validation call, return it to the caller, and log outbound latency. Auth should do the same for its own incoming requests. The final ID format, trust/length rules, and propagation convention remain a **team decision**.

Client API has a bounded HTTP timeout, request-context cancellation, safe forwarding of the four supported filters, and outbound latency logging. The approved checkpoint timeout is `AGGREGATOR_REQUEST_TIMEOUT_MS=5000`; the final value awaits P2 measurements. User decisions 2A/3A approved the Client API hazard error format/mapping and configurable concurrency admission limit described below.

The opt-in legacy checkpoint alias `GET /internal/provisional/hazards` is still disabled by default. It uses the same authentication, field projection, JSON errors, and shared admission limit as `GET /hazards`, including when explicitly enabled. It returns `data/count` rather than the raw Aggregator envelope. The foundation checker logs in as Internal Ops and checks freshness directly at Aggregator. Historical Stage 2–3 evidence below used the earlier unauthenticated route; that bypass has been removed. Public freshness mapping remains undecided.

### Stage 1–3 checkpoint decisions — 5 October 2026

- The user confirmed the current Aggregator contract for M1.
- **NOT FINAL:** `AUTH_PORT=8084` / `http://auth:8084` is approved for the checkpoint; the final port awaits Member C.
- **NOT FINAL:** `AGGREGATOR_REQUEST_TIMEOUT_MS=5000` is approved for the checkpoint; review the final value during P2 load testing.
- `tests/compose-member-b.yml` supplements C's base Compose for local B verification. It does not grant Auth or Client API canonical database credentials or membership in the storage network.
- At the Stage 1–3 checkpoint, login/JWT, refresh/session validation, Media projection, public stale/error semantics, and concurrency controls were deferred. Stage 4/5 implementation and verification are recorded below.

### Verification — 5 October 2026 (historical Stage 2–3 checkpoint)

Go tests and Linux builds passed for both Member B modules. Both Docker images built successfully, passed binary/HTTP health checks, and stopped/restarted successfully in the isolated `member-b-stage123` project. `scripts/check-member-b.py` returned PASS with five BMKG and five PVMBG records, canonical response comparison, all four filters, committed freshness metadata, and correlation/latency logs in Client API and Aggregator. Auth token handling was explicitly not claimed.

RabbitMQ's startup script was checked out with CRLF on Windows and exited with `set: illegal option -`. No broker source or Git attributes were changed. For this HTTP checkpoint only, Aggregator was started with `--no-deps` against the healthy PostgreSQL service; its outbox retained events while the broker was unavailable. This verifies the Stage 3 read path, not a healthy nine-service stack or consumer delivery. The proposed LF portability fix awaits the user's decision. The test project was stopped after verification without deleting volumes.

## 5. Decision and dependency register

### Stage 5 started — 6 October 2026

Client API now has an internal typed envelope/status decoder. It validates `data/count`, required metadata for both agreed sources, and RFC3339 timestamps, while accepting additive fields and preserving the raw internal response. Fetch reachability and completed ingestion remain separate. Aggregator redirects are rejected. Auth login/refresh request logs now include verified identity/scope without changing the four-field token response; Auth-client failures have safe diagnostic categories and WARN level. Unit/HTTP/race tests passed for both Member B modules.

Source metadata belongs to the current Aggregator ingestion manager: `NewManager` initializes both `last_ingested_at` values as absent even when PostgreSQL already contains hazards. An absent ingestion timestamp therefore does not prove a source has never had persisted data. Empty query results can also be caused by filters. Client API must not infer canonical data absence from either observation or inspect PostgreSQL to resolve it. This is a contract constraint verified from `services/aggregator/internal/ingest/manager.go` and `status.go`, not a change to Member A code.

The user approved Stage 5 option 1A after the successful-read behavior was explained. Public responses now contain `data/count/sources`. Timeout remains the previously approved temporary 5000 ms; its final P2 value is still open.

### Stage 5 decision 1A — approved and implemented

- A successful Aggregator stored-data read remains HTTP 200, including cached data during upstream outage, empty filtered results, or absent ingestion metadata. Aggregator storage failure -> 503 under the already approved error contract. Client API never inspects PostgreSQL to infer source history.
- `sources` always includes BMKG and PVMBG, even for a single-source query. Each entry contains exactly `available`, `last_ingested_at`, `stale`, `stale_since`, and `stale_after_seconds`. Missing timestamps serialize as explicit JSON nulls. Source flags/freshness are copied from Aggregator, not inferred from result count or hazard age.
- All three scopes receive the same safe metadata. `last_error`, `ingestion_error`, `last_success_at`, unknown source metadata, and future raw hazard fields are omitted. Media hazard rows remain limited to the seven approved summary fields; other roles retain eleven canonical fields. The opt-in legacy alias uses the same projection and controls.
- Unit/HTTP/race tests cover fresh data, cached outage data, empty filters, metadata reset while stored rows exist, no observed ingestion, three scopes, diagnostic redaction, and explicit null timestamps. The updated Auth checkpoint validates the new envelope for all roles.

### Stage 5 decisions 2A/3A — approved and implemented

- Handler-generated hazard errors use JSON `{"error":{"code":"...","message":"..."},"correlation_id":"..."}` with fixed safe messages. Invalid token -> 401, denied fields -> 403, invalid query/field selection or Aggregator 400 -> 400, Auth failure/Aggregator 503 -> 503, Aggregator timeout -> 504, other transport/status errors or malformed responses -> 502. Auth's own endpoint error format is unchanged.
- `CLIENT_API_MAX_CONCURRENT_REQUESTS` defaults to 64 and must be a positive integer. One semaphore covers public and opt-in legacy hazard routes before Auth introspection, through the Aggregator call/response. Full capacity -> immediate JSON 429 with `Retry-After: 1`; no internal waiting queue. Healthchecks bypass this limit. This is concurrency admission, not a requests-per-second rate limit.
- Request cancellation reaches dependency calls. The handler avoids writing an error after context cancellation; permits are released with `defer`. A canceled request with no written response logs status 0 and `error_kind=canceled`, instead of claiming a successful response. 429/error codes appear in request logs.
- Client API unit/HTTP/race tests passed, including 64 admitted requests with Auth or Aggregator blocked, overflow rejected before dependencies, shared legacy/public capacity, healthcheck availability, cancellation/replacement, and repeated failures without permit leaks. This is local test evidence, not P2 sustained-load or Docker evidence.

| Decision still open | Owner / dependency | Current observation or minimum constraint |
| --- | --- | --- |
| Future changes to the agreed Aggregator read contract | Member A with Member B | Current route, envelope, filters, freshness metadata, and storage 503 are agreed for M1; changing them requires coordination. |
| Protection of Aggregator internal route and Client API-to-Aggregator authentication | Member A and team; Member C for network exposure | Route currently has no auth and port 8083 is published to the host. |
| Final Auth port and deployment wiring | Member C with Member B | Port 8084 is approved temporarily; a B checkpoint overlay supplies environment/health checks. |
| Final correlation-ID convention | Member B and team | Existing Aggregator uses `X-Correlation-ID`. |
| Final P2 tuning of dependency timeouts and concurrency bound | Member B and team, with Member C for load evidence | Temporary dependency timeout is 5000 ms; initial configurable bound 64 and immediate 429 policy are approved/implemented. |

The existing Member B skeletons use `net/http`, matching current A/C services. No router replacement is required for Stage 1–3.

Official source reviewed: `C:/Users/ASUS/Downloads/IF4031 - Tugas Besar - M1.pdf` (29 pages), supplied by the user on 6 October 2026. It remains outside the checkout; review does not copy or commit the PDF or settle implementation choices that the specification leaves open.

### Stage 4 verification — 6 October 2026

`go test -race -timeout 90s ./services/auth/... ./services/client-api/...` passed. This includes actual Auth HTTP login/refresh/introspection, three role projections, immediate old-access rejection, refresh replay, 24 concurrent refreshes with exactly one winner, revocation, fixed refresh-session expiry, restart invalidation, signing failure without token consumption, Auth outage/timeout/cancellation/redirect handling, correlation IDs, and checks that logs omit secrets/tokens. `scripts/check-member-b-auth.py` provides an HTTP checkpoint, with optional natural expiry. Both new Docker images built and passed healthchecks. The real Auth -> Client API -> Aggregator -> PostgreSQL checkpoint passed for both sources and all roles, including natural access expiry (~59.828s remaining from a configured 60s TTL), refresh without login, and old-token rejection. Auth outage returned controlled 503 (~3968ms); Client API/Aggregator health stayed 200. After Auth restart, old access and refresh tokens returned 401 and a new login worked. Correlation traces and outbound latency were verified across services, and B logs contained no tested credentials/tokens. Evidence: `C:/Users/ASUS/AppData/Local/Temp/member-b-stage4-evidence.json` and `C:/Users/ASUS/AppData/Local/Temp/member-b-stage4-resilience-evidence.json`. The checkpoint used six services in the isolated project with Aggregator started using `--no-deps`; broker/consumers were not exercised because the pending CRLF portability fix was not applied. This is not nine-service/fanout evidence. The test project was stopped without deleting volumes. No P2 load or complete team P3 acceptance claim is made.

The final alias security audit removed the historical bypass: even when `ENABLE_PROVISIONAL_HAZARD_ENDPOINT=true`, the legacy route requires Auth and the same scope projection. Client API race tests passed again, the foundation checker now logs in as Internal Ops, and a real Media alias check returned 401 without a token, 403 for raw requests, and seven permitted fields. Evidence: `C:/Users/ASUS/AppData/Local/Temp/member-b-protected-legacy-evidence.json` and `C:/Users/ASUS/AppData/Local/Temp/member-b-stage4-final-http-evidence.json`. Final Client API Docker rebuild and healthcheck passed. The isolated project was stopped again with volumes preserved.

### Stage 5 verification — 6 October 2026

Both updated Docker images built and passed healthchecks in the isolated six-service `member-b-stage123` project. The Auth regression checker passed all three roles, seven/eleven hazard fields, safe sources metadata, raw denial, rotation/replay, and old-access rejection; natural expiry was not repeated because JWT/TTL behavior is unchanged and Stage 4 already verified it. Evidence: `C:/Users/ASUS/AppData/Local/Temp/member-b-stage5-auth-regression-evidence.json`.

`scripts/check-member-b-resilience.py` passed against real Auth, Client API, Aggregator, PostgreSQL, and mock sources. It verified empty-filter 200, invalid-filter JSON 400, Media raw JSON 403, PVMBG outage with last-known data 200/explicit stale metadata, BMKG independence, and source recovery without restart. PostgreSQL and Auth stop/start returned controlled JSON 503 while Client health remained 200, then reads recovered. A short 128-connection burst returned 71 HTTP 200 and 57 controlled HTTP 429, with no other responses. Correlation/latency across three services, Auth verified login/refresh identity, outbound logs, 429 logs, and absence of tested B secrets/tokens were verified. Evidence: `C:/Users/ASUS/AppData/Local/Temp/member-b-stage5-evidence.json`.

This checkpoint does not establish sustained P2 load >=50 connections for >=60 seconds, the slow-PVMBG BMKG p95 target, or full nine-service/fanout acceptance. Broker/consumer source files were not changed or exercised. Port 8084 and dependency timeout 5000 ms remain approved temporary values; final deployment/tuning remains with the team.

After verification, the isolated project's six services were stopped with all volumes preserved. No commits or pushes were made; the local context/architecture/handover/contracts/tracker documents remain untracked.
