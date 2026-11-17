# Codex Handover — Member B (API, Auth & Resilience)
## IF4031 Milestone 1 — Revised Execution Order

> **Role:** Member B — API, Auth & Resilience  
> **Primary ownership:** Client-Facing API, Auth / Token Service, downstream scopes & authorization, access/refresh flow, request-side resilience, graceful-degradation response behavior.  
> **Shared ownership:** P2 with Member A and Member C.  
> **Important execution principle:** work is **parallel within each stage**, with explicit integration checkpoints. Do not treat Member A, B, and C as three isolated tracks that only meet at the end.

---

# 0. Global Rules for Codex

Before modifying code:

1. Read:
   - `context.md`
   - `system-architecture.md`
   - `README.md`
   - `docker-compose.yml`
   - `.env.example`
   - relevant service code.
2. Preserve the source-of-truth order already agreed by the team:
   - official assignment;
   - `system-architecture.md`;
   - `context.md`;
   - implementation convenience.
3. If `system-architecture.md` contains an older `TBD` that has already been frozen in `context.md` or current repository code, follow the newer agreed implementation direction and note the mismatch.
4. Do not silently invent unresolved architecture decisions.
5. Preserve service boundaries:
   - Client API must never access PostgreSQL directly;
   - Auth must remain separate from Client API;
   - Client API must call Aggregator over the network;
   - no shared business-logic package may bypass a service boundary.
6. Do not modify Member A/C-owned code unless an explicitly agreed interface requires it.
7. Do not hardcode secrets.
8. Use environment variables and update `.env.example` only with safe sample values.
9. Use structured logging and `X-Correlation-ID`.
10. Use request timeouts and `context.Context`.
11. Prefer simple, explainable M1-compatible designs over production-scale complexity.
12. For every task:
   - inspect first;
   - state prerequisite status;
   - state assumptions;
   - list files to modify;
   - implement;
   - add tests;
   - report blockers.

---

# 1. Current Known Repository State

## Aggregator already available

Current relevant endpoints:

```text
GET /health
GET /hazards
GET /internal/v1/hazards
```

Current hazard response contains canonical fields:

```text
hazard_id
source
source_ref_id
hazard_type
severity
area_name
latitude
longitude
occurred_at
ingested_at
attributes
```

Current list response is approximately:

```json
{
  "data": [],
  "count": 0,
  "sources": {
    "BMKG": {
      "available": true,
      "last_success_at": "..."
    },
    "PVMBG": {
      "available": false,
      "last_success_at": "...",
      "last_error": "..."
    }
  }
}
```

## Current infrastructure direction

Already intended/frozen in current project context:

```text
Backend              Go 1.27.1
Sync communication   HTTP/REST
Canonical Store      PostgreSQL + JSONB
Broker               RabbitMQ
Authentication       JWT access token + refresh token
Password hashing     Argon2id
Containerization     Docker + Docker Compose
Logging              log/slog
Load testing         k6
```

## Auth Service

Current state:

```text
scaffold only
main.go effectively empty
```

Missing:

```text
identities
credential verification
Argon2id
JWT
login
refresh
refresh state
revocation / generation state
old-token invalidation
health endpoint
```

## Client-Facing API

Current state:

```text
scaffold only
main.go effectively empty
```

Missing:

```text
server
health
token validation
scope enforcement
field-level authorization
Aggregator HTTP client
correlation ID
structured logging
timeouts
controlled dependency errors
concurrency controls
```

---

# 2. Execution Strategy

The revised execution strategy is:

```text
STAGE 1
Agree contracts
      |
      v
STAGE 2
Build service skeletons in parallel
      |
      +-------------------+
      |                   |
      v                   v
Auth skeleton       Client API skeleton
      |                   |
      +-------------------+
              |
              v
STAGE 3
Prove Client API -> Aggregator data flow
              |
              v
STAGE 4
Layer advanced Auth + authorization + resilience
              |
              v
STAGE 5
Team integration and P2/P3 verification
```

Important:

```text
Coding may proceed in parallel,
but integration checkpoints must happen in order.
```

Do not wait for Auth to be “perfect” before proving that:

