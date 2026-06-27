ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS status VARCHAR(20) NOT NULL DEFAULT 'pending';

-- Backfill existing rows: confirmed ones are 'confirmed', the rest stay 'pending'.
UPDATE subscriptions SET status = 'confirmed' WHERE confirmed = true;
