package config

import (
	"os"
	"strconv"
)

// Config holds all application configuration loaded from env variables
type Config struct {
	DatabaseURL      string
	AppPort          string
	BaseURL          string
	RabbitMQURL      string
	GitHubToken      string
	ScanIntervalSecs int
	RedisURL         string
	CacheTTLSeconds  int
	// OutboxPollIntervalMs is how often the transactional-outbox relay polls
	// the outbox table for unpublished messages (milliseconds).
	OutboxPollIntervalMs int
	APIKey               string
	LogLevel             string
}

// Load reads all config from environment variables with sensible defaults
func Load() *Config {
	return &Config{
		DatabaseURL:          getEnv("DATABASE_URL", "postgres://postgres:postgres@db:5432/notifier?sslmode=disable"),
		AppPort:              getEnv("APP_PORT", "8080"),
		BaseURL:              getEnv("BASE_URL", "http://localhost:8080"),
		RabbitMQURL:          getEnv("RABBITMQ_URL", "amqp://guest:guest@localhost:5672/"),
		GitHubToken:          getEnv("GITHUB_TOKEN", ""),
		ScanIntervalSecs:     getEnvInt("SCAN_INTERVAL_SECONDS", 600),
		RedisURL:             getEnv("REDIS_URL", "redis://localhost:6379/0"),
		CacheTTLSeconds:      getEnvInt("CACHE_TTL_SECONDS", 600),
		OutboxPollIntervalMs: getEnvInt("OUTBOX_POLL_INTERVAL_MS", 1000),
		APIKey:               getEnv("API_KEY", ""),
		LogLevel:             getEnv("LOG_LEVEL", "info"),
	}
}

// getEnv returns the env var value or a default
func getEnv(key, fallback string) string {
	if val, ok := os.LookupEnv(key); ok {
		return val
	}
	return fallback
}

// getEnvInt returns the env var as int or a default
func getEnvInt(key string, fallback int) int {
	if val, ok := os.LookupEnv(key); ok {
		if i, err := strconv.Atoi(val); err == nil {
			return i
		}
	}
	return fallback
}