```text
Client -> Client API -> Aggregator
```

actually works.

---

# 3. Prerequisite Status Labels

Use these labels before every task.

- **READY**
- **READY WITH PROVISIONAL CONTRACT**
- **BLOCKED BY MEMBER A**
- **BLOCKED BY MEMBER C**
- **TEAM DECISION REQUIRED**

---

# STAGE 1 — Shared Contract Agreement

# B-01 — Freeze Member B provisional contracts

## Goal

Write down the contract Member B needs before implementation.

## Prerequisites

```text
Current Aggregator endpoint exists             READY
Current HazardEvent shape exists               READY
Exact final internal Aggregator API             BLOCKED BY MEMBER A
Exact stale/unavailable semantics               BLOCKED BY MEMBER A
Exact Auth port/networking                      BLOCKED BY MEMBER C
Token validation strategy                       TEAM DECISION REQUIRED
Exact raw-request syntax                        TEAM DECISION REQUIRED
```

## Required work

Create/update:

```text
docs/member-b-contracts.md
```

Document:

### Aggregator dependency

Provisional:

```text
GET /internal/v1/hazards
```

Current supported filters:

```text
source
hazard_type
since
limit
```

Current response:

```text
data
count
sources
```

### Media access baseline

At minimum, Media must not receive:

```text
latitude
longitude
attributes
```

Do not silently finalize the rest of the summary/raw classification if the team has not agreed it.

### Auth boundary

Record:

```text
Client -> Auth Service
Client -> Client API
Client API -> Aggregator
```

Auth is not a reverse proxy for Client API.

### Open decisions

Track owner for:

```text
Aggregator final internal contract          Member A
stale/unavailable public response           Member A + B
Auth port/internal URL                      Member C
token validation strategy                   Member B/team
raw-field request syntax                    Member B/team
concurrency limit / 429 policy              Member B/team
```

## Acceptance criteria

- no code implementation required yet;
- contract is explicit;
- all unresolved items are labeled;
- no hidden assumptions.

---

# STAGE 2 — Early Parallel Work

This stage deliberately starts Auth and Client API **at the same time**.

---

# B-02 — Auth service skeleton

## Goal

Turn `services/auth` into a real running service.

## Prerequisites

```text
Auth ownership                                  READY
Auth code currently empty                       READY
Router/framework                                TEAM DECISION REQUIRED
Final Auth port                                 BLOCKED BY MEMBER C
Temporary configurable port                     READY
```

## Required implementation

Suggested structure:

```text
services/auth/
├── cmd/server/main.go
└── internal/
    ├── config/
    ├── httpapi/
    ├── identity/
    ├── token/
    ├── session/
    └── observability/
```

Keep it minimal.

## Required configuration

At minimum:

```text
AUTH_PORT
JWT_SIGNING_SECRET
ACCESS_TOKEN_TTL_SECONDS
REFRESH_TOKEN_TTL_SECONDS

MEDIA_CLIENT_ID
MEDIA_CLIENT_PASSWORD

FIELD_TEAM_CLIENT_ID
FIELD_TEAM_CLIENT_PASSWORD

INTERNAL_OPS_CLIENT_ID
INTERNAL_OPS_CLIENT_PASSWORD
```

## Required endpoint

```text
GET /health
```

## Required behavior

- service stays running;
- configuration validation;
- graceful shutdown;
- structured logs;
- server timeouts;
- secrets externalized.

## Tests

- health endpoint;
- config success;
- config failure for missing critical secret.

---

# B-03 — Client API service skeleton

## Goal

Build Client API skeleton in parallel with Auth.

## Prerequisites

```text
Client API ownership                            READY
Port 8080 expected                              READY
Aggregator current URL/port                     READY WITH PROVISIONAL CONTRACT
Auth integration                                NOT REQUIRED YET
```

## Required configuration

At minimum:

```text
CLIENT_API_PORT=8080
AGGREGATOR_BASE_URL=http://aggregator:8083
AGGREGATOR_REQUEST_TIMEOUT_MS=...
```

Auth URL can be added later when the Auth integration strategy is frozen.

## Required endpoints

