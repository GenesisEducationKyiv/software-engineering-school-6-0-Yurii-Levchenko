package orchestrator

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jmoiron/sqlx"
)

// Store is the data access for the `saga` table.
type Store struct {
	db *sqlx.DB
}

// NewStore creates a saga Store.
func NewStore(db *sqlx.DB) *Store {
	return &Store{db: db}
}

// CreateInTx inserts a saga row using the given execer, so it commits in the
// same transaction as the subscription and outbox writes (step T1).
func (s *Store) CreateInTx(ctx context.Context, ext sqlx.ExtContext, sg *Saga) error {
	const q = `INSERT INTO saga (id, subscription_id, email, repo, state)
	           VALUES ($1, $2, $3, $4, $5)`
	if _, err := ext.ExecContext(ctx, q, sg.ID, sg.SubscriptionID, sg.Email, sg.Repo, sg.State); err != nil {
		return fmt.Errorf("insert saga: %w", err)
	}
	return nil
}

// GetByID returns the saga with the given id, or nil if it does not exist.
func (s *Store) GetByID(ctx context.Context, id string) (*Saga, error) {
	var sg Saga
	const q = `SELECT * FROM saga WHERE id = $1`
	if err := s.db.GetContext(ctx, &sg, q, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get saga %s: %w", id, err)
	}
	return &sg, nil
}

// UpdateState moves the saga to a new state, stamping updated_at and recording lastErr when non-empty.
func (s *Store) UpdateState(ctx context.Context, id string, state State, lastErr string) error {
	var le sql.NullString
	if lastErr != "" {
		le = sql.NullString{String: lastErr, Valid: true}
	}
	const q = `UPDATE saga SET state = $1, last_error = $2, updated_at = NOW() WHERE id = $3`
	if _, err := s.db.ExecContext(ctx, q, state, le, id); err != nil {
		return fmt.Errorf("update saga %s state: %w", id, err)
	}
	return nil
}
