// Package logging configures the application's structured logger.
//
// We use the standard library's log/slog with a JSON handler so that every
// log line is a single JSON object. This lets the log pipeline (Filebeat →
// Elasticsearch) index individual fields (level, msg, repo, err, ...) for
// search and aggregation in Kibana.
//
// Setup installs the configured logger as slog's default, so the rest of the
// codebase just calls slog.Info/Warn/Error without threading a *slog.Logger
// through every constructor.
package logging

import (
	"log/slog"
	"os"
	"strings"
)

// Setup builds a JSON slog logger at the given level and installs it as the
// process-wide default. Returns the logger in case a caller wants it directly.
//
// level is case-insensitive: "debug", "info", "warn", "error". Unknown values
// fall back to info.
func Setup(level string) *slog.Logger {
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: parseLevel(level),
	})
	logger := slog.New(handler)
	slog.SetDefault(logger)
	return logger
}

func parseLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