```text
GET /health
```

Optionally prepare a provisional hazard endpoint backed by fixture/mock data, but do not claim final integration yet.

## Required middleware baseline

Implement:

```text
X-Correlation-ID
```

Rules:

```text
if incoming ID exists -> reuse
if missing -> generate
include in logs
prepare to forward on outbound calls
```

## Tests

- health;
- generated correlation ID;
- preserved incoming correlation ID.

---

# B-04 — Make Auth and Client API Compose-ready early

## Goal

Do not postpone Docker/Compose readiness until the end.

## Prerequisites

```text
Service skeletons                               B-02 / B-03
Final Compose ownership                         MEMBER C
Final service names                             MEMBER C
```

## Required Member B responsibility

For both services ensure:

```text
Dockerfile builds
port configurable
/health exists
runtime config via env
service does not exit immediately
```

Do not take over Member C’s Compose ownership.

Member B should ensure each owned service is **ready to be wired** into Compose.

## Acceptance criteria

Each service can be independently:

```text
build
run
health-check
stop
```

even before full-stack integration.

---

# STAGE 3 — First Integration Checkpoint

The purpose of this stage is to prove the main data path **before advanced Auth is layered on top**.

---

# B-05 — Aggregator HTTP adapter

## Goal

Implement:

```text
Client API -> Aggregator API
```

as early as possible.

## Prerequisites

```text
Client API skeleton                              READY
Aggregator endpoint exists                       READY
Exact final contract                             BLOCKED BY MEMBER A
Current endpoint usable                          READY WITH PROVISIONAL CONTRACT
```

## Provisional endpoint

Use:

```text
GET /internal/v1/hazards
```

until Member A freezes the final contract.

## Required implementation

Create a small internal HTTP adapter responsible for:

```text
request construction
query forwarding
context propagation
correlation-ID propagation
timeout
response decoding
latency measurement
controlled error mapping
```

## Do not

```text
import Aggregator internal packages
query PostgreSQL
duplicate canonicalization logic
```

## Tests with httptest

- success;
- query forwarding;
- correlation ID forwarding;
- timeout;
- malformed response;
- 4xx;
- 5xx;
- canceled request.

---

# B-06 — Provisional Client API hazard endpoint

## Goal

Prove this flow first:

```text
Client
  |
  v
Client API
  |
  v
Aggregator
  |
  v
Client API response
```

Authentication may still be temporarily bypassed or fixture-controlled in this integration stage **only if clearly marked as provisional**.

## Prerequisites

```text
Aggregator adapter                              B-05
Final auth middleware                           NOT REQUIRED YET
Final authorization                             NOT REQUIRED YET
```

## Required behavior

- read-only hazard retrieval;
- safe filter forwarding;
- no DB access;
- no Aggregator business-logic imports.

## Acceptance criteria

A real Aggregator response can be retrieved through Client API.

This checkpoint must be completed before treating advanced Auth as complete integration.

---

# STAGE 4 — Advanced Auth and Authorization

Now layer authentication and authorization onto a data path that is already known to work.

---

# B-07 — Downstream identities + Argon2id

## Goal

Implement three distinct identities:

```text
media
field-team
internal-ops
```

## Prerequisites

```text
Identity names                                   READY
Argon2id chosen                                  READY
Persistent identity DB                           NOT REQUIRED FOR M1
```

## Requirements

Credential validation must be isolated from HTTP handlers.

Conceptual model:

```go
type ClientIdentity struct {
    ID           string
    PasswordHash string
    Scope        string
}
```

Passwords must be verified using Argon2id.

## Tests

- valid Media login credentials;
- wrong password rejected;
- identities remain distinct;
- unknown identity rejected.

---

# B-08 — JWT access token issuance

## Goal

Implement short-lived access tokens.

## Prerequisites

```text
Identity verification                            B-07
JWT strategy                                     READY
Default TTL 60 seconds                           READY
```

## Required claims

At minimum:

```text
sub
scope
iat
exp
jti
sid
```

## Endpoint

```text
POST /login
```

## Required response

```json
{
  "access_token": "...",
  "token_type": "Bearer",
  "expires_in": 60,
  "refresh_token": "..."
}
```

