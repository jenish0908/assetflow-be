# Interview notes

Every concept used in AssetFlow: what it is, where it lives in this repo, and
the questions interviewers ask about it, with answers grounded in this code.
Numbers quoted here come from [perf.md](perf.md).

**Contents**
1. [Goroutines vs OS threads](#1-goroutines-vs-os-threads)
2. [Channels and select](#2-channels-and-select)
3. [Context and cancellation](#3-context-and-cancellation)
4. [errgroup vs sync.WaitGroup](#4-errgroup-vs-syncwaitgroup)
5. [Idempotency keys vs webhook dedupe](#5-idempotency-keys-vs-webhook-dedupe)
6. [The N+1 query problem](#6-the-n1-query-problem)
7. [SELECT … FOR UPDATE SKIP LOCKED](#7-select--for-update-skip-locked)
8. [Graceful shutdown](#8-graceful-shutdown)
9. [pgx vs database/sql](#9-pgx-vs-databasesql)
10. [sqlc vs an ORM](#10-sqlc-vs-an-orm)
11. [Distroless images](#11-distroless-images)
12. [Also used: errors, interfaces, testing, auth, outbox, LLM safety](#12-also-used)

---

## 1. Goroutines vs OS threads

**What.** A goroutine is a function running concurrently, managed by the Go
runtime, not the OS. It starts with a ~2 KB stack that grows and shrinks as
needed (an OS thread typically reserves ~1–8 MB). The runtime's scheduler
multiplexes many goroutines onto a few OS threads: the **M:N model**, with G
(goroutines), M (OS threads) and P (logical processors, `GOMAXPROCS`). When a
goroutine blocks on network I/O, the netpoller parks it and the thread runs
another goroutine.

**In AssetFlow.** [`notify/worker.go`](../internal/notify/worker.go) runs one
claimer goroutine plus N handler goroutines. Throughput at 20 ms per job went
from 39 jobs/s (1 goroutine) to 2,260 jobs/s (64), almost linear, because the
goroutines spend their time waiting.

**Q: If goroutines are cheap, why limit the pool to N?**
The goroutine is cheap; what it holds is not. Each job uses a DB connection
(the pool has 20), memory for its payload, and a slice of a provider's rate
limit. An unbounded pool turns a backlog into a stampede on those. N is the
back-pressure knob, and the claimer only leases as many jobs as the channel
has room for.

**Q: How do goroutines leak, and how do you catch it?**
A goroutine blocked forever on a channel send or receive nobody will
complete, or on a context nobody cancels. This repo runs
`goleak.VerifyTestMain` in the `notify` and `agent` packages: the test binary
fails if any goroutine outlives the tests.

**Q: What does `GOMAXPROCS` control?**
How many OS threads execute Go code at the same time (P count). It defaults to
the number of CPUs. Blocking syscalls get extra threads.

---

## 2. Channels and select

**What.** A channel is a typed pipe between goroutines. Unbuffered: send and
receive meet (synchronisation). Buffered: up to `cap` items can wait (a
queue). Closing a channel tells receivers "no more values"; `v, ok := <-ch`
gives `ok == false` once it is drained. `select` waits on several channel
operations and runs one that is ready.

**In AssetFlow.** The worker's claimer sends leased jobs into a buffered
channel (`cap = 2 × concurrency`) and `close`s it when it stops. Handlers
`select` on "next job" or "shutdown":

```go
select {
case <-stop.Done():
    return
case job, ok := <-jobs:
    if !ok { return }            // channel closed: claimer is done
    if stop.Err() != nil {       // select picked the job, but we are shutting down
        w.release([]Job{job})
        return
    }
    w.runJob(jobCtx, job)
}
```

**Q: What happens if two `select` cases are ready?**
One is chosen **at random** (so neither starves). That is why the code above
re-checks `stop.Err()` after receiving: otherwise a worker could start a new
job after shutdown began.

**Q: Who should close a channel?**
The sender, and only once. Closing from the receiver side, or twice, panics a
sender. Here only the claimer sends, so the claimer closes (`defer close(jobs)`).

**Q: Buffered or unbuffered here?**
Buffered, so claiming (a DB round trip) overlaps with handling. It is sized
small and the claimer only fills free slots, because every buffered job is
already leased; a big buffer would hold leases on jobs nobody is working on.

---

## 3. Context and cancellation

**What.** `context.Context` carries a deadline, a cancellation signal and
request-scoped values down a call chain. Cancelling a context cancels every
context derived from it. Functions that do I/O take `ctx` as the first
parameter and stop when `ctx.Done()` closes.

**In AssetFlow.**
- `signal.NotifyContext` turns SIGTERM into a cancelled context ([`cmd/api`](../cmd/api/main.go), [`cmd/worker`](../cmd/worker/main.go)).
- `/readyz` pings the DB with `context.WithTimeout(…, 2s)`.
- The agent gives each tool call `context.WithTimeout(ctx, 5s)` ([`agent/agent.go`](../internal/agent/agent.go)); a cancelled HTTP request still cancels every tool.
- The worker gives in-flight jobs `context.WithoutCancel(ctx)` so SIGTERM does not kill half-finished work, and cancels them only after the shutdown deadline.
- After a job's context is cancelled, the worker still records the outcome with a fresh `context.WithTimeout(context.WithoutCancel(ctx), 5s)`.

**Q: Why not store the context in a struct?**
A context belongs to one operation; a struct usually outlives it. Passing it
explicitly makes lifetimes visible. (`gin.Context` is a request object;
`c.Request.Context()` is the real context.)

**Q: `WithTimeout` returns a `cancel` func. Why call it if the timeout fires anyway?**
To release the timer and the child's resources as soon as the work is done,
not when the timeout expires. `defer cancel()` right after creating it.

**Q: How do you tell "my deadline" from "the caller gave up"?**
Check the parent: the agent reports "timed out" only when
`errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil`, i.e. the
per-tool deadline fired but the request itself is still alive.

---

## 4. errgroup vs sync.WaitGroup

**What.** `sync.WaitGroup` waits for N goroutines and nothing else.
`golang.org/x/sync/errgroup` also collects the **first error** and, with
`WithContext`, **cancels a shared context** when any goroutine fails.
`SetLimit(n)` bounds how many run at once.

**In AssetFlow.**
- Worker: `errgroup.WithContext` runs the claimer and N handlers. If claiming fails 10 times in a row, the claimer returns an error; the group context is cancelled; handlers finish and stop; `Run` returns the error and the process exits for its supervisor to restart.
- Agent: an `errgroup.Group` with `SetLimit(4)` runs read-only tool calls in parallel. Every goroutine returns `nil`, because a failed tool is a *result for the model*, not a reason to cancel the other reads.
- Billing simulator: `errgroup` with `SetLimit(8)` delivers webhooks concurrently.
- Tests use plain `sync.WaitGroup` where goroutines cannot fail.

**Q: When is a WaitGroup enough?**
When goroutines cannot fail, or handle their own errors and you only need to
wait.

**Q: Any errgroup gotchas?**
`Wait` waits for **all** goroutines, even after the first error; they must
watch the group context to stop early. And do not put a goroutine in the group
that only exits *after* `Wait` returns: the worker's shutdown-deadline
goroutine lives outside the group for exactly that reason (it would deadlock).

---

## 5. Idempotency keys vs webhook dedupe

Two different problems with similar names.

| | Idempotency key | Webhook dedupe |
|---|---|---|
| direction | **outgoing**: our request to Stripe | **incoming**: Stripe's events to us |
| failure | we timed out; did the charge happen? | Stripe delivers at least once, sometimes concurrently, in any order |
| fix | send the same key on every retry; Stripe replays the first result | record `event.id` with a unique constraint, in the same transaction as the side effect |
| code | `billing.Service.CreatePayment` / `createIntent` | `billing.Service.processEvent`, `RecordStripeEvent` query |

**In AssetFlow.** The payment row and its key are stored *before* calling
Stripe; retries reuse it; a client resending with the same `Idempotency-Key`
header gets the original payment (reusing a key with a different amount → 409).
Webhooks run `INSERT INTO stripe_events … ON CONFLICT DO NOTHING` inside
`pgx.BeginFunc`; zero rows means "already processed".
Fault-injection result: **47 double charges and 387 total problems → 0**.

**Q: Why must the dedupe insert be in the same transaction as the side effect?**
If you record the event and then crash before updating the payment, the
redelivery is skipped and the update is lost. If you update first and crash
before recording, it is applied twice. Same transaction → both or neither.

**Q: Two deliveries arrive at the same millisecond. Walk through it.**
Both insert the same primary key. The second insert **blocks** on the first
transaction's uncommitted index entry. When the first commits, the second
sees the conflict, inserts nothing, and returns 200.
`TestDuplicateWebhookProcessedOnce` fires 2 and 10 identical deliveries at
once and gets one notification; with dedupe off, the same test sees 2.

**Q: Is dedupe by event ID enough?**
No. It catches the *same* event twice, but not *different* events out of
order (`processing` after `succeeded`). That needs the `last_event_at`
comparison and terminal states. It also misses the *same fact by two routes*:
Stripe's create response already says `succeeded`. The reconciliation
simulator caught exactly that bug in this code: notifications were never sent.
The fix ties the side effect to the **state transition**, decided under the
row lock, whichever message arrives first.

---

## 6. The N+1 query problem

**What.** Load a list with 1 query, then run 1 more query per item. Each query
is cheap on its own; the cost is N sequential round trips (plus a pool
checkout each), growing with page size and with DB latency.

**In AssetFlow.** `GET /assets` (page of 50 with allocations), measured with
`pg_stat_statements` and `hey`:

| strategy | DB queries / request | p95 | req/s |
|---|---:|---:|---:|
| N+1 | 52 | 123.7 ms | 292 |
| batched `WHERE asset_id = ANY($1)` | **3** | **27.0 ms** | **1,433** |
| JOIN | 2 | 38.5 ms | 1,070 |

All three are kept behind `LIST_STRATEGY` so the "before" stays measurable,
and `TestListStrategiesAgree` proves they return identical pages.

**Q: How do you detect it?**
Count queries per request, don't just time requests: `pg_stat_statements`
(reset, run load, sum calls ÷ requests), query logging, or a pgx tracer. Locally,
with the DB on the same machine, N+1 can look fine; in production each round
trip costs milliseconds.

**Q: Batched query or JOIN?**
Measure. Here JOIN made fewer queries but was slower: it repeats every asset
column on every allocation row and sorts the joined set. Batching sends each
row once. Also, with a JOIN the `LIMIT` must apply to assets in a subquery,
or it counts allocation rows and splits an asset's history across pages.

**Q: Why `ANY($1::bigint[])` and not `IN ($1, $2, …)`?**
One parameter and one SQL text for any number of IDs, so a single prepared
statement is cached, there is no string building, and you never hit the
65,535-parameter limit. Results are grouped into a `map[int64][]Allocation`.

---

## 7. SELECT … FOR UPDATE SKIP LOCKED

**What.** `FOR UPDATE` locks the selected rows until the transaction ends.
`SKIP LOCKED` makes other transactions **skip** rows that are already locked
instead of waiting. It turns a table into a work queue many workers can pull
from without blocking or double-claiming.

**In AssetFlow.** [`db/queries/notifications.sql`](../db/queries/notifications.sql):

```sql
UPDATE notification_jobs SET status = 'running', attempts = attempts + 1,
       locked_by = $worker, locked_until = now() + $lease
WHERE id IN (
  SELECT id FROM notification_jobs
  WHERE kind = ANY($kinds)
    AND ((status = 'pending' AND run_at <= now())
      OR (status = 'running' AND locked_until < now()))   -- expired lease
  ORDER BY run_at, id LIMIT $n
  FOR UPDATE SKIP LOCKED)
RETURNING …;
```

Two workers sharing 500 jobs split them 256/244 with zero duplicates; the
throughput benchmark found 0 duplicates in every run.

**Q: Why not hold the lock while the job runs?**
It would keep a transaction and a pool connection open for the whole job
(seconds, maybe minutes), and a long-running transaction blocks vacuum. The
claim commits immediately and a **lease** (`locked_until`) protects the job.

**Q: The worker crashes mid-job. What happens?**
The lease expires and another worker reclaims the job. `attempts` is counted
at claim time, so a job that keeps crashing workers still reaches
dead-letter. That makes delivery **at least once**, so each message carries a
stable key (`job-<id>`) for the provider to dedupe.

**Q: A slow worker comes back after its lease expired and someone else took the job. Can it overwrite the result?**
No: every update includes `AND locked_by = $me`. This is a **fencing** check;
the stale update matches zero rows (`TestExpiredLeaseIsReclaimedAndFenced`).

**Q: Postgres queue vs Kafka/RabbitMQ/SQS?**
A Postgres queue gives you **transactional enqueue** (the outbox: the job
commits with the business change) and no extra infrastructure. It tops out
lower (here about 1–2k jobs/s, bound by per-job UPDATEs) and polling adds
latency. Use a broker for high throughput, fan-out or cross-service streams,
often fed *from* an outbox table.

---

## 8. Graceful shutdown

**What.** On SIGTERM (what Kubernetes and Docker send before killing), stop
taking new work, finish what is in flight within a deadline, release what
never started, then exit.

**In AssetFlow.**
- **API:** `signal.NotifyContext` → `srv.Shutdown(ctx)` with a 10 s deadline: stops accepting connections, waits for in-flight requests. `ListenAndServe` returns `http.ErrServerClosed`, checked with `errors.Is` because it is expected.
- **Worker:**
  1. The claimer stops.
  2. In-flight jobs keep a non-cancelled context and finish.
  3. After `SHUTDOWN_TIMEOUT` they are cancelled and re-queued as retries.
  4. Buffered jobs that never started are released with their attempt refunded.

  Tested in `TestGracefulShutdownFinishesInFlight` and `TestShutdownDeadlineCancelsInFlight`.
- **Compose:** `stop_grace_period` is longer than the app's timeout. Docker's default is 10 s before SIGKILL, which would cut the worker's 20 s drain short.

**Q: Why does `main` call `run() error` instead of exiting directly?**
`os.Exit` skips deferred functions (`pool.Close()`, flushing). With
`run() error` every exit path runs defers, and `main` decides the exit code.

**Q: What breaks if shutdown isn't graceful?**
In-flight requests get connection resets during every deploy, jobs sit
`running` until their lease expires, and with SIGKILL a half-sent email can
be sent again.

---

## 9. pgx vs database/sql

**What.** `database/sql` is Go's generic SQL interface: driver-agnostic, with
its own pool, and values through `driver.Value`. **pgx** is a
PostgreSQL-specific driver and toolkit. It can sit under `database/sql`, but
its native API (`pgxpool`) is faster and speaks Postgres directly.

**In AssetFlow.** `pgxpool` everywhere ([`store/store.go`](../internal/store/store.go)):
- native arrays (`ANY($1::bigint[])` takes a `[]int64` directly);
- `jsonb` as `[]byte`;
- typed errors: `*pgconn.PgError` with SQLSTATE codes; `23505` unique violation → `ErrConflict` in `translate()`;
- `pgx.BeginFunc` for transactions: commit on `nil`, roll back on error or panic;
- automatic prepared-statement caching.

**Q: Why pgx?**
Performance (binary protocol, less reflection), Postgres types (arrays,
jsonb, intervals, `COPY`), precise errors, and listen/notify. The trade-off is
being tied to Postgres, which is fine when Postgres is the product decision.

**Q: How do you map DB errors to HTTP errors without leaking internals?**
The store converts driver errors to domain sentinels
(`fmt.Errorf("%w: %w", service.ErrConflict, err)`, Go 1.20 multi-wrap, keeping
the original for logs). The service turns known cases into a `*service.Error`
with a safe message. The handler maps the kind to a status with `errors.Is`
and the message with `errors.As`; anything else becomes a generic 500.

---

## 10. sqlc vs an ORM

**What.** sqlc reads your schema and **hand-written SQL** and generates
type-safe Go functions at build time. An ORM (GORM, ent) builds SQL from Go
code at runtime.

**In AssetFlow.** [`sqlc.yaml`](../sqlc.yaml) uses `db/migrations` as the schema
and `db/queries/*.sql` as the input, generating `internal/store/db`. Nullable
columns become pointers, and the LEFT JOIN columns came out nullable
automatically. CI runs `sqlc diff` so generated code can't drift from the SQL.

**Q: Why sqlc?**
You see the exact SQL (`EXPLAIN` it, tune it, use `FOR UPDATE SKIP LOCKED`,
`ON CONFLICT`, `ANY`); mistakes such as a wrong column or type fail at
generation, not in production; and there is no reflection or hidden lazy
loading, which is how ORMs produce N+1 without anyone noticing.

**Q: Downsides?**
Dynamic queries (arbitrary filter combinations) are awkward: you write
`COALESCE(sqlc.narg(x), col)`-style SQL, several queries, or a small query
builder for that one place. There's also more code to read, but it is generated.

**Q: Why not use the generated structs as your domain model?**
They mirror tables. The store maps them to service types, so the service
layer never imports pgx or sqlc and is tested with plain mocks.

---

## 11. Distroless images

**What.** A multi-stage Docker build compiles in a full toolchain image and
copies **only the binary** into a minimal runtime image.
`gcr.io/distroless/static` contains CA certificates, tzdata and `/etc/passwd`
entries: no shell, no package manager, no libc. `CGO_ENABLED=0` makes the Go
binary fully static so it needs nothing else.

**In AssetFlow.** [`Dockerfile`](../Dockerfile): `golang:1.23` (1.24 GB) builds
with `-trimpath -ldflags="-s -w"`; the runtime is
`distroless/static-debian12:nonroot`. Result: **api 24.1 MB**, worker 18.5 MB
(5.4 / 4.0 MB compressed). `docker run --entrypoint sh` fails, because there is no shell.

**Q: Why distroless?**
Small images pull and start faster. Fewer packages means fewer CVEs and less
for an attacker to use: there is no shell to land in after an exploit.
`nonroot` limits the damage further.

**Q: How do you debug it without a shell?**
Ephemeral debug containers (`kubectl debug -it pod --image=busybox
--target=app`), or `:debug` distroless tags in non-production. Mostly, design
for observability: structured logs with request IDs, `/healthz` and `/readyz`,
metrics.

**Q: distroless vs `scratch` vs alpine?**
`scratch` is empty; you would have to add CA certs and a user yourself.
Alpine has a shell and musl libc, which is fine but bigger and adds packages
to patch. distroless/static is scratch plus exactly the essentials.

---

## 12. Also used

**Errors as values.** Wrap with `%w`, check with `errors.Is` (sentinels such as
`service.ErrNotFound`) and `errors.As` (types such as `*service.Error`,
`*pgconn.PgError`, `*stripe.Error`). Expected outcomes are values, not
panics.

**Interfaces at boundaries, defined by the consumer.** `service.Store`,
`billing.Gateway`, `notify.Store`, `agent.LLM` and the handler-side
`AssetService` are declared where they are *used*: "accept interfaces, return
structs". This is what lets each layer be tested with a fake.
Gotcha: assigning a nil `*T` to an interface makes it non-nil, so `main`
only sets `Deps.Billing` when Stripe is configured.

**Table-driven tests and mocks.** Each case is a struct row, run with
`t.Run`. Mocks are hand-written, with function fields, and embed the
interface, so calling an unstubbed method panics loudly instead of returning
zero values.

**Race detector.** `go test -race` instruments memory accesses and reports
unsynchronised concurrent access at runtime. It runs on every test in CI;
concurrency tests are also repeated (`-count=20`) to shake out flakes.

**Row locks plus constraints.** Allocation runs `SELECT … FOR UPDATE` on the
asset, so concurrent allocations serialise, and a partial unique index
`UNIQUE (asset_id) WHERE returned_at IS NULL` is the backstop. In tests,
20 concurrent requests gave 1 success and 19 conflicts.

**JWT + RBAC.** HS256 with the algorithm pinned (`alg: none` is rejected),
expiry required, roles ranked viewer < manager < admin, enforced per route by
middleware. JWTs can't be revoked directly, so they are short-lived.

**Token-bucket rate limiting.** `x/time/rate` per client IP, with idle
buckets swept so the map cannot grow forever. `SetTrustedProxies(nil)` stops
clients from faking their IP with `X-Forwarded-For`.

**Transactional outbox.** Notification jobs are inserted in the same
transaction as the allocation or payment change, so there is never a
notification for a rolled-back change and never a lost one.

**LLM tool-calling safety.** The model only *proposes* calls. Permissions are
checked in Go against the caller's role before execution. Arguments are
decoded strictly, and errors go back to the model. Reads run in parallel and
writes in order. Each tool has a timeout and the loop has a step limit.
Secrets (the generated password) never enter the model's context. Prompt
injection through data is contained because the model can never exceed the
caller's own permissions.

**Measuring honestly.** Report p50/p95/p99, not averages (the tail is what users
feel); count queries in the database (`pg_stat_statements`); keep the
"before" code runnable; commit raw tool output; say when numbers are
simulated.

**Liveness vs readiness.** `/healthz` means the process is alive (restart it
if not). `/readyz` means its dependencies are reachable (stop sending traffic
if not). A DB outage should not trigger restarts.
