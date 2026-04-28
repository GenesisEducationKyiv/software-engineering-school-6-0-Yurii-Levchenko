package repository

import (
	"database/sql"
	"github-release-notifier/internal/model"
	"time"

	"github.com/jmoiron/sqlx"
)

// Repository handles all database operations
type Repository struct {
	db *sqlx.DB
}

// New creates a new Repository instance
func New(db *sqlx.DB) *Repository {
	return &Repository{db: db}
}

// --- Subscription queries ---

// CreateSubscription inserts a new subscription
func (r *Repository) CreateSubscription(sub *model.Subscription) error {
	query := `INSERT INTO subscriptions (email, repo, token, confirmed)
	          VALUES ($1, $2, $3, $4)`
	_, err := r.db.Exec(query, sub.Email, sub.Repo, sub.Token, sub.Confirmed)
	return err
}

// GetSubscriptionByToken finds a subscription by its confirmation/unsubscribe token
func (r *Repository) GetSubscriptionByToken(token string) (*model.Subscription, error) {
	var sub model.Subscription
	query := `SELECT * FROM subscriptions WHERE token = $1`
	err := r.db.Get(&sub, query, token)
	if err == sql.ErrNoRows {
		return nil, nil // not found — return nil, no error
	}
	return &sub, err
}

// GetSubscriptionByEmailAndRepo checks if a subscription already exists
func (r *Repository) GetSubscriptionByEmailAndRepo(email, repo string) (*model.Subscription, error) {
	var sub model.Subscription
	query := `SELECT * FROM subscriptions WHERE email = $1 AND repo = $2`
	err := r.db.Get(&sub, query, email, repo)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &sub, err
}

// ConfirmSubscription sets confirmed=true for a subscription
func (r *Repository) ConfirmSubscription(token string) error {
	query := `UPDATE subscriptions SET confirmed = true WHERE token = $1`
	_, err := r.db.Exec(query, token)
	return err
}

// DeleteSubscription removes a subscription by token
func (r *Repository) DeleteSubscription(token string) error {
	query := `DELETE FROM subscriptions WHERE token = $1`
	_, err := r.db.Exec(query, token)
	return err
}

// GetActiveSubscriptionsByEmail returns all confirmed subscriptions for an email
func (r *Repository) GetActiveSubscriptionsByEmail(email string) ([]model.Subscription, error) {
	var subs []model.Subscription
	query := `SELECT * FROM subscriptions WHERE email = $1 AND confirmed = true`
	err := r.db.Select(&subs, query, email)
	return subs, err
}

// GetActiveRepos returns all unique repos that have at least one confirmed subscription
func (r *Repository) GetActiveRepos() ([]string, error) {
	var repos []string
	query := `SELECT DISTINCT repo FROM subscriptions WHERE confirmed = true`
	err := r.db.Select(&repos, query)
	return repos, err
}

// GetSubscribersByRepo returns all confirmed subscribers for a given repo
func (r *Repository) GetSubscribersByRepo(repo string) ([]model.Subscription, error) {
	var subs []model.Subscription
	query := `SELECT * FROM subscriptions WHERE repo = $1 AND confirmed = true`
	err := r.db.Select(&subs, query, repo)
	return subs, err
}

// --- Repository (release tracking) queries ---

// GetRepoTracking returns the tracking record for a repo (last_seen_tag)
func (r *Repository) GetRepoTracking(repo string) (*model.Repository, error) {
	var repoRecord model.Repository
	query := `SELECT * FROM repositories WHERE repo = $1`
	err := r.db.Get(&repoRecord, query, repo)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &repoRecord, err
}

// UpsertRepoTracking creates or updates the tracking record for a repo
func (r *Repository) UpsertRepoTracking(repo, lastSeenTag string) error {
	now := time.Now()
	query := `INSERT INTO repositories (repo, last_seen_tag, last_checked_at)
	          VALUES ($1, $2, $3)
	          ON CONFLICT (repo) DO UPDATE SET last_seen_tag = $2, last_checked_at = $3`
	_, err := r.db.Exec(query, repo, lastSeenTag, now)
	return err
}
