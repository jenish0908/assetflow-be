package billing

import (
	"context"
	"fmt"
	"io"
	"time"
)

// IntentLister lists PaymentIntents at the provider. Gateway satisfies it.
type IntentLister interface {
	ListPaymentIntents(ctx context.Context, since time.Time) ([]PaymentIntent, error)
}

// ReconcileStore is the DB side of reconciliation. Store satisfies it.
type ReconcileStore interface {
	ListPaymentsSince(ctx context.Context, since time.Time) ([]Payment, error)
	SucceededJobCounts(ctx context.Context, since time.Time) (map[int64]int, error)
}

// Mismatch describes one disagreement between Stripe and the database.
type Mismatch struct {
	PaymentID int64
	IntentID  string
	Detail    string
}

// Report lists every disagreement found. An empty report means Stripe and
// the database agree.
type Report struct {
	StripeIntents int
	DBPayments    int

	// OrphanIntents exist at Stripe but no payment row points at them, e.g. a
	// second intent created by a retry without an idempotency key.
	OrphanIntents []Mismatch
	// OrphanCharged counts orphan intents that succeeded: money taken with no
	// matching payment record, i.e. double charges.
	OrphanCharged int
	// StatusMismatches: the row's status differs from the intent's.
	StatusMismatches []Mismatch
	// AmountMismatches: the row's amount or currency differs from the intent's.
	AmountMismatches []Mismatch
	// MissingIntents: the row points at an intent Stripe does not have.
	MissingIntents []Mismatch
	// StuckPending: rows that never got an intent (Stripe was unreachable).
	StuckPending []Mismatch
	// NotificationIssues: payments whose intent succeeded but that do not
	// have exactly one payment_succeeded notification job.
	NotificationIssues []Mismatch
}

// Total is the number of problems found.
func (r Report) Total() int {
	return len(r.OrphanIntents) + len(r.StatusMismatches) + len(r.AmountMismatches) +
		len(r.MissingIntents) + len(r.StuckPending) + len(r.NotificationIssues)
}

// Reconcile compares Stripe's PaymentIntents with the payments table for
// everything created since the given time.
func Reconcile(ctx context.Context, stripe IntentLister, db ReconcileStore, since time.Time) (Report, error) {
	intents, err := stripe.ListPaymentIntents(ctx, since)
	if err != nil {
		return Report{}, err
	}
	payments, err := db.ListPaymentsSince(ctx, since)
	if err != nil {
		return Report{}, fmt.Errorf("reconcile: list payments: %w", err)
	}
	jobs, err := db.SucceededJobCounts(ctx, since)
	if err != nil {
		return Report{}, fmt.Errorf("reconcile: count notification jobs: %w", err)
	}

	r := Report{StripeIntents: len(intents), DBPayments: len(payments)}
	byID := make(map[string]PaymentIntent, len(intents))
	for _, pi := range intents {
		byID[pi.ID] = pi
	}
	referenced := make(map[string]bool, len(payments))

	for _, p := range payments {
		if p.PaymentIntentID == nil {
			r.StuckPending = append(r.StuckPending, Mismatch{PaymentID: p.ID, Detail: "no intent; status " + p.Status})
			continue
		}
		referenced[*p.PaymentIntentID] = true
		pi, ok := byID[*p.PaymentIntentID]
		if !ok {
			r.MissingIntents = append(r.MissingIntents, Mismatch{PaymentID: p.ID, IntentID: *p.PaymentIntentID, Detail: "intent not found at Stripe"})
			continue
		}
		if p.Status != pi.Status {
			r.StatusMismatches = append(r.StatusMismatches, Mismatch{
				PaymentID: p.ID, IntentID: pi.ID,
				Detail: fmt.Sprintf("db=%s stripe=%s", p.Status, pi.Status),
			})
		}
		if p.AmountCents != pi.AmountCents || p.Currency != pi.Currency {
			r.AmountMismatches = append(r.AmountMismatches, Mismatch{
				PaymentID: p.ID, IntentID: pi.ID,
				Detail: fmt.Sprintf("db=%d %s stripe=%d %s", p.AmountCents, p.Currency, pi.AmountCents, pi.Currency),
			})
		}
		if pi.Status == StatusSucceeded && jobs[p.ID] != 1 {
			r.NotificationIssues = append(r.NotificationIssues, Mismatch{
				PaymentID: p.ID, IntentID: pi.ID,
				Detail: fmt.Sprintf("%d payment_succeeded jobs, want 1", jobs[p.ID]),
			})
		}
	}

	for _, pi := range intents {
		if referenced[pi.ID] {
			continue
		}
		r.OrphanIntents = append(r.OrphanIntents, Mismatch{
			PaymentID: pi.PaymentID, IntentID: pi.ID,
			Detail: fmt.Sprintf("status %s, %d %s", pi.Status, pi.AmountCents, pi.Currency),
		})
		if pi.Status == StatusSucceeded {
			r.OrphanCharged++
		}
	}
	return r, nil
}

// WriteText prints a human-readable report with up to five examples per
// category.
func (r Report) WriteText(w io.Writer) {
	fmt.Fprintf(w, "stripe intents: %d, db payments: %d, problems: %d\n", r.StripeIntents, r.DBPayments, r.Total())
	sections := []struct {
		name  string
		items []Mismatch
	}{
		{fmt.Sprintf("orphan intents (%d charged)", r.OrphanCharged), r.OrphanIntents},
		{"status mismatches", r.StatusMismatches},
		{"amount mismatches", r.AmountMismatches},
		{"missing intents", r.MissingIntents},
		{"stuck pending", r.StuckPending},
		{"notification issues", r.NotificationIssues},
	}
	for _, s := range sections {
		fmt.Fprintf(w, "  %-28s %d\n", s.name+":", len(s.items))
		for i, m := range s.items {
			if i == 5 {
				fmt.Fprintf(w, "      ... and %d more\n", len(s.items)-5)
				break
			}
			fmt.Fprintf(w, "      payment=%d intent=%s %s\n", m.PaymentID, m.IntentID, m.Detail)
		}
	}
}
