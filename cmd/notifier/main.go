package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	notifierv1 "github-release-notifier/gen/notifier/v1"
	"github-release-notifier/internal/logging"

	"github.com/joho/godotenv"
	"google.golang.org/grpc"
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

	// ctx signals the consumer to stop; wg lets us wait until it actually has.
	var wg sync.WaitGroup

	consumer := NewConsumer(sender, dedup)
	consumerErr := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := consumer.Run(ctx, getEnv("RABBITMQ_URL", "amqp://guest:guest@localhost:5672/")); err != nil {
			consumerErr <- err
		}
	}()

	// gRPC server: the synchronous confirmation transport (ADR-011), running
	// alongside the AMQP consumer on its own port and sharing the same SMTP
	// sender + Redis dedup. Idle unless the monolith runs CONFIRMATION_TRANSPORT=grpc.
	grpcPort := getEnv("NOTIFIER_GRPC_PORT", "50051")
	grpcSrv := grpc.NewServer()
	notifierv1.RegisterNotifierServiceServer(grpcSrv, newConfirmationServer(sender, dedup))
	grpcErr := make(chan error, 1)
	go func() {
		lis, err := net.Listen("tcp", ":"+grpcPort)
		if err != nil {
			grpcErr <- fmt.Errorf("grpc listen: %w", err)
			return
		}
		slog.Info("Notifier gRPC server starting", "port", grpcPort)
		if err := grpcSrv.Serve(lis); err != nil {
			grpcErr <- fmt.Errorf("grpc serve: %w", err)
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
	case err := <-grpcErr:
		return fmt.Errorf("notifier grpc server: %w", err)
	case <-quit:
		slog.Info("Notifier shutting down gracefully")
	}

	cancel() // stop the consumer
	grpcSrv.GracefulStop()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("notifier shutdown: %w", err)
	}

	// Wait for the consumer to finish its in-flight delivery, bounded by a
	// timeout so a stuck send can't block the process from exiting.
	waitForWorkers(&wg, consumerShutdownTimeout)
	slog.Info("Notifier stopped")
	return nil
}

// consumerShutdownTimeout bounds how long run() waits for the AMQP consumer
// after cancellation.
const consumerShutdownTimeout = 10 * time.Second

// waitForWorkers blocks until every background worker has returned or timeout
// elapses, whichever comes first.
func waitForWorkers(wg *sync.WaitGroup, timeout time.Duration) {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		slog.Info("Background workers stopped")
	case <-time.After(timeout):
		slog.Warn("Background workers did not stop in time, exiting anyway", "timeout", timeout)
	}
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}