Refresh mechanics may be completed in B-09.

## Tests

- valid login;
- invalid login;
- expected claims;
- correct expiry;
- no hardcoded signing key.

---

# B-09 — Refresh-token rotation and session state

## Goal

Implement refresh flow that satisfies:

```text
refresh succeeds without login
old access token becomes invalid
old refresh token cannot be replayed
```

## Prerequisites

```text
JWT issuance                                     B-08
Old-access-token invalidation requirement        READY
Persistent session store                         TEAM DECISION REQUIRED
In-memory PoC state                              READY
```

## Required design step

Before coding, Codex must explain the selected invalidation mechanism.

Recommended simple M1 model:

```text
Session
├── session_id
├── client_id
├── scope
├── generation
├── refresh_token_hash
├── refresh_expiry
└── revoked
```

Access token includes:

```text
sid
session generation/version
```

Refresh:

```text
generation 1
     |
     v
rotate refresh
     |
     v
generation 2
```

Old access token:

```text
token generation 1
!=
current session generation 2

=> reject
```

## Endpoint

```text
POST /refresh
```

## Tests

- refresh succeeds;
- access token changes;
- refresh token changes;
- old refresh replay fails;
- old access token invalid;
- new access token valid;
- expiry handled;
- revoked session cannot refresh.

---

# B-10 — Freeze access-token validation strategy

## Goal

Choose how Client API checks whether an access token is still valid.

## Prerequisites

```text
Auth token/session implementation               B-08/B-09
Architecture decision                           TEAM DECISION REQUIRED
```

## Explicit options already identified by architecture

### Option A — Local signed-token verification

```text
Client API validates JWT locally
```

But this alone does not automatically satisfy immediate old-token invalidation after refresh.

### Option B — Auth introspection

```text
Client API
   |
   | token
   v
Auth Service
   |
   v
valid/invalid + claims
```

### Hybrid alternative

Local JWT verification plus Auth/session-state check.

## Codex must first report

- chosen strategy;
- trade-offs;
- how old access tokens are rejected;
- effect of Auth outage;
- required network/env configuration;
- why it is suitable for M1.

Do not silently pick.

---

# B-11 — Client API authentication middleware

## Goal

Protect Client API using the chosen token-validation strategy.

## Prerequisites

```text
Auth issuance                                    B-08
Refresh/session                                  B-09
Validation strategy                              B-10
```

## Required behavior

For:

```text
Authorization: Bearer <token>
```

Client API must reject:

```text
missing token
malformed token
expired token
revoked/outdated token
```

and extract:

```text
client identity
scope
session identity
```

## Status semantics

Recommended:

```text
401 authentication failure
403 authenticated but insufficient permission
```

## Tests

- missing -> 401;
- malformed -> 401;
- expired -> 401;
- old token after refresh -> 401;
- valid token proceeds.

---

# B-12 — Scope and field-level authorization

## Goal

Enforce:

```text
Media        summary only
Field Team   full permitted data
Internal Ops full permitted data
```

## Prerequisites

```text
Auth middleware                                 B-11
HazardEvent shape                               READY
Exact full summary field list                   TEAM DECISION REQUIRED
```

## Minimum mandatory Media restriction

Media must not receive at least:

```text
latitude
longitude
attributes
```

## Implementation principle

Use Client API DTO/projection logic.

Do not mutate or depend directly on Aggregator internals.

## Explicit raw request

The assignment requires an explicit raw request from Media to be rejected.

Possible request contracts:

```text
?include_raw=true
```

or

```text
?fields=latitude,longitude,attributes
```

But do not silently select the final syntax.

## Tests

- Media normal response has no raw fields;
- Field Team gets full permitted fields;
- Internal Ops gets full permitted fields;
- explicit Media raw request -> 403.

---

# STAGE 5 — Member B Resilience Layer

---

# B-13 — Aggregator dependency timeout and controlled failures

## Goal

Prevent Client API from hanging when Aggregator is slow or unavailable.

## Prerequisites

```text
Aggregator adapter                               B-05
Timeout value                                    TEAM DECISION REQUIRED
Public error response                            TEAM DECISION REQUIRED
```

