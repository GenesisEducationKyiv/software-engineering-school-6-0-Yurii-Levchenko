package subscription

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jmoiron/sqlx"
)

// Store is the subscription domain's data access — it owns the `subscriptions` table.
type Store struct {
	db *sqlx.DB
}

// NewStore creates a subscription Store.
func NewStore(db *sqlx.DB) *Store {
	return &Store{db: db}
}

func (s *Store) CreateSubscription(sub *Subscription) error {
	query := `INSERT INTO subscriptions (email, repo, token, confirmed)
	          VALUES ($1, $2, $3, $4)`
	_, err := s.db.Exec(query, sub.Email, sub.Repo, sub.Token, sub.Confirmed)
	return err
}

// CreateInTx inserts a pending subscription within the given transaction and
// returns its new id. The saga orchestrator uses it so the subscription, saga,
// and outbox rows all commit atomically (step T1). ext is a *sqlx.Tx in
// production; *sqlx.DB also satisfies it.
func (s *Store) CreateInTx(ctx context.Context, ext sqlx.ExtContext, email, repo, token string) (int, error) {
	const q = `INSERT INTO subscriptions (email, repo, token, confirmed)
	           VALUES ($1, $2, $3, false) RETURNING id`
	var id int
	if err := ext.QueryRowxContext(ctx, q, email, repo, token).Scan(&id); err != nil {
		return 0, err
	}
	return id, nil
}

func (s *Store) GetSubscriptionByToken(token string) (*Subscription, error) {
	var sub Subscription
	query := `SELECT * FROM subscriptions WHERE token = $1`
	err := s.db.Get(&sub, query, token)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &sub, err
}

func (s *Store) GetSubscriptionByEmailAndRepo(email, repo string) (*Subscription, error) {
	var sub Subscription
	query := `SELECT * FROM subscriptions WHERE email = $1 AND repo = $2`
	err := s.db.Get(&sub, query, email, repo)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &sub, err
}

func (s *Store) ConfirmSubscription(token string) error {
	query := `UPDATE subscriptions SET confirmed = true, status = 'confirmed' WHERE token = $1`
	_, err := s.db.Exec(query, token)
	return err
}

// MarkFailed records that the subscription's confirmation email could not be
// sent (saga compensation C1). We mark, not delete — the row stays as an
// auditable terminal state and a future re-subscribe can reactivate it.
func (s *Store) MarkFailed(ctx context.Context, id int) error {
	const q = `UPDATE subscriptions SET status = 'failed' WHERE id = $1`
	res, err := s.db.ExecContext(ctx, q, id)
	if err != nil {
		return fmt.Errorf("mark subscription %d failed: %w", id, err)
	}
	// Compensation must actually hit a row; 0 rows means a wrong/gone id — surface
	// it instead of a silent no-op (review: k1llzers).
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("mark subscription %d failed: no rows updated", id)
	}
	return nil
}

func (s *Store) DeleteSubscription(token string) error {
	query := `DELETE FROM subscriptions WHERE token = $1`
	_, err := s.db.Exec(query, token)
	return err
}

func (s *Store) GetActiveSubscriptionsByEmail(email string) ([]Subscription, error) {
	var subs []Subscription
	query := `SELECT * FROM subscriptions WHERE email = $1 AND confirmed = true`
	err := s.db.Select(&subs, query, email)
	return subs, err
}

func (s *Store) GetActiveRepos() ([]string, error) {
	var repos []string
	query := `SELECT DISTINCT repo FROM subscriptions WHERE confirmed = true`
	err := s.db.Select(&repos, query)
	return repos, err
}

func (s *Store) GetSubscribersByRepo(repo string) ([]Subscription, error) {
	var subs []Subscription
	query := `SELECT * FROM subscriptions WHERE repo = $1 AND confirmed = true`
	err := s.db.Select(&subs, query, repo)
	return subs, err
}
