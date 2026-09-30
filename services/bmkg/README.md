# Mock BMKG

BMKG is an independent Go service that serves simulated seismic events and tsunami warnings. It seeds 20 historical events on startup and generates another event every `BMKG_GENERATE_INTERVAL_SECONDS` (default: 15 seconds).

## API

- `GET /health` is public.
- `GET /seismic-events?since=<RFC3339>` requires `X-BMKG-Key`.
- `GET /tsunami-warnings?since=<RFC3339>` requires `X-BMKG-Key`.

Both data endpoints return JSON arrays. A missing `since` value returns the current catalog. Requests return an `X-Correlation-ID` and write structured logs with outbound latency available to the Aggregator.

Configure `BMKG_API_KEY` and `BMKG_GENERATE_INTERVAL_SECONDS` through the environment. The local Compose default key is `dev-bmkg-key` and is for development only.