## Required behavior

Configure a bounded timeout.

Handle:

```text
timeout
connection refused
Aggregator 5xx
malformed response
request cancellation
```

with controlled responses.

No stack traces or raw internal errors.

---

# B-14 — Stale/unavailable source response semantics

## Goal

Correctly expose source problems downstream.

## Prerequisites

```text
Aggregator source state available                READY
last_success_at available                        READY
Exact public stale semantics                     BLOCKED BY MEMBER A + TEAM
```

## Current Aggregator data

```text
available
last_success_at
last_error
```

## Can implement now

- parsing/types;
- internal representation;
- tests against current response.

## Must wait before final public contract

Do not silently finalize:

```text
stale_since
source_status
unavailable
```

## Required final behavior

If PVMBG is unavailable:

```text
seismic request -> still normal
volcanic request ->
    last known data + explicit stale info
    OR explicit controlled unavailability
```

Never generic hanging behavior.

---

# B-15 — Request concurrency control / controlled 429

## Goal

Prevent Client API resource exhaustion.

## Prerequisites

```text
P2 requirement                                  READY
Exact concurrency limit                         TEAM DECISION REQUIRED
429 allowed                                     READY
Load infrastructure                             SHARED WITH MEMBER C
```

## Suggested simple design

```text
incoming request
      |
      v
bounded semaphore
   /       \
space      full
 |          |
 v          v
process    429
```

## Requirements

- no unbounded goroutine creation;
- concurrency limit configurable;
- permit always released;
- 429 distinguishable from failures.

## Tests

- within limit succeeds;
- over limit -> 429;
- canceled request does not leak permit.

---

# B-16 — Structured observability

## Goal

Make cross-service behavior demonstrable.

## Prerequisites

```text
structured logging requirement                  READY
X-Correlation-ID direction                      READY
```

## Required fields where relevant

```text
timestamp
level
service
correlation_id
operation
target
latency_ms
status
client_id
scope
```

Never log:

```text
password
JWT signing secret
refresh token
full access token
```

## Trace flows

### Hazard query

```text
Client
 -> Client API
 -> Aggregator
 -> Client API
 -> Client
```

### Login/refresh

```text
Client
 -> Auth
 -> issue/rotate tokens
```

---

# STAGE 6 — Team Integration and Verification

---

# B-17 — P3 automated test suite

## Goal

Make Member B's P3 responsibilities reproducible.

## Prerequisites

```text
Auth complete                                    REQUIRED
Client API auth complete                         REQUIRED
Upstream cross-credential test                   MEMBER A
```

## Required scenarios

### Media summary only

```text
login media
GET hazards
verify no latitude/longitude/attributes
```

### Media raw denied

```text
media token
explicit raw request
-> 403
```

### Access expiry

```text
field-team login
access valid
TTL expires naturally
old token rejected
```

### Refresh

```text
refresh without manual login
new token valid
```

### Old token invalid

```text
Token A
refresh
Token B valid
Token A rejected
```

### Refresh replay

```text
R1 rotated to R2
reuse R1
-> reject
```

### Upstream trust domain

Final integration evidence only:

```text
BMKG credential -> PVMBG -> 401/403
PVMBG credential -> BMKG -> 401/403
```

Member B should not take over Member A’s upstream implementation.

---

# B-18 — P2 integration and load-readiness

## Goal

Prepare Client API for team load testing.

## Prerequisites

```text
Client API functional                            REQUIRED
resilience controls                              REQUIRED
k6 infra                                         MEMBER C
Aggregator/storage resilience                    MEMBER A/C
```

## Member B must ensure

```text
no indefinite hang
dependency timeout
bounded concurrency
controlled 429 if enabled
auth still works under load
source status mapped correctly
correlation IDs/logs present
```

## Final team-level measurements

```text
>= 50 parallel connections
>= 60 seconds sustained
throughput
p50
p95
p99
error rate
controlled 429 count
```

Do not claim success until real load testing is run.

---

# B-19 — Full Compose integration

## Goal

Integrate Member B services into the full system after early Compose-readiness has already been established.

