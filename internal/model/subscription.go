package model

import "time"

// Subscription represents an email subscription to a GitHub repo's releases
// The `db` tags tell sqlx how to map SQL columns to struct fields
// The `json` tags tell Gin how to serialize to JSON responses
type Subscription struct {
	ID        int       `db:"id" json:"id"`
	Email     string    `db:"email" json:"email"`
	Repo      string    `db:"repo" json:"repo"`
	Token     string    `db:"token" json:"token"`
	Confirmed bool      `db:"confirmed" json:"confirmed"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
}

// Repository tracks the last seen release tag for a GitHub repo
type Repository struct {
	ID            int        `db:"id" json:"id"`
	Repo          string     `db:"repo" json:"repo"`
	LastSeenTag   string     `db:"last_seen_tag" json:"last_seen_tag"`
	LastCheckedAt *time.Time `db:"last_checked_at" json:"last_checked_at"`
}

// SubscribeRequest is the JSON body for POST /api/subscribe, defines what the client sends to the server
type SubscribeRequest struct {
	Email string `json:"email" binding:"required,email"`
	Repo  string `json:"repo" binding:"required"`
}
