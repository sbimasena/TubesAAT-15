# Project Context — IF4031 Milestone 1

> Historical design notes for IF4031 Milestone 1. See the README for current deployment instructions.
>
> **Source-of-truth priority:** official assignment specification > `system-architecture.md` > this file > implementation convenience.
> If this file conflicts with the official specification, follow the official specification and update this file.

---

## 1. Project Goal

Build a distributed disaster-coordination platform that integrates two autonomous upstream institutions:

- **BMKG** — seismic events and tsunami warnings.
- **PVMBG** — volcanic activity reports.

**BNPB** acts as the coordination layer. BNPB periodically ingests data from both sources, normalizes heterogeneous source data into a canonical `HazardEvent`, persists it, serves downstream clients with different access rights, and distributes newly ingested events asynchronously to independent consumers.

Milestone 1 is a **proof-of-concept of distributed-system fundamentals**, not a production platform. Prefer the simplest design that clearly demonstrates the required properties.

---

## 2. Milestone 1 Scope

The assessed system covers:

- 2 upstream institutions: BMKG and PVMBG;
- 2 disaster domains: seismic and volcanic;
- BNPB as the coordination node;
- 3 downstream client identities:
  - Media / Pers;
  - Tim Lapangan / BASARNAS / Pemda;
  - BNPB Internal / Operations;
- at least 2 independent event consumers.

Do not add unrelated domain features unless they are needed to satisfy the assignment.

---

## 3. Non-Negotiable Assignment Constraints

### 3.1 Network-only service communication

All communication across service boundaries must happen over the network using REST/HTTP, gRPC, or a message broker.

Do **not**:

- call functions directly across services;
- import another service's business logic;
- create a shared business-logic library to bypass a service boundary.

### 3.2 Independent containers

Each long-running application service must:

- have its own Dockerfile;
- run in its own container;
- be independently startable/stoppable;
- communicate with other services over the network.

The complete stack must be startable with one orchestration command, expected to be:

```bash
docker compose up
```

### 3.3 Single-owner storage

Every persistent store has exactly one owning service.

For the Canonical Store:

- owner: **Aggregator**;
- Aggregator may access it directly;
- Client-Facing API may **not** access it directly;
- other services request canonical data through the Aggregator API.

### 3.4 Polling-based ingestion

BMKG and PVMBG mocks are passive.

They do **not** push events into BNPB.

The Aggregator polls them periodically with a configurable interval. Recommended assignment interval: **2–5 seconds**.

### 3.5 Separate trust domains

Credentials for BMKG, PVMBG, and BNPB downstream clients are separate.

A valid credential for one domain must not automatically work in another domain.

### 3.6 Required observability baseline

Every service must provide or participate in:

- health checks;
- structured logs;
- correlation-ID propagation;
- outbound-call latency logging.

Do not postpone these until the end of the project.

---

## 4. High-Level Architecture

```text
                         ┌────────────────────┐
                         │     BMKG Mock      │
                         │       :8081        │
                         └─────────┬──────────┘
                                   │ polling
                                   │ X-BMKG-Key
                                   ▼
                         ┌────────────────────┐
                         │                    │
                         │     Aggregator     │
                         │                    │
                         └──────┬──────┬──────┘
                                │      │
                       persist  │      │ publish
                                ▼      ▼
                        ┌──────────┐  ┌──────────────┐
                        │Canonical │  │   RabbitMQ   │
                        │ Store    │  │              │
                        └────┬─────┘  └──────┬───────┘
                             │               │
                     Aggregator API      ┌───┴────────────┐
                             │           ▼                ▼
                             │    Notification      Dashboard/Audit
                             │      Consumer          Consumer
                             ▼
                    ┌───────────────────┐
                    │ Client-Facing API │
                    │       :8080       │
                    └────────┬──────────┘
                             │ authorization / scope
                    ┌────────┼─────────┐
                    ▼        ▼         ▼
                  Media   Field Team  Internal Ops
                    ▲        ▲         ▲
                    └────────┼─────────┘
                             │ access / refresh token
                       ┌──────────────┐
                       │ Auth Service │
                       └──────────────┘

                         ┌────────────────────┐
                         │     PVMBG Mock     │
                         │       :8082        │
                         └─────────┬──────────┘
                                   │ polling
                                   │ Bearer credential
                                   └──────────────> Aggregator
```

