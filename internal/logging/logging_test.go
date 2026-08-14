package logging

import (
	"log/slog"
	"testing"
)

// TestParseLevel covers the level-string mapping: case-insensitivity, the
// "warning" alias, surrounding whitespace, and the fallback to info for
// unknown/empty input (so a typo in LOG_LEVEL never silences logs entirely).
func TestParseLevel(t *testing.T) {
	tests := []struct {
		input string
		want  slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"warning", slog.LevelWarn}, // alias
		{"error", slog.LevelError},

		// case-insensitive + trimmed
		{"DEBUG", slog.LevelDebug},
		{"  Warn  ", slog.LevelWarn},
		{"ERROR", slog.LevelError},

		// unknown / empty → fall back to info, never panic or silence
		{"verbose", slog.LevelInfo},
		{"", slog.LevelInfo},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := parseLevel(tt.input); got != tt.want {
				t.Errorf("parseLevel(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
