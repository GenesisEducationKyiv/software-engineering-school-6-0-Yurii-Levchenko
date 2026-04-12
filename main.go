package main

import (
	"log"

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

	// --- Initialize Dependencies ---
	// manual dependency injection
	repo := repository.New(db)
	ghClient := github.New(cfg.GitHubToken)
	emailNotifier := notifier.New(cfg.SMTPHost, cfg.SMTPPort, cfg.SMTPUser, cfg.SMTPPass, cfg.SMTPFrom)
	svc := service.New(repo, ghClient, emailNotifier, cfg.BaseURL)
	h := handler.New(svc)

	// --- Start Background Scanner (goroutine) ---
	releaseScanner := scanner.New(repo, ghClient, emailNotifier, cfg.ScanIntervalSecs, cfg.BaseURL)
	go releaseScanner.Start() // `go` launches it in the background

	// --- Setup Router ---
	router := gin.Default()

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

	// --- Start Server ---
	log.Printf("Server starting on port %s", cfg.AppPort)
	if err := router.Run(":" + cfg.AppPort); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
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
