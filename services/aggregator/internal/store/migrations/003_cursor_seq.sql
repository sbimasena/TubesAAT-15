-- Units can disagree on a cursor (one was down during a save); LoadCursor keeps the
-- copy with the highest (epoch, seq) instead of trusting any node's clock.
ALTER TABLE ingest_cursor ADD COLUMN seq BIGINT NOT NULL DEFAULT 0;
