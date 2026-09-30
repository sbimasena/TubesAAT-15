# Sistem Koordinasi Bencana IF4031 M1

Proof of concept for a distributed disaster-coordination platform that integrates BMKG seismic events and PVMBG volcanic reports for BNPB.

## Stack

- Go 1.27.1, with one Go module per service
- `net/http` for service APIs
- Docker Compose for local orchestration
- PostgreSQL and RabbitMQ are the intended canonical store and broker; they are not included in the current Compose file yet

## Start the Member A flow

Run the source mocks and Aggregator:

```sh
docker compose up --build bmkg pvmbg aggregator
```

The Aggregator polls each source independently every 3 seconds. Query canonical events at `http://localhost:8083/hazards`; check its source status at `http://localhost:8083/health`.

The BMKG mock requires `X-BMKG-Key`. PVMBG data and admin endpoints require `Authorization: Bearer <PVMBG_TOKEN>`. Local Compose defaults use distinct development credentials (`dev-bmkg-key` and `dev-pvmbg-token`); copy `.env.example` to `.env` to adjust the polling interval, mock generation intervals, credentials, or PVMBG delay.

Trigger PVMBG schema evolution at runtime:

```sh
curl -X POST http://localhost:8082/admin/schema-version \
  -H 'Authorization: Bearer dev-pvmbg-token' \
  -H 'Content-Type: application/json' \
  -d '{"version":2}'
```

This emits a v2 report with `confidence_level` without restarting PVMBG. Older v1 reports remain unchanged, and the Aggregator retains the new field in `HazardEvent.attributes`.

The current Aggregator uses a bounded in-memory repository and logs that broker publishing is not configured. Its repository and publisher are interfaces for connecting Member C's PostgreSQL and RabbitMQ setup. Other service entrypoints remain placeholders pending their owners' work.
