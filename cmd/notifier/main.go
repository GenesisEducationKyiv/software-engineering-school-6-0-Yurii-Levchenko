package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github-release-notifier/internal/logging"
	"github-release-notifier/internal/notification"

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

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/send/confirmation", handleConfirmation(sender))
	mux.HandleFunc("/send/release", handleRelease(sender))

	port := getEnv("NOTIFIER_PORT", "9090")
	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

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
	case <-quit:
		slog.Info("Notifier shutting down gracefully")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("notifier shutdown: %w", err)
	}
	slog.Info("Notifier stopped")
	return nil
}

// The request payloads carry the recipient email, which is PII — handlers must
// never log it (GDPR; same rule the monolith follows).

func handleConfirmation(sender *SMTPSender) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req notification.ConfirmationRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if err := sender.SendConfirmationEmail(req.To, req.ConfirmURL); err != nil {
			slog.Error("Failed to send confirmation email", "err", err)
			http.Error(w, "send failed", http.StatusBadGateway)
			return
		}
		slog.Info("Confirmation email sent")
		w.WriteHeader(http.StatusAccepted)
	}
}

func handleRelease(sender *SMTPSender) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req notification.ReleaseRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if err := sender.SendReleaseNotification(req.To, req.Repo, req.Tag, req.UnsubscribeURL); err != nil {
			slog.Error("Failed to send release notification", "repo", req.Repo, "err", err)
			http.Error(w, "send failed", http.StatusBadGateway)
			return
		}
		slog.Info("Release notification sent", "repo", req.Repo, "tag", req.Tag)
		w.WriteHeader(http.StatusAccepted)
	}
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}
