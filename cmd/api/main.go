// Command api runs the AssetFlow HTTP server.
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

	"github.com/gin-gonic/gin"

	"github.com/jenish0908/assetflow-be/internal/agent"
	"github.com/jenish0908/assetflow-be/internal/auth"
	"github.com/jenish0908/assetflow-be/internal/billing"
	"github.com/jenish0908/assetflow-be/internal/config"
	"github.com/jenish0908/assetflow-be/internal/httpapi"
	"github.com/jenish0908/assetflow-be/internal/service"
	"github.com/jenish0908/assetflow-be/internal/store"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// run wires dependencies together. Keeping it separate from main means every
// failure is an error value and deferred cleanups always run.
func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	logger.Info("starting", "list_strategy", cfg.ListStrategy)

	pool, err := store.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	listStrategy := service.ListStrategy(cfg.ListStrategy)
	if !listStrategy.Valid() {
		return fmt.Errorf("LIST_STRATEGY must be nplus1, batched or join, got %q", cfg.ListStrategy)
	}

	st := store.New(pool)
	tokens := auth.NewTokenManager([]byte(cfg.JWTSecret), cfg.JWTTTL)

	users := service.NewUserService(st, tokens)
	assets := service.NewAssetService(st, listStrategy)

	features := httpapi.Features{Billing: "disabled", Agent: "fake"}

	var llm agent.LLM = agent.Fake{}
	if cfg.OpenAIAPIKey != "" {
		llm = agent.NewOpenAI(cfg.OpenAIAPIKey, cfg.OpenAIModel)
		features.Agent = "openai"
		logger.Info("agent using OpenAI", "model", cfg.OpenAIModel)
	} else {
		logger.Warn("OPENAI_API_KEY not set; agent uses the built-in fake model")
	}
	agentOpts := agent.DefaultOptions()
	agentOpts.ToolTimeout = cfg.AgentToolTimeout

	deps := httpapi.Deps{
		Logger:    logger,
		DB:        pool,
		Tokens:    tokens,
		Users:     users,
		Assets:    assets,
		Agent:     agent.New(llm, assets, users, agentOpts, logger),
		Dashboard: service.NewDashboardService(st),
		RateLimit: httpapi.RateLimit{RPS: cfg.RateLimitRPS, Burst: cfg.RateLimitBurst},
	}
	// Only assign Billing when configured: a nil *billing.Service stored in
	// the interface field would make d.Billing != nil true.
	switch {
	case cfg.BillingMode == "fake":
		// In-memory card processor: every charge succeeds, nothing leaves the
		// machine. For local demos only; its event log grows in memory.
		gateway := billing.NewFakeStripe(time.Now().UnixNano(), 0)
		deps.Billing = billing.NewService(store.NewBilling(pool), gateway, "whsec_fake_mode", billing.DefaultOptions())
		features.Billing = "fake"
		logger.Warn("BILLING_MODE=fake: payments use a simulated card processor")
	case cfg.StripeSecretKey != "":
		gateway := billing.NewStripeGateway(cfg.StripeSecretKey, cfg.StripePaymentMethod)
		deps.Billing = billing.NewService(store.NewBilling(pool), gateway, cfg.StripeWebhookSecret, billing.DefaultOptions())
		features.Billing = "stripe"
	default:
		logger.Warn("billing disabled: set STRIPE_SECRET_KEY, or BILLING_MODE=fake for a local demo")
	}
	deps.Features = features

	gin.SetMode(gin.ReleaseMode)
	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           httpapi.NewRouter(deps),
		ReadHeaderTimeout: 5 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("http server listening", "addr", cfg.HTTPAddr)
		serveErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}

	logger.Info("shutting down", "timeout", cfg.ShutdownTimeout.String())
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("http shutdown: %w", err)
	}
	logger.Info("stopped")
	return nil
}
