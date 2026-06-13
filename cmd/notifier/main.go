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

	"github-release-notifier/internal/logging"

	"github.com/joho/godotenv"
)

func main() {
	// Same thin-main pattern as the monolith: all logic in run() so deferred
	// cleanup runs (os.Exit skips defers).
	if err := run(); err != nil {
		slog.Error("notifier exited with error", "err", err)
		os.Exit(1)
	}
}

func run() error {
	_ = godotenv.Load()
	logging.Setup(getEnv("LOG_LEVEL", "info"))

	// The notifier owns its own (small) config — it knows nothing about the
	// monolith's database/redis. SMTP is now this service's private concern.
	sender := NewSMTPSender(
		getEnv("SMTP_HOST", "localhost"),
		getEnv("SMTP_PORT", "1025"),
		getEnv("SMTP_USER", ""),
		getEnv("SMTP_PASS", ""),
		getEnv("SMTP_FROM", "noreply@github-notifier.local"),
	)

	// HTTP server now serves /health only (for the container healthcheck);
	// notification delivery moved to the RabbitMQ consumer below.
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	port := getEnv("NOTIFIER_PORT", "9090")
	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Consume notification commands from RabbitMQ and send the emails. A fatal
	// consumer error surfaces here so run() returns it -> the container exits
	// and restarts (restart: on-failure) instead of silently not consuming.
	dedup, err := newRedisDeduper(getEnv("REDIS_URL", "redis://localhost:6379/0"), 24*time.Hour)
	if err != nil {
		return fmt.Errorf("connect to redis: %w", err)
	}
	defer dedup.Close()

	consumer := NewConsumer(sender, dedup)
	consumerErr := make(chan error, 1)
	go func() {
		if err := consumer.Run(ctx, getEnv("RABBITMQ_URL", "amqp://guest:guest@localhost:5672/")); err != nil {
			consumerErr <- err
		}
	}()

	serverErr := make(chan error, 1)
	go func() {
		slog.Info("Notifier service starting", "port", port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-serverErr:
		return fmt.Errorf("notifier http server: %w", err)
	case err := <-consumerErr:
		return fmt.Errorf("notifier consumer: %w", err)
	case <-quit:
		slog.Info("Notifier shutting down gracefully")
	}

	cancel() // stop the consumer

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("notifier shutdown: %w", err)
	}
	slog.Info("Notifier stopped")
	return nil
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}
