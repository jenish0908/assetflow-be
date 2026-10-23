// Command billingsim measures what idempotency keys and webhook dedupe
// protect against. It runs the real HTTP API, billing service and Postgres
// against FakeStripe, injects the failures Stripe integrations see in
// production, then runs the same reconciliation as cmd/reconcile.
//
// Injected failures:
//   - lost responses: Stripe creates (and charges) the intent, but the
//     response times out, so our service retries;
//   - client retries: a client that gets a 5xx resends with the same
//     Idempotency-Key;
//   - webhooks delivered in random order, some delivered twice concurrently.
//
// -mode before turns both safeguards off; -mode after is production.
// Requires DATABASE_URL pointing at a migrated database whose name ends in
// "_sim" (billing tables are truncated on every run).
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stripe/stripe-go/v81/webhook"
	"golang.org/x/sync/errgroup"

	"github.com/jenish0908/assetflow-be/internal/auth"
	"github.com/jenish0908/assetflow-be/internal/billing"
	"github.com/jenish0908/assetflow-be/internal/httpapi"
	"github.com/jenish0908/assetflow-be/internal/service"
	"github.com/jenish0908/assetflow-be/internal/store"
)

const webhookSecret = "whsec_billingsim"

type simConfig struct {
	mode     string
	payments int
	seed     int64
	lostRate float64
	dupRate  float64
	workers  int
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	var cfg simConfig
	flag.StringVar(&cfg.mode, "mode", "after", "before (no idempotency, no webhook guards) or after")
	flag.IntVar(&cfg.payments, "payments", 200, "payments to create")
	flag.Int64Var(&cfg.seed, "seed", 42, "random seed")
	flag.Float64Var(&cfg.lostRate, "lost-rate", 0.2, "probability a Stripe create response is lost")
	flag.Float64Var(&cfg.dupRate, "dup-rate", 0.3, "probability a webhook is delivered twice")
	flag.IntVar(&cfg.workers, "workers", 8, "concurrent webhook deliveries")
	flag.Parse()
	if cfg.mode != "before" && cfg.mode != "after" {
		return errors.New("-mode must be before or after")
	}

	ctx := context.Background()
	pool, err := store.NewPool(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return err
	}
	defer pool.Close()

	var dbName string
	if err := pool.QueryRow(ctx, "SELECT current_database()").Scan(&dbName); err != nil {
		return err
	}
	if !strings.HasSuffix(dbName, "_sim") {
		return fmt.Errorf("refusing to truncate %q: database name must end in _sim", dbName)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE payments, stripe_events, notification_jobs RESTART IDENTITY"); err != nil {
		return err
	}
	user, err := simUser(ctx, store.New(pool))
	if err != nil {
		return err
	}

	opts := billing.DefaultOptions()
	opts.RetryBackoff = time.Millisecond
	if cfg.mode == "before" {
		opts.Idempotency = false
		opts.WebhookGuards = false
	}
	fake := billing.NewFakeStripe(cfg.seed, cfg.lostRate)
	billingStore := store.NewBilling(pool)
	tokens := auth.NewTokenManager([]byte("billingsim-secret-billingsim-secret"), time.Hour)

	gin.SetMode(gin.ReleaseMode)
	srv := httptest.NewServer(httpapi.NewRouter(httpapi.Deps{
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:        pool,
		Tokens:    tokens,
		Billing:   billing.NewService(billingStore, fake, webhookSecret, opts),
		RateLimit: httpapi.RateLimit{RPS: 1e6, Burst: 1e6},
	}))
	defer srv.Close()

	token, err := tokens.Issue(user.ID, auth.RoleViewer)
	if err != nil {
		return err
	}
	start := time.Now().Add(-time.Second)
	rng := rand.New(rand.NewSource(cfg.seed)) //nolint:gosec // simulation, not security

	// Phase 1: create payments one by one, retrying 5xx like a careful client.
	var clientRetries int
	for i := range cfg.payments {
		body := fmt.Sprintf(`{"amount_cents":%d,"currency":"usd"}`, 500+rng.Int63n(10_000))
		for attempt := 1; attempt <= 3; attempt++ {
			code, err := post(ctx, srv.URL+"/payments", body, map[string]string{
				"Authorization":   "Bearer " + token,
				"Idempotency-Key": fmt.Sprintf("sim-payment-%d", i),
			})
			if err != nil {
				return err
			}
			if code < 500 {
				break
			}
			clientRetries++
		}
	}

	// Phase 2: deliver every webhook in random order; some twice, submitted
	// back to back so the copies run concurrently on the worker pool.
	events := fake.Events()
	rng.Shuffle(len(events), func(i, j int) { events[i], events[j] = events[j], events[i] })
	var deliveries, failed atomic.Int64
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(cfg.workers)
	for _, ev := range events {
		copies := 1
		if rng.Float64() < cfg.dupRate {
			copies = 2
		}
		for range copies {
			g.Go(func() error {
				deliveries.Add(1)
				ok, err := deliver(gctx, srv.URL, ev.Payload)
				if !ok {
					failed.Add(1)
				}
				return err
			})
		}
	}
	if err := g.Wait(); err != nil {
		return err
	}

	// Phase 3: compare Stripe with the database.
	report, err := billing.Reconcile(ctx, fake, billingStore, start)
	if err != nil {
		return err
	}

	fmt.Printf("mode=%s payments=%d seed=%d lost-rate=%.2f dup-rate=%.2f workers=%d\n",
		cfg.mode, cfg.payments, cfg.seed, cfg.lostRate, cfg.dupRate, cfg.workers)
	fmt.Printf("client retries after 5xx: %d, webhook events: %d, deliveries: %d (failed after retries: %d)\n",
		clientRetries, len(events), deliveries.Load(), failed.Load())
	report.WriteText(os.Stdout)
	fmt.Printf("SUMMARY|%s|%d|%d|%d|%d|%d|%d|%d|%d|%d|%d\n", cfg.mode,
		report.StripeIntents, report.DBPayments,
		len(report.OrphanIntents), report.OrphanCharged,
		len(report.StatusMismatches), len(report.AmountMismatches),
		len(report.MissingIntents), len(report.StuckPending),
		len(report.NotificationIssues), report.Total())
	return nil
}

func simUser(ctx context.Context, st *store.Store) (service.User, error) {
	const email = "billingsim@assetflow.dev"
	u, err := st.CreateUser(ctx, service.NewUser{Email: email, Name: "Billing Sim", PasswordHash: "x", Role: auth.RoleViewer})
	if errors.Is(err, service.ErrConflict) {
		creds, err := st.GetUserCredentials(ctx, email)
		return creds.User, err
	}
	return u, err
}

// deliver POSTs one signed event, retrying 5xx responses like Stripe does.
func deliver(ctx context.Context, baseURL string, payload []byte) (bool, error) {
	for attempt := 1; attempt <= 5; attempt++ {
		signed := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{Payload: payload, Secret: webhookSecret})
		code, err := post(ctx, baseURL+"/webhooks/stripe", string(payload), map[string]string{"Stripe-Signature": signed.Header})
		if err != nil {
			return false, err
		}
		if code < 300 {
			return true, nil
		}
		if code < 500 {
			return false, fmt.Errorf("webhook rejected with %d", code)
		}
	}
	return false, nil
}

func post(ctx context.Context, url, body string, headers map[string]string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewBufferString(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}
