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