The diagram is conceptual. `system-architecture.md` remains the detailed architecture document.

---

## 5. Service Boundaries

Long-running application services:

```text
bmkg
pvmbg
aggregator
client-api
auth
notification-consumer
dashboard-consumer
```

Infrastructure components:

```text
postgres / canonical-store
rabbitmq / message-broker
```

Downstream clients can be simple CLI/test programs rather than full graphical applications.

---

## 6. Service Responsibilities

### 6.1 BMKG Mock

Owns the BMKG simulation only.

Required behavior includes:

- seed at least 20 historical records on startup;
- generate new data periodically;
- `GET /seismic-events?since=<timestamp>`;
- `GET /tsunami-warnings?since=<timestamp>`;
- `GET /health`;
- low and consistent simulated delay, approximately **50–150 ms**;
- validate `X-BMKG-Key`;
- reject invalid or foreign credentials with 401/403.

BMKG and PVMBG must remain autonomous simulations.

### 6.2 PVMBG Mock

Required behavior includes:

- seed at least 20 historical records on startup;
- generate new data periodically;
- `GET /volcanic-reports?since=<timestamp>`;
- `POST /admin/schema-version`;
- `POST /admin/outage`;
- `GET /health`;
- configurable delay approximately **500 ms–3 s**;
- separate `Authorization: Bearer <pvmbg-token>` credential;
- runtime outage simulation;
- runtime additive schema change that introduces `confidence_level` without restarting the mock or gateway.

### 6.3 Aggregator

The Aggregator is the core BNPB ingestion and canonicalization service.

For M1 it keeps these responsibilities together:

- independent periodic polling of BMKG;
- independent periodic polling of PVMBG;
- upstream authentication;
- parsing source responses;
- source correlation, including tsunami warnings;
- canonical mapping to `HazardEvent`;
- polling cursor/watermark management;
- duplicate handling / idempotency as required by the implementation;
- persistence into Canonical Store;
- internal query API for Client-Facing API;
- publication of new `HazardEvent` messages to RabbitMQ;
- graceful behavior when one upstream is slow or unavailable.

Only Aggregator accesses the Canonical Store directly.

### 6.4 Client-Facing API

Responsibilities:

- receive downstream read-only requests;
- validate authenticated access tokens;
- enforce scopes server-side;
- prevent Media clients from receiving raw fields;
- call Aggregator API for hazard data;
- never query Canonical Store directly.

### 6.5 Auth / Token Service

Responsibilities:

- maintain distinct downstream client identities;
- authenticate clients;
- issue short-lived access tokens;
- default access-token TTL: **60 seconds**, configurable;
- issue/handle refresh tokens;
- assign authorization scope/claims;
- support the demo in which a session continues after access-token expiry without manual login.

The final token-revocation / old-token-invalid-after-refresh mechanism must satisfy P3. Do not assume plain stateless JWT alone automatically satisfies this behavior.

### 6.6 Notification Consumer

Consumes canonical hazard events independently and simulates notification processing.

It must not require the Aggregator to know that it exists.

### 6.7 Dashboard / Audit Consumer

Consumes the same event stream independently and simulates dashboard/audit processing.

It must not require the Aggregator to know that it exists.

---

## 7. Canonical Data Model

The canonical entity is `HazardEvent`.

Core fields required by the assignment:

```text
hazard_id       string
source          BMKG | PVMBG
source_ref_id   string
hazard_type     SEISMIC | VOLCANIC
severity        NORMAL | WASPADA | SIAGA | AWAS
area_name       string
latitude        float
longitude       float
occurred_at     ISO-8601 datetime
ingested_at     ISO-8601 datetime
attributes      map<string, any>
```

`attributes` stores source-specific fields that do not have a fixed canonical field.

### 7.1 SeismicEvent -> HazardEvent

Important mapping:

