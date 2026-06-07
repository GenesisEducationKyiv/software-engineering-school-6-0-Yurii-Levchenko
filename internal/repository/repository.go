package repository

import (
	"database/sql"
	"errors"
	"time"

	"github-release-notifier/internal/model"

	"github.com/jmoiron/sqlx"
)

// Repository owns release-tracking data — the `repositories` table.
type Repository struct {
	db *sqlx.DB
}

// New creates a new Repository instance.
func New(db *sqlx.DB) *Repository {
	return &Repository{db: db}
}

// GetRepoTracking returns the tracking record for a repo (last_seen_tag).
func (r *Repository) GetRepoTracking(repo string) (*model.Repository, error) {
	var repoRecord model.Repository
	query := `SELECT * FROM repositories WHERE repo = $1`
	err := r.db.Get(&repoRecord, query, repo)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &repoRecord, err
}

// UpsertRepoTracking creates or updates the tracking record for a repo.
func (r *Repository) UpsertRepoTracking(repo, lastSeenTag string) error {
	now := time.Now()
	query := `INSERT INTO repositories (repo, last_seen_tag, last_checked_at)
	          VALUES ($1, $2, $3)
	          ON CONFLICT (repo) DO UPDATE SET last_seen_tag = $2, last_checked_at = $3`
	_, err := r.db.Exec(query, repo, lastSeenTag, now)
	return err
}
