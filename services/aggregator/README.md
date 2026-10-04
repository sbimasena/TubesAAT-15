# Aggregator

The Aggregator polls BMKG and PVMBG independently every `POLL_INTERVAL_SECONDS` (default: 3 seconds), maps their records to `HazardEvent`, correlates tsunami warnings, and exposes the canonical events through an internal HTTP API.

## API

- `GET /health` reports storage availability and the latest upstream poll status. A database outage returns HTTP 503; an upstream outage alone does not make stored data unavailable.
- `GET /hazards` returns `{ "data": [...], "count": n, "sources": {...} }`.
- `GET /hazards?source=BMKG&hazard_type=SEISMIC&since=<RFC3339>&limit=100` filters the result. The maximum limit is 1000.
- `GET /internal/v1/hazards` is an alias for the list endpoint for internal clients.

Hazard IDs are deterministic from the upstream source and source reference ID, so repeated polling is idempotent. Unknown additive PVMBG fields are retained in `attributes`; known `confidence_level` is retained there as well. Volcano names and coordinates come from the Aggregator's static reference table.

Schema evolution supports new valid JSON properties while the existing identity and mapping fields keep their names and types. Additions are retained in `attributes`. Renaming or removing `report_id`, `volcano_id`, `alert_level`, or `reported_at`, or providing an unknown `volcano_id` without a coordinate reference, is unsupported and the affected report cannot be normalized.

## PostgreSQL and transactional outbox

`DATABASE_URL` is required. Aggregator owns the canonical database and runs the embedded migration in `internal/store/migrations/001_canonical.sql` on startup. The migration version is recorded in `schema_migrations`; restarting with an existing volume does not recreate tables. The connection pool is capped at eight connections and read/write operations have a three-second deadline. Database unavailability returns HTTP 503 from hazard queries.

`hazard_events` contains fixed canonical columns, dynamic `attributes JSONB`, and an internal revision. `hazard_id` is the primary key and `(source, source_ref_id)` is unique. An insert or meaningful update writes an immutable `HazardEvent` snapshot to `hazard_outbox` in the same transaction. JSONB content is compared without key-order differences, and a new mapping timestamp alone does not count as a change. Repeated polling therefore does not create additional messages. A late tsunami warning updates the same hazard and creates a new outbox message/revision.

Polling cursors advance after the transaction commits. Failed writes retain the cursor so the next cycle can retry, including warnings already cached by the preceding failed cycle. Cursors remain in memory for M1; after restart, Aggregator rebuilds them from available upstream history while PostgreSQL deduplicates replayed data.

## RabbitMQ outbox publisher (stage 2)

`BROKER_URL` is required. One background worker reads up to 50 pending snapshots in message-ID order. It targets only the pre-provisioned durable fanout exchange `hazard.events`, publishes persistent JSON with `mandatory=true`, and waits for a positive publisher confirm without a return before setting `published_at`. AMQP `MessageId` is the outbox ID; `CorrelationId` and `X-Correlation-ID` retain the polling ID. Headers include `schema_version=1` and `hazard_revision`.

Connection/setup/publish operations are bounded to five seconds. The worker retries every three seconds, reconnects after publish failure, and immediately drains another full batch. Cancellation closes socket I/O as well as interrupting the confirm wait. Broker failures leave rows pending while ingestion and HTTP queries continue; `/health` checks storage/upstream availability and does **not** certify broker delivery. Publisher logs show connection failures, batch size (up to 50, not total pending count), confirmed IDs, correlation IDs, and numeric latency.

An accepted message followed by a crash or failed database mark is published again with the same ID/body. Delivery is at-least-once for existing bindings, with healthy retained volumes and eventual recovery. Mandatory routing proves at least one queue is bound, not that both subscriptions still exist. Queue deletion/purge or lost volumes can lose already-marked messages. One Aggregator replica is supported; concurrent workers would need row leases. There is no automatic retention/cleanup of records or outbox rows. Consumers and their durable deduplication are stage 3.

## Storage checks

From the repository root, start the development stack and run the Aggregator checks on its own storage network:

```sh
docker compose --env-file .env.example up --build -d canonical-store message-broker bmkg pvmbg aggregator
docker compose --env-file .env.example -f docker-compose.yml -f tests/compose-storage.yml run --build --rm --no-deps aggregator
```

The test override builds the Aggregator's builder stage and uses a temporary PostgreSQL schema, removed after testing. Existing canonical records are untouched. It checks duplicate polling, revisions and immutable snapshots, old/new JSONB fields without migration, filters, atomic rollback, concurrent deduplication, and reopening the repository. With `TEST_BROKER_URL`, it also uses a private exchange/queue to check the actual publisher, persistent message metadata, cancelled publish, accepted-but-unmarked retry, and mandatory returns. Test resources are removed on normal completion. Without `TEST_DATABASE_URL`, local `go test ./...` skips the database integration check; the Compose command above runs it against real PostgreSQL.

Configure source URLs, credentials, database URL, listen port, and polling interval through environment variables. `BMKG_BASE_URL`, `PVMBG_BASE_URL`, `BMKG_API_KEY`, `PVMBG_TOKEN`, `DATABASE_URL`, and `BROKER_URL` are required. The upstream credentials must differ; the Aggregator exits on missing configuration or reused credentials. Do not use the Compose example credentials outside local development.
