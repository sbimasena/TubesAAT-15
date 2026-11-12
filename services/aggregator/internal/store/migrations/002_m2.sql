-- M2 sources and hazard types (kontrak-m2.md §3).
ALTER TABLE hazard_events
    DROP CONSTRAINT hazard_events_source_check,
    DROP CONSTRAINT hazard_events_hazard_type_check,
    DROP CONSTRAINT hazard_events_check,
    ADD CONSTRAINT hazard_events_source_check
        CHECK (source IN ('BMKG', 'PVMBG', 'SATELIT', 'SENSOR_PANTAI')),
    ADD CONSTRAINT hazard_events_hazard_type_check
        CHECK (hazard_type IN ('SEISMIC', 'VOLCANIC', 'WILDFIRE', 'COASTAL')),
    ADD CONSTRAINT hazard_events_source_type_check
        CHECK ((source, hazard_type) IN (('BMKG', 'SEISMIC'), ('PVMBG', 'VOLCANIC'),
                                         ('SATELIT', 'WILDFIRE'), ('SENSOR_PANTAI', 'COASTAL'))),
    -- fnv32a(hazard_id) % 24 (D-P1). Backfilled by the migrator, then made NOT NULL.
    ADD COLUMN partition_id SMALLINT CHECK (partition_id BETWEEN 0 AND 23);

CREATE INDEX hazard_events_ingested_at_idx ON hazard_events (ingested_at, hazard_id);
CREATE INDEX hazard_events_partition_idx ON hazard_events (partition_id, ingested_at, hazard_id);

-- D-FENCE: one row per unit, raised by the leader barrier and checked in every write.
CREATE TABLE fence (
    singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
    epoch BIGINT NOT NULL DEFAULT 0 CHECK (epoch >= 0)
);
INSERT INTO fence DEFAULT VALUES;

-- D-P1 amnesia detection: a unit that comes back with an empty disk gets a new incarnation.
CREATE TABLE unit_meta (
    singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
    incarnation UUID NOT NULL DEFAULT gen_random_uuid(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO unit_meta DEFAULT VALUES;

-- D-COORD: durable polling cursor / stream offset, advanced only after the data commit.
CREATE TABLE ingest_cursor (
    source TEXT PRIMARY KEY CHECK (source IN ('BMKG', 'PVMBG', 'SATELIT', 'SENSOR_PANTAI')),
    cursor_value TEXT NOT NULL,
    epoch BIGINT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- D-DLQ draft (owner B): partitioned by message_id with the same router.
CREATE TABLE unprocessed_messages (
    message_id TEXT PRIMARY KEY CHECK (message_id <> ''),
    partition_id SMALLINT NOT NULL CHECK (partition_id BETWEEN 0 AND 23),
    source TEXT NOT NULL CHECK (source IN ('BMKG', 'PVMBG', 'SATELIT', 'SENSOR_PANTAI')),
    source_ref_id TEXT,
    source_position TEXT,
    raw BYTEA NOT NULL,
    reason TEXT NOT NULL CHECK (reason <> ''),
    retryable BOOLEAN NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    status TEXT NOT NULL CHECK (status IN ('retrying', 'parked', 'resolved')),
    received_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    next_attempt_at TIMESTAMPTZ,
    resolved_at TIMESTAMPTZ
);
CREATE INDEX unprocessed_messages_open_idx ON unprocessed_messages (received_at, message_id)
    WHERE status <> 'resolved';

-- D-FR draft (owner B): idempotent insert keyed by report_id.
CREATE TABLE field_reports (
    report_id TEXT PRIMARY KEY CHECK (report_id <> ''),
    partition_id SMALLINT NOT NULL CHECK (partition_id BETWEEN 0 AND 23),
    payload JSONB NOT NULL CHECK (jsonb_typeof(payload) = 'object'),
    received_at TIMESTAMPTZ NOT NULL,
    processed_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- P4 draft (owner A): raw detections deduplicated by detection key, coordinates kept as raw strings in payload.
CREATE TABLE satellite_detections (
    detection_key TEXT PRIMARY KEY CHECK (detection_key <> ''),
    partition_id SMALLINT NOT NULL CHECK (partition_id BETWEEN 0 AND 23),
    payload JSONB NOT NULL CHECK (jsonb_typeof(payload) = 'object'),
    stream_offset TEXT NOT NULL,
    detected_at TIMESTAMPTZ NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    retracted_at TIMESTAMPTZ
);
CREATE INDEX satellite_detections_detected_at_idx ON satellite_detections (detected_at);
