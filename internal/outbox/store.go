// Package outbox implements the transactional-outbox pattern: notification
// commands are written to the `outbox` table in the same local transaction as
// the business data, and a background Relay publishes them to the broker. This
// removes the dual-write gap between "write to DB" and "publish to RabbitMQ"
// (see ADR-010).
package outbox

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
)

// Message is one row of the outbox table — a notification command waiting to be
// published to the broker.
type Message struct {
	ID          int64      `db:"id"`
	SagaID      *string    `db:"saga_id"`
	RoutingKey  string     `db:"routing_key"`
	MessageID   string     `db:"message_id"`
	Payload     []byte     `db:"payload"`
	CreatedAt   time.Time  `db:"created_at"`
	PublishedAt *time.Time `db:"published_at"`
}

// execer is the subset of *sqlx.DB / *sqlx.Tx that Enqueue needs. Taking it as a
// parameter lets the caller run the insert inside its own transaction, so the
// outbox row commits atomically with the business write — the whole point of the
// transactional-outbox pattern. *sqlx.DB satisfies it for standalone writes.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Store is the data access for the outbox table.
type Store struct {
	db *sqlx.DB
}

// NewStore creates an outbox Store.
func NewStore(db *sqlx.DB) *Store {
	return &Store{db: db}
}

// Enqueue inserts one outbox row using the given execer (a *sqlx.Tx to join the
// caller's transaction, or a *sqlx.DB for a standalone write). An empty sagaID is
// stored as NULL.
func (s *Store) Enqueue(ctx context.Context, ex execer, sagaID, routingKey, messageID string, payload []byte) error {
	var sid sql.NullString
	if sagaID != "" {
		sid = sql.NullString{String: sagaID, Valid: true}
	}
	const q = `INSERT INTO outbox (saga_id, routing_key, message_id, payload)
	           VALUES ($1, $2, $3, $4)`
	if _, err := ex.ExecContext(ctx, q, sid, routingKey, messageID, payload); err != nil {
		return fmt.Errorf("enqueue outbox message: %w", err)
	}
	return nil
}

// FetchUnpublished returns up to limit not-yet-published messages, oldest first.
func (s *Store) FetchUnpublished(ctx context.Context, limit int) ([]Message, error) {
	const q = `SELECT id, saga_id, routing_key, message_id, payload, created_at, published_at
	           FROM outbox
	           WHERE published_at IS NULL
	           ORDER BY id
	           LIMIT $1`
	var msgs []Message
	if err := s.db.SelectContext(ctx, &msgs, q, limit); err != nil {
		return nil, fmt.Errorf("fetch unpublished outbox messages: %w", err)
	}
	return msgs, nil
}

// MarkPublished stamps published_at = NOW() for the given message id.
func (s *Store) MarkPublished(ctx context.Context, id int64) error {
	const q = `UPDATE outbox SET published_at = NOW() WHERE id = $1`
	if _, err := s.db.ExecContext(ctx, q, id); err != nil {
		return fmt.Errorf("mark outbox message %d published: %w", id, err)
	}
	return nil
}

// Requeue resets a saga's outbox messages back to unpublished so the relay
// re-publishes them. Used by the resume-sweeper to re-drive a stuck saga; the
// consumer dedups on MessageId, so a re-publish does not send a duplicate email.
func (s *Store) Requeue(ctx context.Context, sagaID string) error {
	const q = `UPDATE outbox SET published_at = NULL WHERE saga_id = $1`
	if _, err := s.db.ExecContext(ctx, q, sagaID); err != nil {
		return fmt.Errorf("requeue outbox for saga %s: %w", sagaID, err)
	}
	return nil
}