```text
source            = BMKG
source_ref_id     = event_id
hazard_type       = SEISMIC
area_name         = region_name
latitude          = epicenter_lat
longitude         = epicenter_lon
occurred_at       = occurred_at
attributes        = magnitude, depth_km, potential_tsunami,
                    plus related TsunamiWarning fields if present
```

Severity rules:

- related TsunamiWarning exists -> use its `threat_level`;
- no warning and magnitude < 5.0 -> `NORMAL`;
- 5.0 <= magnitude < 6.5 -> `WASPADA`;
- magnitude >= 6.5 -> `SIAGA`.

### 7.2 VolcanicReport -> HazardEvent

Important mapping:

```text
source            = PVMBG
source_ref_id     = report_id
hazard_type       = VOLCANIC
area_name         = volcano name mapped from volcano_id
latitude/longitude= BNPB static volcano reference table
occurred_at       = reported_at
severity          = alert_level mapped to canonical enum
attributes        = eruption_count_24h,
                    ash_column_height_m,
                    confidence_level when available
```

Unknown/additive source fields must follow the documented schema-evolution policy. The intended direction is to retain additive source fields in `attributes` rather than requiring a database migration.

---

## 8. Current Technology Stack

### 8.1 Fixed / intended choices

| Area | Choice |
|---|---|
| Backend language | **Go 1.27.1** |
| Service communication | HTTP/REST for synchronous paths unless explicitly changed |
| Canonical database | **PostgreSQL + JSONB** |
| PostgreSQL driver | **pgx** |
| Database migration | **golang-migrate** or equivalent lightweight migration tool |
| Message broker | **RabbitMQ** |
| RabbitMQ client | **amqp091-go** |
| Authentication | **JWT access token + refresh token** |
| Password hashing | **Argon2id** |
| Containerization | **Docker + Docker Compose** |
| Structured logging | Go `log/slog` unless the team intentionally selects another logger |
| Load testing | **k6** |
| Go API testing | `testing` + `httptest` |

### 8.2 HTTP router/framework

**Not frozen yet.**

Possible choices include:

```text
net/http
Chi
Gin
Echo
```

Do not introduce a framework implicitly. Once selected, use it consistently across services unless a service has a concrete reason not to.

### 8.3 Go module strategy

Each backend service should be buildable independently and should own its own `go.mod`.

A root `go.work` may be used for local monorepo development.

`go.work` must not be used as an excuse to import business logic from another service.

---

## 9. PostgreSQL Storage Strategy

PostgreSQL is the Canonical Store implementation.

Use fixed relational columns for stable canonical fields and `JSONB` for dynamic source-specific attributes.

Conceptual schema:

```text
hazard_events
---------------------------------
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
attributes JSONB
```

The appearance of PVMBG `confidence_level` must **not** require a database migration.

Migrations are acceptable for the stable application schema itself, but not as a reaction to every additive upstream field.

The implementation should enforce a sensible identity/uniqueness strategy for source events so polling does not accidentally create endless duplicates.

---

## 10. RabbitMQ Messaging Strategy

The system requires fan-out-like distribution in which multiple independent consumers receive the same published `HazardEvent`.

Do **not** place both consumers on one queue if the intention is that both receive every event; that would create competing consumers.

Expected conceptual topology:

```text
Aggregator
    |
    v
RabbitMQ Exchange
   /            \
  v              v
notification     dashboard/audit
queue            queue
  |              |
  v              v
Consumer A       Consumer B
```

Current intended behavior for M1:

- independent queues per consumer;
- durable topology where practical;
- message acknowledgement;
- producer must not block because one consumer is down;
- consumer restart behavior must be documented and demonstrated;
- if delivery is at-least-once, consumers should be safe against duplicate delivery.

The exact exchange type, durability flags, retry policy, and delivery semantics must be documented in the P5 solution.

---

## 11. Concurrency and Resilience Direction

Go is used partly because concurrency is a first-class concern in this milestone, but **goroutines alone are not a solution**.

Required design properties:

- BMKG polling must not be blocked by slow PVMBG polling;
- PVMBG can be configured to ~3 s delay without making unrelated BMKG-only requests slow;
- one upstream outage must not collapse the entire system;
- request and worker concurrency must be bounded where resource exhaustion is possible;
- use request timeouts and cancellation;
- avoid unlimited goroutine creation;
- keep ingestion path independent from downstream query path where possible.

