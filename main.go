package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github-release-notifier/internal/app"
	"github-release-notifier/internal/cache"
	"github-release-notifier/internal/config"
	"github-release-notifier/internal/githubgateway"
	"github-release-notifier/internal/logging"
	"github-release-notifier/internal/notification"
	"github-release-notifier/internal/orchestrator"
	"github-release-notifier/internal/outbox"
	"github-release-notifier/internal/releasetracking"
	"github-release-notifier/internal/subscription"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jmoiron/sqlx"
	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
)

func main() {
	// Keep main() thin: all logic lives in run() so that deferred cleanup
	// (db.Close, redisCache.Close, cancel) actually executes.
	if err := run(); err != nil {
		slog.Error("application exited with error", "err", err)
		// os.Exit skips defers, so we only ever call it here, after run() has returned and its defers have run.
		os.Exit(1)
	}
}

// run wires dependencies, starts the server + scanner, and blocks until a
// shutdown signal or a fatal server error
func run() error {
	// Load .env file (ignored in Docker where env vars are set directly)
	_ = godotenv.Load()

	// Load configuration from env variables
	cfg := config.Load()

	// Configure structured JSON logging before anything else logs
	logging.Setup(cfg.LogLevel)

	// --- DB Connection ---
	slog.Info("Connecting to database")
	db, err := sqlx.Connect("postgres", cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer db.Close()
	slog.Info("Database connected")

	// --- Run Migrations ---
	slog.Info("Running database migrations")
	if err := runMigrations(cfg.DatabaseURL); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	slog.Info("Migrations completed")

	// --- Redis Cache ---
	slog.Info("Connecting to Redis")
	ttl := time.Duration(cfg.CacheTTLSeconds) * time.Second
	redisCache, err := cache.New(cfg.RedisURL, ttl)
	if err != nil {
		slog.Warn("Redis not available, running without cache", "err", err)
		redisCache = nil
	} else {
		defer redisCache.Close()
		slog.Info("Redis connected", "ttl", ttl)
	}

	// --- Initialize Dependencies ---
	trackStore := releasetracking.NewStore(db)
	subStore := subscription.NewStore(db)
	ghClient := githubgateway.New(cfg.GitHubToken)

	// wrap GitHub client with Redis cache if available
	var ghService subscription.GitHubClient
	var scannerGH releasetracking.ReleaseChecker
	if redisCache != nil {
		cachedGH := githubgateway.NewCachedClient(ghClient, redisCache)
		ghService = cachedGH
		scannerGH = cachedGH
	} else {
		ghService = ghClient
		scannerGH = ghClient
	}

	// Notifications are published to RabbitMQ; the notifier service consumes
	// them. Same EmailNotifier/ReleaseNotifier interfaces — callers unchanged.
	emailNotifier, err := notification.NewBrokerPublisher(cfg.RabbitMQURL)
	if err != nil {
		return fmt.Errorf("connect to broker: %w", err)
	}
	defer emailNotifier.Close()

	// Saga orchestrator: Subscribe runs as a transactional saga — step T1 writes
	// the subscription, the saga record, and the outbox command in one transaction.
	outboxStore := outbox.NewStore(db)
	sagaStore := orchestrator.NewStore(db)
	orch := orchestrator.New(db, subStore, sagaStore, outboxStore)

	svc := subscription.New(subStore, trackStore, ghService, orch, cfg.BaseURL)

	// --- Start Background Scanner with context for graceful shutdown ---
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	releaseScanner := releasetracking.New(svc, trackStore, scannerGH, emailNotifier, cfg.ScanIntervalSecs, cfg.BaseURL)
	go releaseScanner.Start(ctx)

	// --- Start the saga reply consumer FIRST ---
	// It declares the saga.replies queue on connect; starting it before the relay
	// narrows the startup window where an early reply could be published to a
	// not-yet-declared queue and dropped on a fresh broker (review: k1llzers).
	// Reconnect loop keeps a RabbitMQ blip from taking down the HTTP server.
	replyConsumer := orchestrator.NewReplyConsumer(sagaStore, subStore)
	go func() {
		for {
			if err := replyConsumer.Run(ctx, cfg.RabbitMQURL); err != nil {
				slog.Error("Saga reply consumer stopped, retrying", "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
		}
	}()

	// --- Start the transactional-outbox relay ---
	// Publishes notification commands written to the outbox table to RabbitMQ.
	// Idle until the orchestrator (HW9) starts writing rows; shares ctx with the
	// scanner so it stops on graceful shutdown.
	outboxRelay := outbox.NewRelay(
		outboxStore,
		emailNotifier,
		time.Duration(cfg.OutboxPollIntervalMs)*time.Millisecond,
	)
	go outboxRelay.Run(ctx)

	// --- Setup Router ---
	// Router wiring lives in internal/app so that integration tests can
	// build the same router without spinning up main().
	router := app.BuildRouter(svc, cfg.APIKey, "./static/index.html")

	// --- Graceful Shutdown ---
	// Create HTTP server manually (instead of router.Run) so we can shut it down gracefully
	srv := &http.Server{
		Addr:              ":" + cfg.AppPort,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Start server in a goroutine; a fatal listen error is surfaced via the
	// channel so run() can return it (and defers can clean up).
	serverErr := make(chan error, 1)
	go func() {
		slog.Info("Server starting", "port", cfg.AppPort)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	// Block until either the server fails or we get a shutdown signal.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-serverErr:
		return fmt.Errorf("http server: %w", err)
	case <-quit:
		slog.Info("Shutting down gracefully")
	}

	// Stop scanner
	cancel()

	// Give the HTTP server 5 seconds to finish ongoing requests
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("server shutdown: %w", err)
	}
	slog.Info("Server stopped")
	return nil
}

// applies all pending SQL migrations
func runMigrations(dbURL string) error {
	m, err := migrate.New("file://migrations", dbURL)
	if err != nil {
		return err
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}
