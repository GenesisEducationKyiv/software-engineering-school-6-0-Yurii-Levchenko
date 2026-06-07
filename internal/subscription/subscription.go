package subscription

import "time"

// Subscription is the subscription domain's entity — a row of the `subscriptions` table.
type Subscription struct {
	ID        int       `db:"id" json:"id"`
	Email     string    `db:"email" json:"email"`
	Repo      string    `db:"repo" json:"repo"`
	Token     string    `db:"token" json:"token"`
	Confirmed bool      `db:"confirmed" json:"confirmed"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
}