A natural implementation is to run BMKG and PVMBG pollers independently rather than sequentially in one blocking loop.

Use `context.Context` for cancellation/deadline propagation where appropriate.

---

## 12. Authentication and Authorization Rules

Three trust domains exist:

```text
Aggregator -> BMKG
Aggregator -> PVMBG
Downstream clients -> BNPB Auth + Client API
```

They must remain independent.

### Downstream clients

Required logical identities:

```text
media
field-team
internal-ops
```

Media may only receive fields classified as **Ringkasan**.

Raw fields include at least precise coordinates and `attributes`; these must not leak to Media.

Authorization is enforced by the server. A client hiding a field in its UI is not authorization.

Secrets, signing keys, and credentials:

- come from environment variables or non-committed config;
- are never hardcoded;
- are never committed;
- are documented through `.env.example` using safe example values.

---

## 13. Observability Conventions

The implementation should converge on one correlation header, expected default:

```text
X-Correlation-ID
```

Rules:

- if an entry-point receives no correlation ID, create one;
- propagate the same ID on synchronous downstream calls;
- include it in structured logs;
- carry trace/correlation metadata into published events where practical;
- record outbound latency as a numeric field.

Example log shape:

```json
{
  "timestamp": "2026-10-01T10:15:23Z",
  "level": "INFO",
  "service": "aggregator",
  "correlation_id": "req-abc-123",
  "operation": "poll-pvmbg",
  "target": "pvmbg",
  "latency_ms": 1821,
  "status": 200
}
```

For background polling, the poll cycle / ingestion operation should generate its own correlation ID when no incoming HTTP request exists.

---

## 15. Problem Set Acceptance Context

### P1 — Architecture & Data Interoperability

Must demonstrate:

1. System starts with old PVMBG schema.
2. Aggregator creates valid canonical events from BMKG and PVMBG.
3. `POST /admin/schema-version` is triggered at runtime.
4. `confidence_level` starts appearing without restarting services.
5. Aggregator continues working without code intervention/redeploy.
6. Unknown-field policy is consistent and documented.

Implementation must state what schema changes are supported and intentionally unsupported.

### P2 — Concurrency, Availability & Graceful Degradation

Must demonstrate:

1. PVMBG delay = 3 s.
2. BMKG-only request p95 remains **< 300 ms** while slow PVMBG work occurs concurrently.
3. Sustained load test with **>= 50 parallel connections for >= 60 seconds**.
4. Report throughput, p50, p95, p99, error rate, and controlled 429s if used.
5. Non-controlled error rate < 1%.
6. PVMBG outage does not break seismic service.
7. Volcanic reads return last known data with freshness/stale information or explicit unavailability, not generic 500/hanging behavior.
8. System returns to normal after outage without restart.

### P3 — Service-to-Service Authentication & Trust

Must demonstrate:

1. BMKG credential is rejected by PVMBG.
2. PVMBG credential is rejected by BMKG.
3. Media only receives summary fields.
4. Explicit Media request for raw fields is rejected server-side with 401/403 as required by the chosen API behavior.
5. Field-team access token expires naturally according to configured TTL.
6. Refresh produces a new valid token without manual login.
7. The old token is rejected after the required refresh flow.
8. No secret is hardcoded/committed; `.env.example` exists.

### P4 — Microservices & Flexible Storage

Must demonstrate:

1. Components run as separate containers.
2. One component can be stopped/rebuilt/restarted without rebuilding all others.
3. Unrelated functionality remains available during that restart where dependencies allow it.
4. HazardEvent stores dynamic attributes without migration when `confidence_level` appears.
5. Records from before and after schema evolution coexist and remain readable.
6. Canonical Store is directly accessible only by Aggregator.

### P5 — Fan-out Event Distribution / Pub-sub

Must demonstrate:

