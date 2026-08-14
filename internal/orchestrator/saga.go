// Package orchestrator coordinates the subscribe saga (ADR-010): a distributed
// transaction across the subscription module (monolith) and the notifier service.
// It owns the `saga` state table and drives each step, writing step T1
// transactionally together with the subscription and the outbox command.
package orchestrator

import "time"

// State is the lifecycle state of a subscribe saga.
type State string

const (
	// StateSubscriptionCreated: T1 committed (subscription + saga + outbox command).
	// Waiting for the notifier to report the email was sent.
	StateSubscriptionCreated State = "subscription_created"
	// StateCompleted: the confirmation email was sent (terminal, success).
	StateCompleted State = "completed"
	// StateCompensating: the email failed permanently; compensation in progress.
	StateCompensating State = "compensating"
	// StateFailed: compensated — the subscription was marked failed (terminal).
	StateFailed State = "failed"
)

// Saga is one row of the `saga` table: the persisted state of a subscribe saga.
type Saga struct {
	ID             string    `db:"id"`
	SubscriptionID int       `db:"subscription_id"`
	Email          string    `db:"email"`
	Repo           string    `db:"repo"`
	State          State     `db:"state"`
	Attempts       int       `db:"attempts"`
	LastError      *string   `db:"last_error"`
	CreatedAt      time.Time `db:"created_at"`
	UpdatedAt      time.Time `db:"updated_at"`
}
