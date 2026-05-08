CREATE TABLE IF NOT EXISTS subscriptions (
    id SERIAL PRIMARY KEY,
    email VARCHAR(255) NOT NULL,
    repo VARCHAR(255) NOT NULL,
    token VARCHAR(255) UNIQUE NOT NULL,
    confirmed BOOLEAN DEFAULT FALSE,
    created_at TIMESTAMP DEFAULT NOW(),
    UNIQUE(email, repo)
);

CREATE TABLE IF NOT EXISTS repositories (
    id SERIAL PRIMARY KEY,
    repo VARCHAR(255) UNIQUE NOT NULL,
    last_seen_tag VARCHAR(255) DEFAULT '',
    last_checked_at TIMESTAMP
);
