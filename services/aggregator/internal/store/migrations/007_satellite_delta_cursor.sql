-- The satellite checkpoint is a full base plus a small delta row (see satellite.deltaSource).
ALTER TABLE ingest_cursor
    DROP CONSTRAINT ingest_cursor_source_check,
    ADD CONSTRAINT ingest_cursor_source_check
        CHECK (source IN ('BMKG', 'PVMBG', 'SATELIT', 'SATELIT_DELTA', 'SENSOR_PANTAI'));
