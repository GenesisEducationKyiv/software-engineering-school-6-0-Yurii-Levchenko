//go:build integration

// Package integration contains black-box HTTP tests against the full
// application stack: real Gin router, real service layer, real Postgres
// (running in a testcontainer), with the GitHub API and SMTP notifier
// stubbed out via fakes (per Lecture 6 "Functional Testing": mock what
// you cannot control — external APIs and email).
//
// Run with:
//
//	go test -tags=integration -v ./internal/integration/...
//
// Requires Docker to be running.
package integration

import (
	"context"
	"fmt"
	"log"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github-release-notifier/internal/app"
	"github-release-notifier/internal/repository"
	"github-release-notifier/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Package-level state, initialised once in TestMain and reused by all tests.
// Tests are responsible for resetting per-test state via newTestApp().
var (
	testDB *sqlx.DB
)

// TestMain spins up a Postgres container, runs migrations against it,
// runs all tests, and tears the container down.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)

	ctx := context.Background()

	pgContainer, err := postgres.Run(ctx,
		"postgres:16-alpine",
		postgres.WithDatabase("notifier"),
		postgres.WithUsername("postgres"),
		postgres.WithPassword("postgres"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		log.Fatalf("start postgres container: %v", err)
	}

	dsn, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		log.Fatalf("get postgres connection string: %v", err)
	}

	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		log.Fatalf("connect to postgres: %v", err)
	}
	testDB = db

	if err := runMigrations(db); err != nil {
		log.Fatalf("run migrations: %v", err)
	}

	code := m.Run()

	_ = db.Close()
	_ = pgContainer.Terminate(ctx)
	os.Exit(code)
}

// runMigrations applies the up migration SQL. We read the file directly
// instead of using golang-migrate because the path resolution from inside
// a test package is awkward and this migration is a single file.
func runMigrations(db *sqlx.DB) error {
	sqlBytes, err := os.ReadFile("../../migrations/000001_init.up.sql")
	if err != nil {
		return fmt.Errorf("read migration file: %w", err)
	}
	if _, err := db.Exec(string(sqlBytes)); err != nil {
		return fmt.Errorf("execute migration: %w", err)
	}
	return nil
}

// testApp bundles a running HTTP test server with the fakes wired into
// the service layer, plus a handle to the shared DB so tests can assert
// persisted state directly.
type testApp struct {
	server   *httptest.Server
	github   *fakeGitHubClient
	notifier *fakeNotifier
	db       *sqlx.DB
}

// newTestApp truncates the DB to a clean slate, builds a fresh service
// graph wired to fakes, and starts an in-process HTTP test server.
//
// Setup (per Lecture 6 test structure: Setup → Action → Assertion → Teardown).
// The teardown is registered via t.Cleanup.
func newTestApp(t *testing.T) *testApp {
	t.Helper()

	if _, err := testDB.Exec(
		`TRUNCATE TABLE subscriptions, repositories RESTART IDENTITY`,
	); err != nil {
		t.Fatalf("truncate tables: %v", err)
	}

	gh := &fakeGitHubClient{repos: map[string]bool{}}
	notif := &fakeNotifier{}

	repo := repository.New(testDB)
	svc := service.New(repo, repo, gh, notif, "http://test.local")

	// apiKey="" disables auth middleware; staticIndexPath="" skips the "/" route.
	router := app.BuildRouter(svc, "", "")
	server := httptest.NewServer(router)

	t.Cleanup(func() {
		server.Close()
	})

	return &testApp{
		server:   server,
		github:   gh,
		notifier: notif,
		db:       testDB,
	}
}

// --- Fakes for external dependencies ---

// fakeGitHubClient implements service.GitHubClient.
// Tests configure which repos "exist" via the repos map.
type fakeGitHubClient struct {
	repos map[string]bool // key: "owner/repo" → exists
	err   error           // if set, all calls return this error
}

func (f *fakeGitHubClient) CheckRepoExists(ctx context.Context, owner, repo string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	return f.repos[owner+"/"+repo], nil
}

// sentEmail records a single notifier call for later assertion.
type sentEmail struct {
	To         string
	ConfirmURL string
}

// fakeNotifier implements service.EmailNotifier.
// It records every call so tests can verify what would have been sent.
type fakeNotifier struct {
	sent []sentEmail
	err  error // if set, every call returns this error
}

func (f *fakeNotifier) SendConfirmationEmail(to, confirmURL string) error {
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, sentEmail{To: to, ConfirmURL: confirmURL})
	return nil
}
