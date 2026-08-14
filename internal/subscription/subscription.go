package subscription

import "time"

// Subscription is the subscription domain's entity — a row of the `subscriptions` table.
type Subscription struct {
	ID        int    `db:"id" json:"id"`
	Email     string `db:"email" json:"email"`
	Repo      string `db:"repo" json:"repo"`
	Token     string `db:"token" json:"token"`
	Confirmed bool   `db:"confirmed" json:"confirmed"`
	// Status is the lifecycle state: pending | confirmed | failed. Exposed in the
	// API so the UI can show a subscription that could not be confirmed-emailed.
	Status    string    `db:"status" json:"status"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
}

// Subscription lifecycle statuses (the `status` column).
const (
	StatusPending   = "pending"
	StatusConfirmed = "confirmed"
	StatusFailed    = "failed"
)
