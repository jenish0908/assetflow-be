# Performance

Every number on this page comes from a script in `scripts/` that you can
re-run. Raw tool output for each run is kept in `docs/perf-results/`. The
numbers depend on the machine, so if you re-run, compare ratios, not absolute
milliseconds.

## M3: N+1 on `GET /assets` and the fix

**Script:** `scripts/perf_list_assets.sh` (seeds `scripts/seed.sql` into a separate
`assetflow_perf` database, then load-tests each list strategy with
[`hey`](https://github.com/rakyll/hey)).
**Run:** [`perf-results/20261023T063713Z-list-assets.md`](perf-results/20261023T063713Z-list-assets.md),
with raw `hey` output beside it.

- Request: `GET /assets?limit=50` with a viewer JWT, 20 concurrent workers for 30 s, after a 500-request warm-up
- Data: 10,000 assets, 18,333 allocations (0–4 per asset)
- Machine: 8 CPUs, 16 GB RAM for Docker (Docker Desktop on Windows 11). Postgres, the API and the load generator all run on the same machine.
- Queries per request: `sum(calls)` from `pg_stat_statements` during the run, divided by the number of 200 responses. This is counted by Postgres itself, not estimated.

| strategy | requests in 30 s | req/s | p50 (ms) | p95 (ms) | p99 (ms) | DB queries / request |
|---|---:|---:|---:|---:|---:|---:|
| `nplus1` (before) | 8,769 | 292 | 60.6 | 123.7 | 192.4 | 52.00 |
| `batched` (fix, default) | 43,017 | 1,433 | 12.3 | 27.0 | 43.2 | 3.00 |
| `join` (alternative) | 32,111 | 1,070 | 15.7 | 38.5 | 74.9 | 2.00 |

All requests in all three runs returned 200.

**Result (N+1 → batched):** p95 latency **123.7 ms → 27.0 ms (4.6× lower)**,
p50 **60.6 → 12.3 ms**, throughput **292 → 1,433 req/s (4.9×)**, DB queries per
request **52 → 3 (−94%)**.

### What the strategies do

- **`nplus1`:** 1 query for the page of 50 assets, then 1 allocations query per
  asset (50), plus 1 `count(*)` for the total = 52.
- **`batched`:** 1 query for the page, 1 query
  `WHERE asset_id = ANY($1::bigint[])` for all 50 assets' allocations, grouped
  into a `map[assetID][]Allocation` in Go, plus the count = 3.
- **`join`:** the page is cut in a subquery, then `LEFT JOIN allocations`, and
  the flat rows are folded back into assets in Go, plus the count = 2.

### Why JOIN is slower than batched despite fewer queries

The JOIN result repeats every asset column once per allocation row, so it sends
more bytes and has more rows to scan and fold. It also sorts the joined rows,
`ORDER BY a.id, allocated_at DESC`. The batched query returns each asset once
and each allocation once. On this data the extra round trip costs less than the
duplication. Query count matters, but so does the size and shape of each result.

### Correctness

`internal/store/store_integration_test.go` (`TestListStrategiesAgree`) pages
through a real Postgres with all three strategies and requires identical
results at every offset, including assets with no allocations and pages that
cut across allocation histories.

### Later re-runs and run-to-run variance

When the UI work added search and status filters to `GET /assets`, the list
was re-measured to check for regressions:

| run | what changed | nplus1 req/s | batched req/s | batched p95 | queries/req (n+1 / batched) |
|---|---|---:|---:|---:|---:|
| [`…104646Z`](perf-results/20261023T104646Z-list-assets.md) | catch-all filter SQL; **`npm install` running at the same time** (invalid) | 139 | 514 | 103.1 ms | 52 / 3 |
| [`…105352Z`](perf-results/20261023T105352Z-list-assets.md) (no other work of mine running) | catch-all filter SQL | 193 | 876 | 50.5 ms | 52 / 3 |
| [`…110141Z`](perf-results/20261023T110141Z-list-assets.md) (no other work of mine running) | filters moved to separate `*Filtered` queries; hot path is the SQL measured above | 169 | 699 | 69.5 ms | 52 / 3 |

Findings:
- `EXPLAIN ANALYZE` showed a real but small cost of the catch-all
  `($1 IS NULL OR status = $1)` style: in a generic plan the condition is
  evaluated on every row, so the per-request `count(*)` went from 1.2 ms to
  2.55 ms. The unfiltered list now uses its own statements again (see
  `db/queries/assets.sql`), and searches use the `*Filtered` ones.
- The absolute numbers in these re-runs are lower for **every** strategy,
  including the last run, whose hot-path SQL is identical to the headline
  run. Host CPU was at about 68% before any test started (other Windows
  processes and another local service), so these runs are not comparable in
  absolute terms with the headline table. The relationships held in every
  run: 52 vs 3 queries per request, and batched at 3.7–4.5× the N+1 throughput (4.9× in the headline run).
- Lesson: on a shared laptop, compare strategies within one run, record the
  host load, and repeat before trusting an absolute number.

### Re-run

```bash
scripts/perf_list_assets.sh                 # all three strategies, 30 s each
DURATION=60s CONCURRENCY=50 scripts/perf_list_assets.sh nplus1 batched
```

## M4: Stripe billing, before and after idempotency + webhook dedupe

**Script:** `scripts/billing_sim.sh` runs `cmd/billingsim` twice (safeguards
off, then on) against its own `assetflow_billing_sim` database.
**Run:** [`perf-results/20261023T071951Z-billing-sim.md`](perf-results/20261023T071951Z-billing-sim.md),
with the full reconciliation output for each mode beside it.

> **These numbers are from a simulation, not from live Stripe.** The real HTTP
> API, billing service, Postgres and Stripe signature verification
> (`webhook.ConstructEvent`) run against `billing.FakeStripe`, an in-memory
> gateway that follows Stripe's documented behaviour: idempotency-key replay,
> and `created`/`processing`/`succeeded` events with increasing timestamps.
> Real Stripe cannot be made to drop responses or redeliver webhooks on
> demand, which is why a fake is used. `cmd/reconcile` runs the same
> reconciliation against a real Stripe account.

Injected failures, seed 42, 200 payments:
- 20% of Stripe create responses are lost: the intent is created and charged, but our call times out and is retried.
- A client that receives a 5xx resends with the same `Idempotency-Key`.
- All webhooks are delivered in random order, and 30% are delivered twice, back to back on 8 concurrent workers.

| mode | Stripe intents | DB payments | double charges (orphan succeeded intents) | status mismatches | duplicate/missing notifications | total problems |
|---|---:|---:|---:|---:|---:|---:|
| before: no idempotency key, no webhook dedupe/ordering | 249 | 202 | **47** | **138** | **202** | **387** |
| after: production code | 200 | 200 | **0** | **0** | **0** | **0** |

What each "before" problem is:
- **Double charges (47):** each lost response was retried without an
  idempotency key, so Stripe created a second PaymentIntent and charged the
  card again. 47 of 200 payments (23.5%) charged the customer twice.
- **Status mismatches (138):** a stale event (`created`/`processing`) applied
  after `succeeded`, so the DB shows a non-final status for a payment that
  succeeded at Stripe.
- **Notification issues (202):** every payment row had a
  `payment_succeeded` job count other than exactly one. The listed examples
  have 2–4, from duplicate deliveries, duplicate intents and the
  API-response path.

### Bug found by this script in the "after" code

The first run ([`perf-results/20261023T071517Z-billing-sim.md`](perf-results/20261023T071517Z-billing-sim.md))
reported **200 notification issues in "after" mode: every payment had 0
notification jobs.** With `confirm=true`, Stripe's create response already
says `succeeded`. The code stored that status, and the `succeeded` webhook
then saw a terminal payment and skipped its side effect. The fix ties the
notification to the *transition into* `succeeded` in a single
`applyStatus`, used by both the API-response path and the webhook path under
the row lock. `TestSucceededResponseThenWebhook` is the regression test.

### Tests behind the guarantees

- `TestDuplicateWebhookProcessedOnce`: the same signed event delivered 2 and 10
  times at the same instant gives 1 notification and 1 `stripe_events` row.
  With dedupe off, the same test sees 2, which shows it catches the bug.
- `TestOutOfOrderEvents`: `succeeded` then `processing` then `created` leaves the payment succeeded.
- `TestCreatePaymentIdempotency`: 3 lost responses plus 2 client retries still create exactly 1 PaymentIntent.
- `TestWebhookBeforeIntentSaved`: a webhook that beats the API response is matched through metadata.

### Re-run

```bash
scripts/billing_sim.sh
SEED=7 LOST_RATE=0.05 DUP_RATE=0.1 PAYMENTS=1000 scripts/billing_sim.sh
```

## M5: Notification worker throughput

**Script:** `scripts/notify_throughput.sh` runs `cmd/notifybench` against its own
`assetflow_bench` database.
**Run:** [`perf-results/20261023T073545Z-notify-throughput.md`](perf-results/20261023T073545Z-notify-throughput.md).

Each row enqueues 5,000 jobs, starts the worker(s), and measures the time
until Postgres shows every job `done`. That includes claiming
(`FOR UPDATE SKIP LOCKED`), handling, and marking done. The handler only
sleeps for the "send latency", standing in for an email API call, so these
numbers are the worker's own capacity, not a real provider's. Single runs on
8 CPUs / 16 GB in Docker, with Postgres on the same machine.

| send latency | instances | goroutines per instance | seconds | jobs/sec | handled twice |
|---:|---:|---:|---:|---:|---:|
| 20 ms | 1 | 1 | 129.56 | 39 | 0 |
| 20 ms | 1 | 4 | 31.99 | 156 | 0 |
| 20 ms | 1 | 16 | 8.52 | 587 | 0 |
| 20 ms | 1 | 64 | 2.21 | **2,260** | 0 |
| 20 ms | 2 | 16 | 5.65 | 885 | 0 |
| 0 | 1 | 16 | 4.92 | 1,017 | 0 |
| 0 | 1 | 64 | 2.62 | 1,909 | 0 |

What it shows:
- **The bounded pool scales almost linearly for I/O-bound jobs.** Each 4×
  increase in goroutines gives 3.8–4.0× the throughput, 39 → 2,260 jobs/sec
  (58×) from 1 to 64 goroutines. Goroutines waiting on I/O cost almost nothing.
- **Per-job queue overhead is about 6 ms with one goroutine:** 39 jobs/sec
  against a 50/sec ceiling for 20 ms jobs, spent on claim and completion
  round trips.
- **Without send latency the queue tops out at about 1–2k jobs/sec**, limited
  by one completion `UPDATE` per job through a 20-connection pool. The
  64-goroutine rows at 0 ms and 20 ms land in the same range; their order is
  within run-to-run noise. The next optimisation would be batching
  completions (`UPDATE … WHERE id = ANY($1)`).
- **Horizontal scaling:** two instances × 16 goroutines gave 885 jobs/sec, 1.5×
  one instance, sharing one Postgres on one machine.
- **Exactly once at the claim level:** 0 jobs handled twice in every run,
  including two instances competing for the same rows.

Correctness tests, all with `-race` and `goleak.VerifyTestMain`, which fails the
package if any goroutine outlives a test:
- `TestGracefulShutdownFinishesInFlight`: on shutdown, in-flight jobs finish
  with an uncancelled context, and the 18 claimed-or-queued jobs return to
  `pending` with their attempt refunded.
- `TestShutdownDeadlineCancelsInFlight`: a job that outlives the deadline is
  cancelled and re-queued, and `Run` returns right after the deadline.
- `TestTwoWorkersNeverShareAJob` (real Postgres): 500 jobs, 2 workers, each job handled once (split 256/244).
- `TestExpiredLeaseIsReclaimedAndFenced`: a crashed worker's job is reclaimed
  after the lease, and its late update is ignored because of `locked_by`.

### Re-run

```bash
scripts/notify_throughput.sh
JOBS=20000 scripts/notify_throughput.sh
```
