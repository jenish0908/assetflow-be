-- Deterministic perf dataset: 10,000 assets, 200 users, ~18k allocations.
-- Run against the dedicated perf database only; it wipes asset data first.
--
-- Shape (no randomness, so every run produces the same data):
--   * asset i has (i % 4) returned allocations in its history
--   * every 3rd asset is currently allocated (one active allocation)
BEGIN;

CREATE EXTENSION IF NOT EXISTS pgcrypto;

TRUNCATE allocations, assets RESTART IDENTITY;
DELETE FROM users WHERE email LIKE 'seed-%@assetflow.dev' OR email = 'perf@assetflow.dev';

-- Login used by the load test. pgcrypto's bf hashes are bcrypt ($2a$),
-- which Go's bcrypt package verifies.
INSERT INTO users (email, name, password_hash, role)
VALUES ('perf@assetflow.dev', 'Perf', crypt('perf-pass-123', gen_salt('bf', 10)), 'viewer');

INSERT INTO users (email, name, password_hash, role)
SELECT format('seed-%s@assetflow.dev', g), format('Seed User %s', g), 'x', 'viewer'
FROM generate_series(1, 200) AS g;

INSERT INTO assets (tag, name, category, status)
SELECT
  format('SEED-%s', lpad(g::text, 5, '0')),
  format('Seed asset %s', g),
  (ARRAY['laptop', 'monitor', 'phone', 'dock'])[1 + g % 4],
  CASE WHEN g % 3 = 0 THEN 'allocated' ELSE 'available' END
FROM generate_series(1, 10000) AS g;

-- Returned history: asset i gets (i % 4) past allocations.
INSERT INTO allocations (asset_id, user_id, allocated_at, returned_at)
SELECT
  a.id,
  u.id,
  now() - interval '30 days' * (h + 1),
  now() - interval '30 days' * (h + 1) + interval '7 days'
FROM assets a
CROSS JOIN LATERAL generate_series(0, (a.id % 4) - 1) AS h
JOIN users u ON u.email = format('seed-%s@assetflow.dev', 1 + (a.id + h) % 200);

-- Active allocations for the assets marked allocated.
INSERT INTO allocations (asset_id, user_id, allocated_at)
SELECT a.id, u.id, now() - interval '1 day'
FROM assets a
JOIN users u ON u.email = format('seed-%s@assetflow.dev', 1 + a.id % 200)
WHERE a.status = 'allocated';

COMMIT;

ANALYZE assets;
ANALYZE allocations;

SELECT
  (SELECT count(*) FROM assets)      AS assets,
  (SELECT count(*) FROM allocations) AS allocations,
  (SELECT count(*) FROM users)       AS users;
