CREATE TABLE IF NOT EXISTS outbox (
    id BIGSERIAL PRIMARY KEY,
    saga_id UUID,
    routing_key VARCHAR(255) NOT NULL,
    message_id VARCHAR(255) NOT NULL,
    payload JSONB NOT NULL,
    created_at TIMESTAMP DEFAULT NOW(),
    published_at TIMESTAMP
);

-- The relay only ever reads not-yet-published rows, oldest first. A partial
-- index keeps that scan tiny: published rows drop out of the index entirely.
CREATE INDEX IF NOT EXISTS idx_outbox_unpublished ON outbox (id) WHERE published_at IS NULL;

-- Each command is enqueued exactly once per saga (message_id = "confirm:{token}",
-- token is a fresh UUID). A unique index turns an accidental double-enqueue into a
-- hard error at INSERT instead of a duplicate publish downstream (review: k1llzers).
CREATE UNIQUE INDEX IF NOT EXISTS idx_outbox_message_id ON outbox (message_id);
