# Aggregator

The Aggregator polls BMKG and PVMBG independently every `POLL_INTERVAL_SECONDS` (default: 3 seconds), maps their records to `HazardEvent`, correlates tsunami warnings, and exposes the canonical events through an internal HTTP API.

## API

- `GET /health` reports service health and the latest upstream poll status.
- `GET /hazards` returns `{ "data": [...], "count": n, "sources": {...} }`.
- `GET /hazards?source=BMKG&hazard_type=SEISMIC&since=<RFC3339>&limit=100` filters the result. The maximum limit is 1000.
- `GET /internal/v1/hazards` is an alias for the list endpoint for internal clients.

Hazard IDs are deterministic from the upstream source and source reference ID, so repeated polling is idempotent. Unknown additive PVMBG fields are retained in `attributes`; known `confidence_level` is retained there as well. Volcano names and coordinates come from the Aggregator's static reference table.

Schema evolution supports new valid JSON properties while the existing identity and mapping fields keep their names and types. Additions are retained in `attributes`. Renaming or removing `report_id`, `volcano_id`, `alert_level`, or `reported_at`, or providing an unknown `volcano_id` without a coordinate reference, is unsupported and the affected report cannot be normalized.

## Current local adapters

The canonical repository is currently in memory, capped at 10,000 events, and the publisher logs when an event would be sent. This keeps the Aggregator runnable with the present Compose file, which does not include PostgreSQL or RabbitMQ. The repository and publisher are interfaces so Member C's database and broker setup can be connected without changing source mapping or polling. Durable persistence and broker delivery are not enabled yet.

Configure source URLs, credentials, the listen port, and polling interval through environment variables. `BMKG_BASE_URL`, `PVMBG_BASE_URL`, `BMKG_API_KEY`, and `PVMBG_TOKEN` are required. The credentials must differ; the Aggregator exits on missing configuration or reused credentials. Do not use the Compose example credentials outside local development.
