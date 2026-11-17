-- D-FR (dibekukan 12 Nov, owner B + C): Field Team reports accepted by the
-- Client API (202 after broker confirm on the field-reports quorum queue).
-- The processor is a competing consumer on every Aggregator replica; the
-- idempotent insert (PRIMARY KEY report_id + ON CONFLICT DO NOTHING) turns
-- at-least-once redelivery into effectively-once processing. The table is
-- created identically on every unit; rows are routed by report_id
-- (PartitionOf) with RF=2 copies, like hazard_events.
CREATE TABLE field_reports (
    report_id      TEXT PRIMARY KEY CHECK (report_id <> ''),
    partition_id   SMALLINT NOT NULL CHECK (partition_id BETWEEN 0 AND 23),
    hazard_id      TEXT,
    note           TEXT NOT NULL CHECK (octet_length(note) BETWEEN 1 AND 4000),
    area_name      TEXT CHECK (area_name IS NULL OR octet_length(area_name) <= 200),
    occurred_at    TIMESTAMPTZ,
    reporter       TEXT NOT NULL CHECK (reporter <> ''),
    received_at    TIMESTAMPTZ NOT NULL,
    correlation_id TEXT CHECK (correlation_id IS NULL OR
                               length(correlation_id) BETWEEN 1 AND 128),
    processed_at   TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX field_reports_received_at_idx ON field_reports (received_at DESC, report_id);
CREATE INDEX field_reports_partition_idx ON field_reports (partition_id);
