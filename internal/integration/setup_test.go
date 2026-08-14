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
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github-release-notifier/internal/app"
	"github-release-notifier/internal/orchestrator"
	"github-release-notifier/internal/outbox"
	"github-release-notifier/internal/releasetracking"
	"github-release-notifier/internal/subscription"

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

// testClient is shared by all request helpers; its timeout makes a hung
// request fail fast (5s) instead of blocking until the global `go test`
// timeout (10 min by default).
var testClient = &http.Client{Timeout: 5 * time.Second}

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

// runMigrations applies every *.up.sql migration in lexical order. The
// zero-padded numeric prefix (000001_, 000002_, ...) makes lexical order ==
// chronological order, so we pick up new migrations automatically. We read
// the files directly instead of using golang-migrate because the container
// is recreated each run, so version tracking and down-migrations add nothing.
func runMigrations(db *sqlx.DB) error {
	files, err := filepath.Glob("../../migrations/*.up.sql")
	if err != nil {
		return fmt.Errorf("glob migrations: %w", err)
	}
	if len(files) == 0 {
		return fmt.Errorf("no migration files found in ../../migrations")
	}
	sort.Strings(files)

	for _, f := range files {
		sqlBytes, err := os.ReadFile(f)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", f, err)
		}
		if _, err := db.Exec(string(sqlBytes)); err != nil {
			return fmt.Errorf("execute migration %s: %w", f, err)
		}
	}
	return nil
}

// testApp bundles a running HTTP test server with the fakes wired into
// the service layer, plus a handle to the shared DB so tests can assert
// persisted state directly.
type testApp struct {
	server *httptest.Server
	github *fakeGitHubClient
	db     *sqlx.DB
}

// newTestApp truncates the DB to a clean slate, builds a fresh service
// graph wired to fakes, and starts an in-process HTTP test server.
//
// Setup (per Lecture 6 test structure: Setup → Action → Assertion → Teardown).
// The teardown is registered via t.Cleanup.
func newTestApp(t *testing.T) *testApp {
	t.Helper()
	// apiKey="" disables the auth middleware — the common case for most tests.
	return newTestAppWithKey(t, "")
}

// newTestAppWithKey is like newTestApp but wires the auth middleware with the
// given API key, so tests can exercise the authenticated path (missing /
// wrong / correct key). apiKey="" disables auth.
func newTestAppWithKey(t *testing.T, apiKey string) *testApp {
	t.Helper()

	if _, err := testDB.Exec(
		`TRUNCATE TABLE subscriptions, repositories, saga, outbox RESTART IDENTITY`,
	); err != nil {
		t.Fatalf("truncate tables: %v", err)
	}

	gh := &fakeGitHubClient{repos: map[string]bool{}}

	subStore := subscription.NewStore(testDB)
	trackStore := releasetracking.NewStore(testDB)
	sagaStore := orchestrator.NewStore(testDB)
	outboxStore := outbox.NewStore(testDB)
	orch := orchestrator.New(testDB, subStore, sagaStore, outboxStore, nil)
	svc := subscription.New(subStore, trackStore, gh, orch, "http://test.local")

	// staticIndexPath="" skips the "/" route (the file is not at a predictable
	// relative path from the test package).
	router := app.BuildRouter(svc, apiKey, "")
	server := httptest.NewServer(router)

	t.Cleanup(func() {
		server.Close()
	})

	return &testApp{
		server: server,
		github: gh,
		db:     testDB,
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
