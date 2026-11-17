-- Extend D-DLQ without modifying an already-applied migration.
ALTER TABLE unprocessed_messages
    DROP CONSTRAINT unprocessed_messages_source_check,
    ADD CONSTRAINT unprocessed_messages_source_check
        CHECK (source IN ('BMKG', 'PVMBG', 'SATELIT', 'SENSOR_PANTAI', 'FIELD_REPORT'));