1. Every newly mapped HazardEvent is published through the broker.
2. At least two independent consumers receive the stream.
3. Aggregator/producer does not know or synchronously invoke individual consumers.
4. Stopping one consumer does not stop producer or other consumer.
5. Restarting it demonstrates documented backlog/loss behavior.
6. A new consumer can be added without changing producer code.
7. Delivery semantics and idempotency implications are explained.

---

## 16. Suggested Repository Shape

Current repository is still at architecture/documentation stage. A reasonable target structure is:

```text
repo/
├── context.md
├── system-architecture.md
├── go.work
├── services/
│   ├── bmkg/
│   │   ├── cmd/server/main.go
│   │   ├── internal/
│   │   ├── go.mod
│   │   └── Dockerfile
│   ├── pvmbg/
│   ├── aggregator/
│   ├── client-api/
│   ├── auth/
│   ├── notification-consumer/
│   └── dashboard-consumer/
├── clients/
│   ├── media-client/
│   ├── field-client/
│   └── internal-client/
├── infra/
│   ├── postgres/
│   └── rabbitmq/
├── migrations/
├── demo/
│   ├── p1-schema-evolution.sh
│   ├── p2-concurrency.sh
│   ├── p2-outage.sh
│   ├── p2-load-test.js
│   ├── p3-cross-credential.sh
│   ├── p3-token-refresh.sh
│   ├── p4-service-restart.sh
│   └── p5-consumer-failure.sh
├── docs/
├── docker-compose.yml
├── .env.example
└── README.md
```

This is a working structure, not an assignment-mandated folder layout.

---

## 17. Implementation Constraints

When generating or editing code in this repository:

1. Preserve service boundaries.
2. Do not create shared business logic across services.
3. Do not let Client API access PostgreSQL directly.
4. Do not let consumers access PostgreSQL as a shortcut.
5. Do not turn polling into upstream push/webhooks.
6. Do not reuse one credential across BMKG, PVMBG, and downstream clients.
7. Do not make BMKG polling wait for slow PVMBG polling.
8. Do not place both RabbitMQ subscribers on one competing-consumer queue if both must receive every event.
9. Do not hardcode secrets or environment-specific URLs.
10. Do not add Kubernetes, service mesh, distributed tracing platforms, or other M2-scale infrastructure unless explicitly requested.
11. Prefer small, understandable Go packages over generic abstraction layers.
12. Prefer standard library functionality where it keeps the design clear.
13. Add timeouts to outbound network operations.
14. Avoid unbounded goroutine creation.
15. Use structured logs and correlation IDs from the beginning.
16. Keep changes scoped to the requested service/problem unless an interface contract must change.
17. If an architectural decision is not yet frozen, do not silently invent one; surface it as a decision to be made.

---

## 18. Decisions Still to Freeze

Before large-scale coding, explicitly settle and document these decisions:

- HTTP router/framework (`net/http`, Chi, Gin, etc.);
- exact internal Aggregator API contract;
- exact ports for services not fixed by the assignment;
- RabbitMQ exchange type and queue durability/ack/retry semantics;
- access-token validation strategy at Client API;
- refresh-token storage/revocation strategy that satisfies P3;
- canonical source-event identity / deduplication rule;
- polling watermark/cursor semantics;
- tsunami-warning late-arrival/update behavior;
- store-vs-publish failure semantics for Aggregator;
- exact stale-data response shape (`stale_since`, source status, etc.);
- final correlation-ID convention;
- detailed behavior of both event consumers.

Do not treat an unresolved item as implicitly decided just because a library has a default.

---

## 19. Definition of Done for Milestone 1

Milestone 1 is ready only when:

- fresh clone can be run by following README;
- `docker compose up` starts the complete required stack;
- every service has a Dockerfile and health check;
- runtime configuration is externalized;
- `.env.example` is complete and contains no real secrets;
- BMKG/PVMBG mocks follow the mandated contracts;
- all P1–P5 scenarios are reproducible;
- load-test metrics are saved;
- correlation ID can be shown across a real flow;
- schema evolution works without service restart or Canonical Store migration;
- one container can be independently rebuilt/restarted;
- two independent broker consumers work as required;
- report evidence exists for every acceptance criterion;
- README contains commands to trigger every demo scenario;
- final milestone tag is created only after clean-clone verification.
