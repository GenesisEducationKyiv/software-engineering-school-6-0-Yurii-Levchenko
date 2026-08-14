CREATE TABLE IF NOT EXISTS saga (
    id UUID PRIMARY KEY,
    subscription_id INTEGER NOT NULL,
    email VARCHAR(255) NOT NULL,
    repo VARCHAR(255) NOT NULL,
    state VARCHAR(32) NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    last_error TEXT,
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

-- The resume sweeper (later PR) queries non-terminal sagas by state.
CREATE INDEX IF NOT EXISTS idx_saga_state ON saga (state);
