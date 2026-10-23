#!/usr/bin/env bash
# Load-tests GET /assets once per list strategy and writes a markdown summary.
# Every number in docs/perf.md (M3 section) comes from this script.
#
# Usage:  scripts/perf_list_assets.sh [strategy ...]    default: nplus1 batched join
# Env:    DURATION=30s  CONCURRENCY=20  PAGE_SIZE=50  API_PORT=8080 (host port)
# Needs:  docker with compose v2, bash, curl. Uses a separate database
#         (assetflow_perf), so your dev data is not touched.
#
# What it measures, per strategy:
#   latency    hey's p50/p95/p99 over DURATION with CONCURRENCY workers
#   queries    pg_stat_statements calls during the run / successful requests
set -euo pipefail
cd "$(dirname "$0")/.."
export MSYS_NO_PATHCONV=1 # stop Git Bash on Windows rewriting /paths

if [ $# -eq 0 ]; then set -- nplus1 batched join; fi
API_PORT=${API_PORT:-8080}
export API_PORT
DURATION=${DURATION:-30s}
CONCURRENCY=${CONCURRENCY:-20}
PAGE_SIZE=${PAGE_SIZE:-50}
PERF_DB=assetflow_perf
PERF_URL="postgres://assetflow:assetflow@db:5432/${PERF_DB}?sslmode=disable"
OUT_DIR=docs/perf-results
STAMP=$(date -u +%Y%m%dT%H%M%SZ)
SUMMARY="$OUT_DIR/${STAMP}-list-assets.md"
mkdir -p "$OUT_DIR"

psql() { docker compose exec -T db psql -U assetflow -v ON_ERROR_STOP=1 -qtA "$@"; }

echo ">> postgres"
docker compose up -d --wait db >/dev/null

echo ">> preparing $PERF_DB (migrate + seed)"
if [ -z "$(psql -d postgres -c "SELECT 1 FROM pg_database WHERE datname = '$PERF_DB'")" ]; then
  psql -d postgres -c "CREATE DATABASE $PERF_DB"
fi
MIGRATE_DATABASE_URL=$PERF_URL docker compose run --rm migrate up 2>&1 | tail -1
psql -d "$PERF_DB" -c "CREATE EXTENSION IF NOT EXISTS pg_stat_statements"
# Run as its own command (not inside $(...)) so a seed error stops the script.
psql -d "$PERF_DB" < scripts/seed.sql > /tmp/assetflow-seed.out
echo "   seeded (assets|allocations|users): $(tail -1 /tmp/assetflow-seed.out)"

echo ">> building api image"
docker compose --profile app build api >/dev/null

{
  echo "# GET /assets load test, $STAMP"
  echo
  echo "- Command: \`DURATION=$DURATION CONCURRENCY=$CONCURRENCY PAGE_SIZE=$PAGE_SIZE scripts/perf_list_assets.sh $*\`"
  echo "- Request: \`GET /assets?limit=$PAGE_SIZE\`, $CONCURRENCY concurrent workers for $DURATION (after a 500-request warm-up)"
  echo "- Data: $(psql -d "$PERF_DB" -c "SELECT count(*) || ' assets, ' || (SELECT count(*) FROM allocations) || ' allocations' FROM assets")"
  echo "- Machine: $(docker info --format '{{.NCPU}} CPUs, {{.MemTotal}} bytes RAM available to Docker ({{.OperatingSystem}})')"
  echo "- Everything (Postgres, API, load generator) runs in Docker on the same machine."
  echo
  echo "| strategy | requests | req/s | p50 (ms) | p95 (ms) | p99 (ms) | DB queries / request |"
  echo "|---|---:|---:|---:|---:|---:|---:|"
} > "$SUMMARY"

for strategy in "$@"; do
  echo ">> $strategy: starting api"
  API_DATABASE_URL=$PERF_URL LIST_STRATEGY=$strategy LOG_LEVEL=warn \
    RATE_LIMIT_RPS=1000000 RATE_LIMIT_BURST=1000000 \
    docker compose --profile app up -d --force-recreate api >/dev/null 2>&1
  for _ in $(seq 1 60); do
    curl -sf "localhost:$API_PORT/readyz" >/dev/null && break
    sleep 1
  done

  if ! login=$(curl -sf -X POST "localhost:$API_PORT/auth/login" \
    -d '{"email":"perf@assetflow.dev","password":"perf-pass-123"}'); then
    echo "ERROR: login failed; is the api up and the perf user seeded?" >&2
    docker compose --profile app logs --tail 20 api >&2
    exit 1
  fi
  token=$(echo "$login" | sed 's/.*"token":"\([^"]*\)".*/\1/')
  url="http://api:8080/assets?limit=$PAGE_SIZE"

  echo "   warm-up"
  docker compose run --rm hey -n 500 -c 10 -H "Authorization: Bearer $token" "$url" >/dev/null

  psql -d "$PERF_DB" -c "SELECT pg_stat_statements_reset()" >/dev/null
  echo "   measuring for $DURATION"
  raw="$OUT_DIR/${STAMP}-list-assets-${strategy}.txt"
  docker compose run --rm hey -z "$DURATION" -c "$CONCURRENCY" -H "Authorization: Bearer $token" "$url" > "$raw"

  queries=$(psql -d "$PERF_DB" -c "
    SELECT coalesce(sum(s.calls), 0)
    FROM pg_stat_statements s JOIN pg_database d ON d.oid = s.dbid
    WHERE d.datname = '$PERF_DB' AND s.query NOT ILIKE '%pg_stat_statements%'")
  echo "pg_stat_statements calls during run: $queries" >> "$raw"

  ok=$(awk '/\[200\]/ {print $2}' "$raw")
  if grep -E '^\s+\[[0-9]+\]' "$raw" | grep -vq '\[200\]'; then
    echo "   WARNING: non-200 responses, see $raw"
  fi
  rps=$(awk '/Requests\/sec:/ {printf "%.0f", $2}' "$raw")
  pct() { awk -v p="$1%" '$1 == p && $2 == "in" {printf "%.1f", $3 * 1000}' "$raw"; }
  qpr=$(awk -v q="$queries" -v n="$ok" 'BEGIN {printf "%.2f", q / n}')

  row="| $strategy | $ok | $rps | $(pct 50) | $(pct 95) | $(pct 99) | $qpr |"
  echo "   $row"
  echo "$row" >> "$SUMMARY"
done

docker compose --profile app stop api >/dev/null 2>&1
echo
echo ">> summary written to $SUMMARY"
cat "$SUMMARY"
