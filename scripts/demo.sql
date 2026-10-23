-- Demo data for trying the UI: a small company's equipment, people,
-- allocation history, payments and delivered notifications.
-- Run with scripts/demo.sh (it uses its own assetflow_demo database).
-- Logins: admin@assetflow.dev / admin-pass-123, mgr@assetflow.dev /
-- mgr-pass-123, priya@assetflow.dev / priya-pass-123.
BEGIN;

CREATE EXTENSION IF NOT EXISTS pgcrypto;

TRUNCATE notification_jobs, stripe_events, payments, allocations, assets, users RESTART IDENTITY CASCADE;

INSERT INTO users (email, name, password_hash, role, created_at) VALUES
  ('admin@assetflow.dev',  'Aarav Mehta',     crypt('admin-pass-123', gen_salt('bf', 10)), 'admin',   now() - interval '400 days'),
  ('mgr@assetflow.dev',    'Sofia Lindqvist', crypt('mgr-pass-123',   gen_salt('bf', 10)), 'manager', now() - interval '380 days'),
  ('priya@assetflow.dev',  'Priya Shah',      crypt('priya-pass-123', gen_salt('bf', 10)), 'viewer',  now() - interval '300 days'),
  ('marcus@assetflow.dev', 'Marcus Okafor',   'x', 'viewer',  now() - interval '290 days'),
  ('elena@assetflow.dev',  'Elena Petrova',   'x', 'manager', now() - interval '250 days'),
  ('kenji@assetflow.dev',  'Kenji Watanabe',  'x', 'viewer',  now() - interval '240 days'),
  ('amara@assetflow.dev',  'Amara Nwosu',     'x', 'viewer',  now() - interval '200 days'),
  ('lucas@assetflow.dev',  'Lucas Moreau',    'x', 'viewer',  now() - interval '180 days'),
  ('fatima@assetflow.dev', 'Fatima Al-Sayed', 'x', 'viewer',  now() - interval '150 days'),
  ('diego@assetflow.dev',  'Diego Fernández', 'x', 'viewer',  now() - interval '120 days'),
  ('hannah@assetflow.dev', 'Hannah Becker',   'x', 'viewer',  now() - interval '90 days'),
  ('rohan@assetflow.dev',  'Rohan Iyer',      'x', 'viewer',  now() - interval '45 days');

INSERT INTO assets (tag, name, category, status, created_at, updated_at)
SELECT tag, name, category, 'available', now() - make_interval(days => d), now() - make_interval(days => d)
FROM (VALUES
  ('L-101', 'MacBook Pro 14 M3',          'laptop',  360),
  ('L-102', 'MacBook Pro 14 M4',          'laptop',  340),
  ('L-103', 'MacBook Air 13 M3',          'laptop',  330),
  ('L-104', 'ThinkPad X1 Carbon Gen 11',  'laptop',  320),
  ('L-105', 'Dell Latitude 7440',         'laptop',  300),
  ('L-106', 'MacBook Pro 16 M3 Max',      'laptop',  290),
  ('L-107', 'ThinkPad T14s Gen 4',        'laptop',  280),
  ('L-108', 'Dell XPS 13 Plus',           'laptop',  260),
  ('L-109', 'MacBook Air 15 M3',          'laptop',  200),
  ('L-110', 'Framework Laptop 13',        'laptop',  150),
  ('L-111', 'ThinkPad P1 Gen 6',          'laptop',  120),
  ('L-112', 'MacBook Pro 14 M4 Pro',      'laptop',   20),
  ('M-201', 'Dell UltraSharp U2723QE',    'monitor', 350),
  ('M-202', 'Dell UltraSharp U2723QE',    'monitor', 350),
  ('M-203', 'LG UltraFine 27UN880',       'monitor', 310),
  ('M-204', 'Apple Studio Display',       'monitor', 270),
  ('M-205', 'BenQ PD2706U',               'monitor', 230),
  ('M-206', 'Dell UltraSharp U3423WE',    'monitor', 160),
  ('M-207', 'LG UltraFine 27UN880',       'monitor',  80),
  ('P-301', 'iPhone 15',                  'phone',   330),
  ('P-302', 'iPhone 15 Pro',              'phone',   300),
  ('P-303', 'Pixel 8',                    'phone',   260),
  ('P-304', 'Samsung Galaxy S24',         'phone',   180),
  ('P-305', 'iPhone 16',                  'phone',    30),
  ('D-401', 'CalDigit TS4 dock',          'dock',    340),
  ('D-402', 'CalDigit TS4 dock',          'dock',    340),
  ('D-403', 'Anker 777 USB-C dock',       'dock',    200),
  ('T-501', 'iPad Pro 11 M4',             'tablet',  140),
  ('T-502', 'iPad Air 13',                'tablet',   60),
  ('L-090', 'MacBook Pro 13 Intel 2019',  'laptop',  900),
  ('L-091', 'Dell Latitude 7400',         'laptop',  880),
  ('M-190', 'Dell P2419H',                'monitor', 870)
) AS v(tag, name, category, d);