## Prerequisites

```text
Auth implementation                             READY
Client API implementation                       READY
Compose ownership                               MEMBER C
final ports/env/network                         MEMBER C
```

## Required final wiring

```text
client-api
  |
  +--> aggregator
  |
  +--> auth / token validation path

auth
  |
  +--> Auth-owned session state
```

Client API must still have:

```text
NO direct PostgreSQL connection
```

## Expected env variables

Possible final set:

```text
AUTH_PORT
CLIENT_API_PORT

AGGREGATOR_BASE_URL
AUTH_BASE_URL

JWT_SIGNING_SECRET

ACCESS_TOKEN_TTL_SECONDS
REFRESH_TOKEN_TTL_SECONDS

MEDIA_CLIENT_ID
MEDIA_CLIENT_PASSWORD

FIELD_TEAM_CLIENT_ID
FIELD_TEAM_CLIENT_PASSWORD

INTERNAL_OPS_CLIENT_ID
INTERNAL_OPS_CLIENT_PASSWORD

AGGREGATOR_REQUEST_TIMEOUT_MS
CLIENT_API_MAX_CONCURRENT_REQUESTS
```

Use team naming conventions.

---

# B-20 — Demo scripts and README

## Goal

Make P2/P3 reproducible for the team and assessment.

## Suggested scripts

```text
demo/
├── p3-login.sh
├── p3-media-fields.sh
├── p3-media-raw-denied.sh
├── p3-token-expiry.sh
├── p3-token-refresh.sh
├── p3-old-token-rejected.sh
└── p2-client-api-resilience.sh
```

## Each script must

- state the scenario;
- show HTTP status;
- avoid exposing real secrets;
- fail clearly;
- be repeatable.

## README must explain

1. Auth endpoints;
2. login flow;
3. refresh flow;
4. access-token TTL;
5. invalidation strategy;
6. identities/scopes;
7. Media field policy;
8. Client API endpoints;
9. Aggregator dependency;
10. correlation ID;
11. timeout behavior;
12. concurrency/429 behavior;
13. environment variables;
14. demo commands;
15. known limitations.

---

# 4. Revised Task Dependency Graph

```text
                    B-01 Contracts
                         |
           +-------------+-------------+
           |                           |
           v                           v
    B-02 Auth Skeleton          B-03 API Skeleton
           |                           |
           |                    B-05 Aggregator Adapter
           |                           |
           |                    B-06 Hazard Endpoint
           |
           +---------> B-04 Compose-ready early
           |
           v
    B-07 Identities + Argon2id
           |
           v
    B-08 JWT
           |
           v
    B-09 Refresh/session
           |
           v
    B-10 Validation strategy
           |
           v
    B-11 API auth middleware
           |
           v
    B-12 Authorization
           |
           +--------------------+
                                |
                                v
                      B-13 Timeout/failures
                                |
                      B-14 Stale semantics
                                |
                      B-15 Concurrency/429
                                |
                      B-16 Observability
                                |
                                v
                      B-17 P3 verification
                                |
                      B-18 P2/load readiness
                                |
                      B-19 Full Compose
                                |
                      B-20 Demo + README
```

---

# 5. What Can Start Immediately

## READY now

```text
B-01 provisional contracts
B-02 Auth skeleton
B-03 Client API skeleton
B-04 service Docker/health readiness
B-05 provisional Aggregator HTTP adapter
B-06 provisional hazard endpoint
B-07 identities + Argon2id
B-08 JWT issuance
B-16 base observability
```

These tasks do not require Member A/C to finish first.

---

# 6. What Can Proceed Provisionally

```text
B-05 Aggregator adapter
B-06 hazard endpoint
B-12 Media projection
B-13 timeout behavior
B-14 source-state parsing
```

Use the current observed contract but isolate assumptions behind small interfaces/adapters.

---

# 7. What Must Not Be Finalized Without Agreement

## Member A / team

```text
exact Aggregator internal API
final HazardEvent list response semantics
stale/unavailable public response
count/pagination semantics
Aggregator internal route protection
late upstream source semantics
```

## Member C / team

