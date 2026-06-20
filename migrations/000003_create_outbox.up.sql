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
