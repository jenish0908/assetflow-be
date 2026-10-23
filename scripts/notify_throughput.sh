#!/usr/bin/env bash
# Measures notification worker throughput (jobs/sec) and writes a markdown
# summary. Every M5 number in docs/perf.md comes from here.
#
# Usage:  scripts/notify_throughput.sh
# Env:    JOBS=5000
# Needs:  docker with compose v2, bash. Uses its own assetflow_bench DB.
set -euo pipefail
cd "$(dirname "$0")/.."
export MSYS_NO_PATHCONV=1

JOBS=${JOBS:-5000}
BENCH_DB=assetflow_bench
BENCH_URL="postgres://assetflow:assetflow@db:5432/${BENCH_DB}?sslmode=disable"
OUT_DIR=docs/perf-results
STAMP=$(date -u +%Y%m%dT%H%M%SZ)
SUMMARY="$OUT_DIR/${STAMP}-notify-throughput.md"
mkdir -p "$OUT_DIR"

psql() { docker compose exec -T db psql -U assetflow -v ON_ERROR_STOP=1 -qtA "$@"; }

echo ">> postgres + $BENCH_DB"
docker compose up -d --wait db >/dev/null
if [ -z "$(psql -d postgres -c "SELECT 1 FROM pg_database WHERE datname = '$BENCH_DB'")" ]; then
  psql -d postgres -c "CREATE DATABASE $BENCH_DB"
fi
MIGRATE_DATABASE_URL=$BENCH_URL docker compose run --rm migrate up 2>&1 | tail -1

bench() {
  docker run --rm --network assetflow_default \
    -v "$(pwd -W 2>/dev/null || pwd):/src" -v assetflow-gomod:/go/pkg/mod -w /src \
    -e DATABASE_URL="$BENCH_URL" golang:1.23 \
    go run ./cmd/notifybench -jobs "$JOBS" "$@"
}

{
  echo "# Notification worker throughput, $STAMP"
  echo
  echo "- Command: \`JOBS=$JOBS scripts/notify_throughput.sh\`"
  echo "- Each row: enqueue $JOBS jobs, start the worker(s), time until every job is marked done in Postgres"
  echo "- Handler = sleep(send latency), standing in for an email API call; 0s measures pure queue overhead"
  echo "- pgxpool max 20 connections per instance; claim batch <= 32; lease 2m"
  echo "- Machine: $(docker info --format '{{.NCPU}} CPUs, {{.MemTotal}} bytes RAM available to Docker ({{.OperatingSystem}})'); Postgres and workers in Docker on the same machine"
  echo
} > "$SUMMARY"

echo ">> 20ms send latency, one worker, growing pool"
bench -latency 20ms -concurrency 1,4,16,64 -header | tee -a "$SUMMARY"
echo ">> 20ms send latency, two worker instances sharing the table"
bench -latency 20ms -concurrency 16 -instances 2 | tee -a "$SUMMARY"
echo ">> no send latency (queue overhead only)"
bench -latency 0s -concurrency 16,64 | tee -a "$SUMMARY"

echo
echo ">> summary written to $SUMMARY"
cat "$SUMMARY"
