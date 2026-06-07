package subscription

import (
	"database/sql"
	"errors"

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
	query := `UPDATE subscriptions SET confirmed = true WHERE token = $1`
	_, err := s.db.Exec(query, token)
	return err
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
