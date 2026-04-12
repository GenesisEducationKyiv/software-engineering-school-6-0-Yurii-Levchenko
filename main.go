package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github-release-notifier/internal/cache"
	"github-release-notifier/internal/config"
	"github-release-notifier/internal/github"
	"github-release-notifier/internal/handler"
	"github-release-notifier/internal/notifier"
	"github-release-notifier/internal/repository"
	"github-release-notifier/internal/scanner"
	"github-release-notifier/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jmoiron/sqlx"
	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
)

func main() {
	// Load .env file (ignored in Docker where env vars are set directly)
	_ = godotenv.Load()

	// Load configuration from env variables
	cfg := config.Load()

	// --- DB Connection ---
	log.Println("Connecting to database...")
	db, err := sqlx.Connect("postgres", cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer db.Close()
	log.Println("Database connected successfully")

	// --- Run Migrations ---
	log.Println("Running database migrations...")
	if err := runMigrations(cfg.DatabaseURL); err != nil {
		log.Fatalf("Failed to run migrations: %v", err)
	}
	log.Println("Migrations completed")

	// --- Redis Cache ---
	log.Println("Connecting to Redis...")
	ttl := time.Duration(cfg.CacheTTLSeconds) * time.Second
	redisCache, err := cache.New(cfg.RedisURL, ttl)
	if err != nil {
		log.Printf("WARNING: Redis not available, running without cache: %v", err)
		redisCache = nil
	} else {
		defer redisCache.Close()
		log.Printf("Redis connected (TTL: %v)", ttl)
	}

	// --- Initialize Dependencies ---
	repo := repository.New(db)
	ghClient := github.New(cfg.GitHubToken)

	// wrap GitHub client with Redis cache if available
	var ghService service.GitHubClient
	var scannerGH scanner.ReleaseChecker
	if redisCache != nil {
		cachedGH := github.NewCachedClient(ghClient, redisCache)
		ghService = cachedGH
		scannerGH = cachedGH
	} else {
		ghService = ghClient
		scannerGH = ghClient
	}

	emailNotifier := notifier.New(cfg.SMTPHost, cfg.SMTPPort, cfg.SMTPUser, cfg.SMTPPass, cfg.SMTPFrom)
	svc := service.New(repo, ghService, emailNotifier, cfg.BaseURL)
	h := handler.New(svc)

	// --- Start Background Scanner with context for graceful shutdown ---
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	releaseScanner := scanner.New(repo, scannerGH, emailNotifier, cfg.ScanIntervalSecs, cfg.BaseURL)
	go releaseScanner.Start(ctx)

	// --- Setup Router ---
	router := gin.Default()

	// serve HTML subscription page at root
	router.StaticFile("/", "./static/index.html")

	// health check
	router.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	// API endpoints
	api := router.Group("/api")
	{
		api.POST("/subscribe", h.Subscribe)
		api.GET("/confirm/:token", h.ConfirmSubscription)
		api.GET("/unsubscribe/:token", h.Unsubscribe)
		api.GET("/subscriptions", h.GetSubscriptions)
	}

	// --- Graceful Shutdown ---
	// Create HTTP server manually (instead of router.Run) so we can shut it down gracefully
	srv := &http.Server{
		Addr:    ":" + cfg.AppPort,
		Handler: router,
	}

	// Start server in a goroutine
	go func() {
		log.Printf("Server starting on port %s", cfg.AppPort)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server failed: %v", err)
		}
	}()

	// Wait for interrupt signal (Ctrl+C or Docker stop)
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down gracefully...")

	// Stop scanner
	cancel()

	// Give the HTTP server 5 seconds to finish ongoing requests
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("Server forced to shutdown: %v", err)
	}
	log.Println("Server stopped")
}

// applies all pending SQL migrations
func runMigrations(dbURL string) error {
	m, err := migrate.New("file://migrations", dbURL)
	if err != nil {
		return err
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return err
	}
	return nil
}