```text
Auth port
Auth service URL
Compose service wiring
final env variable names
persistent session store if introduced
load test infrastructure
```

## Member B / team architecture decision

```text
local JWT vs Auth introspection vs hybrid
refresh/access-token invalidation mechanism
raw-field request syntax
Client API timeout
concurrency limit
429 policy
```

---

# 8. Mandatory Codex Task Report Format

For every task:

```markdown
## Task
B-XX — <name>

## Prerequisite Status
- READY:
- READY WITH PROVISIONAL CONTRACT:
- BLOCKED BY MEMBER A:
- BLOCKED BY MEMBER C:
- TEAM DECISION REQUIRED:

## Files Inspected
- ...

## Current State
...

## Assumptions
...

## Proposed Design
...

## Files to Modify
- ...

## Implementation
...

## Tests Added
- ...

## Commands to Verify
```bash
...
```

## Acceptance Criteria
- [ ] ...

## Remaining Dependencies / Risks
- ...
```

---

# 9. Recommended First Codex Prompt

```text
Read:

- context.md
- system-architecture.md
- README.md
- services/auth/**
- services/client-api/**
- services/aggregator/internal/httpapi/server.go
- services/aggregator/internal/domain/hazard.go
- docker-compose.yml
- .env.example

Do not modify code yet.

Use the revised Member B handover.

Your first job is to inspect B-01 through B-06.

1. Confirm the prerequisite status of each task.
2. Identify any repository changes since the previous inspection.
3. Identify mismatches between system-architecture.md, context.md, and actual code.
4. Treat context.md/current repository as newer implementation direction where system-architecture.md still contains outdated TBD decisions, but explicitly report every mismatch.
5. Confirm whether Client API can already integrate against GET /internal/v1/hazards as a provisional contract.
6. Propose the exact minimal work for:
   - B-01 contract document;
   - B-02 Auth skeleton;
   - B-03 Client API skeleton;
   - B-04 early Docker/Compose readiness;
   - B-05 Aggregator HTTP adapter;
   - B-06 provisional hazard endpoint.
7. Do not implement advanced JWT/refresh/auth middleware yet.
8. Stop before writing code and wait for approval.

Important:
- Auth and Client API should be developed in parallel.
- Do not wait for Auth completion before preparing Client API.
- The first integration checkpoint is proving Client API -> Aggregator.
- Do not access PostgreSQL directly from Client API.
- Do not silently invent unresolved contracts.
```

---

# 10. Definition of Done — Member B

```text
[ ] Auth runs independently
[ ] Client API runs independently
[ ] both services are Docker/Compose-ready early
[ ] Client API successfully retrieves real data through Aggregator API
[ ] Media / Field Team / Internal Ops are distinct identities
[ ] passwords use Argon2id
[ ] access token TTL configurable, default 60s
[ ] refresh works without manual login
[ ] refresh token rotates
[ ] old refresh token replay rejected
[ ] old access token rejected after refresh
[ ] Client API authenticates server-side
[ ] Client API authorizes server-side
[ ] Media never receives forbidden raw fields
[ ] explicit Media raw request rejected
[ ] Client API never accesses PostgreSQL directly
[ ] Client API forwards X-Correlation-ID
[ ] Aggregator outbound latency is logged
[ ] Aggregator calls have bounded timeout
[ ] dependency failure cannot hang Client API indefinitely
[ ] stale/unavailable behavior follows team contract
[ ] concurrency is bounded
[ ] 429 behavior documented if enabled
[ ] P3 scenarios reproducible
[ ] P2 Member B behavior included in team load test
[ ] secrets externalized
[ ] .env.example complete with safe placeholders
[ ] README and demo commands complete
```

---

# 11. Core Principle

The implementation order is not:

```text
finish all Auth
then start API
then integrate at the end
```

It is:

```text
agree contract
      |
      v
build Auth + Client API skeletons in parallel
      |
      v
prove Client API -> Aggregator
      |
      v
layer authentication + authorization
      |
      v
layer resilience
      |
      v
run integrated P2/P3 tests
```

This keeps integration continuous and prevents Member B from becoming blocked by another member unnecessarily.
