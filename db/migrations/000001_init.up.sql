CREATE TABLE users (
  id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  email         TEXT NOT NULL UNIQUE,
  name          TEXT NOT NULL,
  password_hash TEXT NOT NULL,
  role          TEXT NOT NULL CHECK (role IN ('admin', 'manager', 'viewer')),
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE assets (
  id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  tag        TEXT NOT NULL UNIQUE,
  name       TEXT NOT NULL,
  category   TEXT NOT NULL,
  status     TEXT NOT NULL DEFAULT 'available'
             CHECK (status IN ('available', 'allocated', 'maintenance', 'retired')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE allocations (
  id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  asset_id     BIGINT NOT NULL REFERENCES assets (id),
  user_id      BIGINT NOT NULL REFERENCES users (id),
  allocated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  returned_at  TIMESTAMPTZ
);

CREATE INDEX allocations_asset_id_idx ON allocations (asset_id);

-- At most one active (not yet returned) allocation per asset.
CREATE UNIQUE INDEX allocations_one_active_idx ON allocations (asset_id)
  WHERE returned_at IS NULL;

CREATE TABLE payments (
  id                BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id           BIGINT NOT NULL REFERENCES users (id),
  idempotency_key   TEXT NOT NULL UNIQUE,
  payment_intent_id TEXT UNIQUE,
  amount_cents      BIGINT NOT NULL CHECK (amount_cents > 0),
  currency          TEXT NOT NULL DEFAULT 'usd',
  status            TEXT NOT NULL DEFAULT 'pending',
  -- Stripe event.created of the last applied webhook; older events are ignored.
  last_event_at     TIMESTAMPTZ,
  created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Primary key on the Stripe event ID is what dedupes webhook deliveries.
CREATE TABLE stripe_events (
  id           TEXT PRIMARY KEY,
  type         TEXT NOT NULL,
  processed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE notification_jobs (
  id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  kind         TEXT NOT NULL,
  payload      JSONB NOT NULL,
  status       TEXT NOT NULL DEFAULT 'pending'
               CHECK (status IN ('pending', 'running', 'done', 'dead')),
  attempts     INT NOT NULL DEFAULT 0,
  max_attempts INT NOT NULL DEFAULT 5,
  run_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_error   TEXT,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX notification_jobs_claim_idx ON notification_jobs (run_at)
  WHERE status = 'pending';
