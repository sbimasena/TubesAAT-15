-- D-DLQ (dibekukan 12 Nov, owner B + C): durable storage for messages the
-- Aggregator cannot process. The table is created identically on every unit;
-- rows are routed by message_id (PartitionOf) with RF=2 copies, like
-- hazard_events. P3.2: the failed payload is stored verbatim (raw bytes, never
-- re-serialized) with the reason, source and time.
CREATE TABLE unprocessed_messages (
    message_id    TEXT PRIMARY KEY CHECK (message_id <> ''),
    partition_id  SMALLINT NOT NULL CHECK (partition_id BETWEEN 0 AND 23),
    source        TEXT NOT NULL CHECK (source IN ('BMKG', 'PVMBG', 'SATELIT', 'SENSOR_PANTAI')),
    source_ref_id TEXT,
    position      TEXT,
    raw           BYTEA NOT NULL,
    reason        TEXT NOT NULL CHECK (reason <> ''),
    attempts      INTEGER NOT NULL CHECK (attempts >= 0),
    status        TEXT NOT NULL CHECK (status IN ('retrying', 'parked', 'recovered')),
    next_retry_at TIMESTAMPTZ,
    received_at   TIMESTAMPTZ NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_error_at TIMESTAMPTZ,
    CHECK ((status = 'parked' AND next_retry_at IS NULL) OR
           (status = 'retrying' AND next_retry_at IS NOT NULL) OR
           (status = 'recovered' AND next_retry_at IS NULL))
);

CREATE INDEX unprocessed_messages_status_idx ON unprocessed_messages (status, source);
CREATE INDEX unprocessed_messages_partition_idx ON unprocessed_messages (partition_id);
