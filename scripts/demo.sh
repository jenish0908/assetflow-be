#!/usr/bin/env bash
# Starts the API and worker on a fresh demo database (assetflow_demo) filled
# with realistic sample data, for trying the UI. Your dev database is untouched.
#
# Usage:  scripts/demo.sh            (API on :8080)
#         API_PORT=8081 scripts/demo.sh
set -euo pipefail
cd "$(dirname "$0")/.."
export MSYS_NO_PATHCONV=1

DEMO_DB=assetflow_demo
DEMO_URL="postgres://assetflow:assetflow@db:5432/${DEMO_DB}?sslmode=disable"
psql() { docker compose exec -T db psql -U assetflow -v ON_ERROR_STOP=1 -qtA "$@"; }

docker compose up -d --wait db >/dev/null
if [ -z "$(psql -d postgres -c "SELECT 1 FROM pg_database WHERE datname = '$DEMO_DB'")" ]; then
  psql -d postgres -c "CREATE DATABASE $DEMO_DB"
fi
MIGRATE_DATABASE_URL=$DEMO_URL docker compose run --rm migrate up 2>&1 | tail -1
echo "seeded (users|assets|allocations|payments): $(psql -d "$DEMO_DB" < scripts/demo.sql | tail -1)"

API_DATABASE_URL=$DEMO_URL docker compose --profile app up -d --build api worker
echo "API on http://localhost:${API_PORT:-8080}  (admin@assetflow.dev / admin-pass-123)"