-- Allocation history: (tag, holder email, allocated days ago, returned days ago or NULL)
INSERT INTO allocations (asset_id, user_id, allocated_at, returned_at)
SELECT a.id, u.id, now() - make_interval(days => h.from_d, hours => h.from_d % 9),
       CASE WHEN h.to_d IS NULL THEN NULL ELSE now() - make_interval(days => h.to_d, hours => h.to_d % 7) END
FROM (VALUES
  ('L-101', 'marcus@assetflow.dev', 350, 200), ('L-101', 'kenji@assetflow.dev', 190, NULL),
  ('L-102', 'priya@assetflow.dev',  330, NULL),
  ('L-103', 'amara@assetflow.dev',  320, 100), ('L-103', 'rohan@assetflow.dev', 40, NULL),
  ('L-104', 'lucas@assetflow.dev',  300, NULL),
  ('L-105', 'fatima@assetflow.dev', 140, NULL),
  ('L-106', 'elena@assetflow.dev',  280, NULL),
  ('L-107', 'diego@assetflow.dev',  110, 15),
  ('L-108', 'hannah@assetflow.dev', 85, NULL),
  ('L-109', 'mgr@assetflow.dev',    190, NULL),
  ('M-201', 'priya@assetflow.dev',  320, NULL),
  ('M-202', 'marcus@assetflow.dev', 300, NULL),
  ('M-203', 'kenji@assetflow.dev',  180, NULL),
  ('M-204', 'elena@assetflow.dev',  260, NULL),
  ('M-206', 'amara@assetflow.dev',  150, 60),
  ('P-301', 'admin@assetflow.dev',  320, NULL),
  ('P-302', 'mgr@assetflow.dev',    290, NULL),
  ('P-303', 'diego@assetflow.dev',  110, NULL),
  ('P-304', 'lucas@assetflow.dev',  170, 20),
  ('D-401', 'priya@assetflow.dev',  320, NULL),
  ('D-402', 'hannah@assetflow.dev', 80, NULL),
  ('T-501', 'fatima@assetflow.dev', 130, NULL),
  ('L-090', 'marcus@assetflow.dev', 880, 360), ('L-091', 'lucas@assetflow.dev', 860, 300),
  ('L-112', 'rohan@assetflow.dev',  3, NULL)
) AS h(tag, email, from_d, to_d)
JOIN assets a ON a.tag = h.tag
JOIN users u ON u.email = h.email;

UPDATE assets SET status = 'allocated'
WHERE id IN (SELECT asset_id FROM allocations WHERE returned_at IS NULL);
UPDATE assets SET status = 'maintenance', updated_at = now() - interval '6 days' WHERE tag IN ('L-107', 'M-206', 'P-304');
UPDATE assets SET status = 'retired', updated_at = now() - interval '300 days' WHERE tag IN ('L-090', 'L-091', 'M-190');
UPDATE assets a SET updated_at = greatest(a.updated_at, al.allocated_at)
FROM allocations al WHERE al.asset_id = a.id AND al.returned_at IS NULL;

-- Payments (as if made through the simulated processor) and their delivered notifications.
INSERT INTO payments (user_id, idempotency_key, payment_intent_id, amount_cents, currency, status, created_at, updated_at)
SELECT u.id, 'demo-' || p.n, 'pi_demo_' || p.n, p.cents, 'usd', p.status,
       now() - make_interval(days => p.d), now() - make_interval(days => p.d)
FROM (VALUES
  (1, 'priya@assetflow.dev',  4900,  'succeeded', 40),
  (2, 'marcus@assetflow.dev', 12900, 'succeeded', 31),
  (3, 'priya@assetflow.dev',  2500,  'succeeded', 12),
  (4, 'kenji@assetflow.dev',  7900,  'requires_payment_method', 9),
  (5, 'mgr@assetflow.dev',    19900, 'succeeded', 4)
) AS p(n, email, cents, status, d)
JOIN users u ON u.email = p.email
ORDER BY p.n;

INSERT INTO notification_jobs (kind, payload, status, attempts, run_at, created_at, updated_at)
SELECT 'payment_succeeded',
       jsonb_build_object('payment_id', p.id, 'user_id', p.user_id, 'amount_cents', p.amount_cents, 'currency', p.currency),
       'done', 1, p.created_at, p.created_at, p.created_at
FROM payments p WHERE p.status = 'succeeded';

INSERT INTO notification_jobs (kind, payload, status, attempts, run_at, created_at, updated_at)
SELECT 'asset_allocated',
       jsonb_build_object('asset_id', a.id, 'asset_tag', a.tag, 'user_id', al.user_id),
       'done', CASE WHEN a.tag = 'L-108' THEN 2 ELSE 1 END, al.allocated_at, al.allocated_at, al.allocated_at
FROM allocations al JOIN assets a ON a.id = al.asset_id
WHERE al.allocated_at > now() - interval '120 days';

COMMIT;

SELECT (SELECT count(*) FROM users) AS users, (SELECT count(*) FROM assets) AS assets,
       (SELECT count(*) FROM allocations) AS allocations, (SELECT count(*) FROM payments) AS payments;
