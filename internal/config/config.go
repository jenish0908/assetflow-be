// Package config loads application settings from environment variables.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"
)

// Config holds all runtime settings. It is built once in main and passed
// down explicitly; nothing reads the environment after startup.
type Config struct {
	HTTPAddr        string
	DatabaseURL     string
	LogLevel        slog.Level
	ShutdownTimeout time.Duration
	JWTSecret       string
	JWTTTL          time.Duration
	RateLimitRPS    float64
	RateLimitBurst  int
	// ListStrategy picks how GET /assets loads allocations: batched (default),
	// join or nplus1. Kept configurable so the perf script can compare them.
	ListStrategy string
	// Stripe is optional: with no secret key, billing routes are disabled.
	StripeSecretKey     string
	StripeWebhookSecret string
	StripePaymentMethod string
	// OpenAI is optional: with no key the agent uses the built-in fake model.
	OpenAIAPIKey     string
	OpenAIModel      string
	AgentToolTimeout time.Duration
}

// Load builds a Config using getenv (normally os.Getenv). Taking the lookup
// function as a parameter keeps Load free of global state and easy to test.
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		HTTPAddr:        valueOr(getenv("HTTP_ADDR"), ":8080"),
		DatabaseURL:     getenv("DATABASE_URL"),
		LogLevel:        slog.LevelInfo,
		ShutdownTimeout: 10 * time.Second,
		JWTSecret:       getenv("JWT_SECRET"),
		JWTTTL:          time.Hour,
		RateLimitRPS:    10,
		RateLimitBurst:  20,
		ListStrategy:    valueOr(getenv("LIST_STRATEGY"), "batched"),

		StripeSecretKey:     getenv("STRIPE_SECRET_KEY"),
		StripeWebhookSecret: getenv("STRIPE_WEBHOOK_SECRET"),
		StripePaymentMethod: valueOr(getenv("STRIPE_PAYMENT_METHOD"), "pm_card_visa"),

		OpenAIAPIKey:     getenv("OPENAI_API_KEY"),
		OpenAIModel:      valueOr(getenv("OPENAI_MODEL"), "gpt-4o-mini"),
		AgentToolTimeout: 5 * time.Second,
	}
	if cfg.StripeSecretKey != "" && cfg.StripeWebhookSecret == "" {
		return Config{}, errors.New("config: STRIPE_WEBHOOK_SECRET is required when STRIPE_SECRET_KEY is set")
	}

	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("config: DATABASE_URL is required")
	}
	if len(cfg.JWTSecret) < 32 {
		return Config{}, errors.New("config: JWT_SECRET must be at least 32 bytes")
	}

	if v := getenv("LOG_LEVEL"); v != "" {
		if err := cfg.LogLevel.UnmarshalText([]byte(v)); err != nil {
			return Config{}, fmt.Errorf("config: LOG_LEVEL: %w", err)
		}
	}

	var err error
	if cfg.ShutdownTimeout, err = durationOr(getenv("SHUTDOWN_TIMEOUT"), cfg.ShutdownTimeout); err != nil {
		return Config{}, fmt.Errorf("config: SHUTDOWN_TIMEOUT: %w", err)
	}
	if cfg.JWTTTL, err = durationOr(getenv("JWT_TTL"), cfg.JWTTTL); err != nil {
		return Config{}, fmt.Errorf("config: JWT_TTL: %w", err)
	}
	if cfg.AgentToolTimeout, err = durationOr(getenv("AGENT_TOOL_TIMEOUT"), cfg.AgentToolTimeout); err != nil {
		return Config{}, fmt.Errorf("config: AGENT_TOOL_TIMEOUT: %w", err)
	}

	if v := getenv("RATE_LIMIT_RPS"); v != "" {
		if cfg.RateLimitRPS, err = strconv.ParseFloat(v, 64); err != nil || cfg.RateLimitRPS <= 0 {
			return Config{}, fmt.Errorf("config: RATE_LIMIT_RPS must be a positive number, got %q", v)
		}
	}
	if v := getenv("RATE_LIMIT_BURST"); v != "" {
		if cfg.RateLimitBurst, err = strconv.Atoi(v); err != nil || cfg.RateLimitBurst <= 0 {
			return Config{}, fmt.Errorf("config: RATE_LIMIT_BURST must be a positive integer, got %q", v)
		}
	}

	return cfg, nil
}

// WorkerConfig holds settings for cmd/worker.
type WorkerConfig struct {
	DatabaseURL     string
	LogLevel        slog.Level
	WorkerID        string
	Concurrency     int
	ShutdownTimeout time.Duration
}

// LoadWorker builds a WorkerConfig; defaultID is used when WORKER_ID is unset.
func LoadWorker(getenv func(string) string, defaultID string) (WorkerConfig, error) {
	cfg := WorkerConfig{
		DatabaseURL:     getenv("DATABASE_URL"),
		LogLevel:        slog.LevelInfo,
		WorkerID:        valueOr(getenv("WORKER_ID"), defaultID),
		Concurrency:     8,
		ShutdownTimeout: 20 * time.Second,
	}
	if cfg.DatabaseURL == "" {
		return WorkerConfig{}, errors.New("config: DATABASE_URL is required")
	}
	if v := getenv("LOG_LEVEL"); v != "" {
		if err := cfg.LogLevel.UnmarshalText([]byte(v)); err != nil {
			return WorkerConfig{}, fmt.Errorf("config: LOG_LEVEL: %w", err)
		}
	}
	if v := getenv("WORKER_CONCURRENCY"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1000 {
			return WorkerConfig{}, fmt.Errorf("config: WORKER_CONCURRENCY must be 1-1000, got %q", v)
		}
		cfg.Concurrency = n
	}
	var err error
	if cfg.ShutdownTimeout, err = durationOr(getenv("SHUTDOWN_TIMEOUT"), cfg.ShutdownTimeout); err != nil {
		return WorkerConfig{}, fmt.Errorf("config: SHUTDOWN_TIMEOUT: %w", err)
	}
	return cfg, nil
}

func valueOr(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func durationOr(v string, fallback time.Duration) (time.Duration, error) {
	if v == "" {
		return fallback, nil
	}
	return time.ParseDuration(v)
}
