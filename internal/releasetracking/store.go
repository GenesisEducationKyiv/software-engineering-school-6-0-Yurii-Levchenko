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
	LastReleaseAt *time.Time `db:"last_release_at" json:"last_release_at"`
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

// RegisterRepo ensures a tracking row exists for the repo
func (s *Store) RegisterRepo(repo string) error {
	query := `INSERT INTO repositories (repo) VALUES ($1) ON CONFLICT (repo) DO NOTHING`
	_, err := s.db.Exec(query, repo)
	return err
}

// TouchLastChecked stamps the moment the scanner actually checked the repo
// against GitHub. Upsert so a missing row self-heals.
func (s *Store) TouchLastChecked(repo string) error {
	query := `INSERT INTO repositories (repo, last_checked_at) VALUES ($1, NOW())
	          ON CONFLICT (repo) DO UPDATE SET last_checked_at = NOW()`
	_, err := s.db.Exec(query, repo)
	return err
}

// RecordRelease stores the newly detected release tag and stamps last_release_at.
func (s *Store) RecordRelease(repo, tag string) error {
	query := `INSERT INTO repositories (repo, last_seen_tag, last_release_at, last_checked_at)
	          VALUES ($1, $2, NOW(), NOW())
	          ON CONFLICT (repo) DO UPDATE SET last_seen_tag = $2, last_release_at = NOW(), last_checked_at = NOW()`
	_, err := s.db.Exec(query, repo, tag)
	return err
}
