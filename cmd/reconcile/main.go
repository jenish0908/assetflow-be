// Command reconcile compares PaymentIntents in Stripe with the payments table
// and reports every disagreement. It exits 1 when problems are found, so it
// can run on a schedule and alert.
//
//	DATABASE_URL=... STRIPE_SECRET_KEY=sk_test_... go run ./cmd/reconcile -since 24h
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/jenish0908/assetflow-be/internal/billing"
	"github.com/jenish0908/assetflow-be/internal/store"
)

func main() {
	problems, err := run()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
	if problems > 0 {
		os.Exit(1)
	}
}

func run() (int, error) {
	since := flag.Duration("since", 24*time.Hour, "check payments created within this window")
	flag.Parse()

	databaseURL, key := os.Getenv("DATABASE_URL"), os.Getenv("STRIPE_SECRET_KEY")
	if databaseURL == "" || key == "" {
		return 0, errors.New("DATABASE_URL and STRIPE_SECRET_KEY are required")
	}

	ctx := context.Background()
	pool, err := store.NewPool(ctx, databaseURL)
	if err != nil {
		return 0, err
	}
	defer pool.Close()

	gateway := billing.NewStripeGateway(key, "")
	report, err := billing.Reconcile(ctx, gateway, store.NewBilling(pool), time.Now().Add(-*since))
	if err != nil {
		return 0, err
	}
	report.WriteText(os.Stdout)
	return report.Total(), nil
}
