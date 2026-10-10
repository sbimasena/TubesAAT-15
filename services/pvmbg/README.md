# Mock PVMBG

PVMBG is an independent Go service that seeds 20 historical volcanic reports and generates reports every `PVMBG_GENERATE_INTERVAL_SECONDS` (default: 10 seconds). The default response delay is 750 ms; set `PVMBG_DELAY_MS=3000` to reproduce the slow-upstream scenario. Supported delay values are 500–3000 ms.

## API

- `GET /health` is public and reports the active schema version, response delay in milliseconds, and whether outage simulation is active. These values let infrastructure checks restore the original runtime settings.
- `GET /volcanic-reports?since=<RFC3339>` requires `Authorization: Bearer <PVMBG_TOKEN>`.
- `POST /admin/schema-version` requires the PVMBG bearer token and accepts `{"version":2}` to add `confidence_level` to a new report immediately, or `{"version":1}` so later reports use the original shape. Reports already emitted keep their original fields.
- `POST /admin/outage` requires the PVMBG bearer token and accepts `{"enabled":true}` or `{"enabled":false}`.

Reports are returned as JSON arrays. Additive fields are included without restarting this service. Configure the token, delay, and generation interval through environment variables. The local Compose token default is `dev-pvmbg-token` and is for development only.
