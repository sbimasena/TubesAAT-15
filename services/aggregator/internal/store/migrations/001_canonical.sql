CREATE TABLE hazard_events (
    hazard_id TEXT PRIMARY KEY CHECK (hazard_id <> ''),
    source TEXT NOT NULL CHECK (source IN ('BMKG', 'PVMBG')),
    source_ref_id TEXT NOT NULL CHECK (source_ref_id <> ''),
    hazard_type TEXT NOT NULL CHECK (hazard_type IN ('SEISMIC', 'VOLCANIC')),
    severity TEXT NOT NULL CHECK (severity IN ('NORMAL', 'WASPADA', 'SIAGA', 'AWAS')),
    area_name TEXT NOT NULL CHECK (area_name <> ''),
    latitude DOUBLE PRECISION NOT NULL CHECK (latitude BETWEEN -90 AND 90),
    longitude DOUBLE PRECISION NOT NULL CHECK (longitude BETWEEN -180 AND 180),
    occurred_at TIMESTAMPTZ NOT NULL,
    ingested_at TIMESTAMPTZ NOT NULL,
    attributes JSONB NOT NULL CHECK (jsonb_typeof(attributes) = 'object'),
    revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
    UNIQUE (source, source_ref_id),
    CHECK ((source = 'BMKG' AND hazard_type = 'SEISMIC') OR
           (source = 'PVMBG' AND hazard_type = 'VOLCANIC'))
);

CREATE INDEX hazard_events_occurred_at_idx ON hazard_events (occurred_at DESC, hazard_id);
CREATE INDEX hazard_events_source_occurred_at_idx ON hazard_events (source, occurred_at DESC);
CREATE INDEX hazard_events_type_occurred_at_idx ON hazard_events (hazard_type, occurred_at DESC);

CREATE TABLE hazard_outbox (
    message_id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    hazard_id TEXT NOT NULL REFERENCES hazard_events (hazard_id),
    hazard_revision BIGINT NOT NULL,
    payload JSONB NOT NULL CHECK (jsonb_typeof(payload) = 'object'),
    correlation_id TEXT NOT NULL CHECK (length(correlation_id) BETWEEN 1 AND 128),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    published_at TIMESTAMPTZ,
    UNIQUE (hazard_id, hazard_revision)
);

CREATE INDEX hazard_outbox_pending_idx ON hazard_outbox (message_id) WHERE published_at IS NULL;
