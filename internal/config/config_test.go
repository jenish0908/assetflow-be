package config

import (
	"log/slog"
	"maps"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testSecret = "0123456789abcdef0123456789abcdef"

func TestLoad(t *testing.T) {
	required := map[string]string{"DATABASE_URL": "postgres://x", "JWT_SECRET": testSecret}
	with := func(extra map[string]string) map[string]string {
		env := maps.Clone(required)
		maps.Copy(env, extra)
		return env
	}

	tests := []struct {
		name    string
		env     map[string]string
		want    Config
		wantErr bool
	}{
		{
			name: "defaults",
			env:  required,
			want: Config{
				HTTPAddr:        ":8080",
				DatabaseURL:     "postgres://x",
				LogLevel:        slog.LevelInfo,
				ShutdownTimeout: 10 * time.Second,
				JWTSecret:       testSecret,
				JWTTTL:          time.Hour,
				RateLimitRPS:    10,
				RateLimitBurst:  20,
				ListStrategy:    "batched",

				StripePaymentMethod: "pm_card_visa",
				OpenAIModel:         "gpt-4o-mini",
				AgentToolTimeout:    5 * time.Second,
			},
		},
		{
			name: "overrides",
			env: with(map[string]string{
				"HTTP_ADDR":        ":9090",
				"LOG_LEVEL":        "debug",
				"SHUTDOWN_TIMEOUT": "3s",
				"JWT_TTL":          "15m",
				"RATE_LIMIT_RPS":   "0.5",
				"RATE_LIMIT_BURST": "5",
				"LIST_STRATEGY":    "join",
			}),
			want: Config{
				HTTPAddr:        ":9090",
				DatabaseURL:     "postgres://x",
				LogLevel:        slog.LevelDebug,
				ShutdownTimeout: 3 * time.Second,
				JWTSecret:       testSecret,
				JWTTTL:          15 * time.Minute,
				RateLimitRPS:    0.5,
				RateLimitBurst:  5,
				ListStrategy:    "join",

				StripePaymentMethod: "pm_card_visa",
				OpenAIModel:         "gpt-4o-mini",
				AgentToolTimeout:    5 * time.Second,
			},
		},
		{name: "missing database url", env: map[string]string{"JWT_SECRET": testSecret}, wantErr: true},
		{name: "missing jwt secret", env: map[string]string{"DATABASE_URL": "postgres://x"}, wantErr: true},
		{name: "short jwt secret", env: with(map[string]string{"JWT_SECRET": "short"}), wantErr: true},
		{name: "bad log level", env: with(map[string]string{"LOG_LEVEL": "loud"}), wantErr: true},
		{name: "bad shutdown timeout", env: with(map[string]string{"SHUTDOWN_TIMEOUT": "soon"}), wantErr: true},
		{name: "bad jwt ttl", env: with(map[string]string{"JWT_TTL": "1 hour"}), wantErr: true},
		{name: "zero rps", env: with(map[string]string{"RATE_LIMIT_RPS": "0"}), wantErr: true},
		{name: "bad burst", env: with(map[string]string{"RATE_LIMIT_BURST": "many"}), wantErr: true},
		{name: "stripe key without webhook secret", env: with(map[string]string{"STRIPE_SECRET_KEY": "sk_test_x"}), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getenv := func(k string) string { return tt.env[k] }

			got, err := Load(getenv)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestLoadWorker(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    WorkerConfig
		wantErr bool
	}{
		{
			name: "defaults",
			env:  map[string]string{"DATABASE_URL": "postgres://x"},
			want: WorkerConfig{DatabaseURL: "postgres://x", LogLevel: slog.LevelInfo, WorkerID: "host-1", Concurrency: 8, ShutdownTimeout: 20 * time.Second},
		},
		{
			name: "overrides",
			env:  map[string]string{"DATABASE_URL": "postgres://x", "WORKER_ID": "w9", "WORKER_CONCURRENCY": "32", "SHUTDOWN_TIMEOUT": "5s", "LOG_LEVEL": "warn"},
			want: WorkerConfig{DatabaseURL: "postgres://x", LogLevel: slog.LevelWarn, WorkerID: "w9", Concurrency: 32, ShutdownTimeout: 5 * time.Second},
		},
		{name: "missing database url", env: map[string]string{}, wantErr: true},
		{name: "zero concurrency", env: map[string]string{"DATABASE_URL": "postgres://x", "WORKER_CONCURRENCY": "0"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := LoadWorker(func(k string) string { return tt.env[k] }, "host-1")
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
