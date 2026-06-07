package releasetracking

import (
	"database/sql"
	"errors"
	"time"

	"github.com/jmoiron/sqlx"
)

// Repository is the release-tracking entity: the last release tag seen for a repo.
type Repository struct {
	ID            int        `db:"id" json:"id"`
	Repo          string     `db:"repo" json:"repo"`
	LastSeenTag   string     `db:"last_seen_tag" json:"last_seen_tag"`
	LastCheckedAt *time.Time `db:"last_checked_at" json:"last_checked_at"`
}

// Store owns release-tracking data — the `repositories` table.
type Store struct {
	db *sqlx.DB
}

// NewStore creates a release-tracking Store.
func NewStore(db *sqlx.DB) *Store {
	return &Store{db: db}
}

func (s *Store) GetRepoTracking(repo string) (*Repository, error) {
	var rec Repository
	query := `SELECT * FROM repositories WHERE repo = $1`
	err := s.db.Get(&rec, query, repo)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &rec, err
}

func (s *Store) UpsertRepoTracking(repo, lastSeenTag string) error {
	now := time.Now()
	query := `INSERT INTO repositories (repo, last_seen_tag, last_checked_at)
	          VALUES ($1, $2, $3)
	          ON CONFLICT (repo) DO UPDATE SET last_seen_tag = $2, last_checked_at = $3`
	_, err := s.db.Exec(query, repo, lastSeenTag, now)
	return err
}
