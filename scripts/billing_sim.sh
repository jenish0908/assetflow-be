#!/usr/bin/env bash
# Runs cmd/billingsim with the safeguards off ("before") and on ("after") and
# writes a markdown summary. Every M4 number in docs/perf.md comes from here.
#
# Usage:  scripts/billing_sim.sh
# Env:    PAYMENTS=200 SEED=42 LOST_RATE=0.2 DUP_RATE=0.3 WORKERS=8
# Needs:  docker with compose v2, bash. Uses its own assetflow_billing_sim DB.
set -euo pipefail
cd "$(dirname "$0")/.."
export MSYS_NO_PATHCONV=1

PAYMENTS=${PAYMENTS:-200}
SEED=${SEED:-42}
LOST_RATE=${LOST_RATE:-0.2}
DUP_RATE=${DUP_RATE:-0.3}
WORKERS=${WORKERS:-8}
SIM_DB=assetflow_billing_sim
SIM_URL="postgres://assetflow:assetflow@db:5432/${SIM_DB}?sslmode=disable"
OUT_DIR=docs/perf-results
STAMP=$(date -u +%Y%m%dT%H%M%SZ)
SUMMARY="$OUT_DIR/${STAMP}-billing-sim.md"
mkdir -p "$OUT_DIR"

psql() { docker compose exec -T db psql -U assetflow -v ON_ERROR_STOP=1 -qtA "$@"; }

echo ">> postgres + $SIM_DB"
docker compose up -d --wait db >/dev/null
if [ -z "$(psql -d postgres -c "SELECT 1 FROM pg_database WHERE datname = '$SIM_DB'")" ]; then
  psql -d postgres -c "CREATE DATABASE $SIM_DB"
fi
MIGRATE_DATABASE_URL=$SIM_URL docker compose run --rm migrate up 2>&1 | tail -1

{
  echo "# Billing reconciliation: before vs after safeguards, $STAMP"
  echo
  echo "- Command: \`PAYMENTS=$PAYMENTS SEED=$SEED LOST_RATE=$LOST_RATE DUP_RATE=$DUP_RATE WORKERS=$WORKERS scripts/billing_sim.sh\`"
  echo "- Simulated: real HTTP API + Postgres + signature verification against an in-memory Stripe fake"
  echo "- Injected: $LOST_RATE of Stripe create responses lost (intent created, caller times out);"
  echo "  webhooks shuffled; $DUP_RATE delivered twice, back to back on $WORKERS concurrent workers"
  echo
  echo "| mode | Stripe intents | DB payments | orphan intents | of which charged | status mismatches | amount mismatches | missing intents | stuck pending | notification issues | total problems |"
  echo "|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|"
} > "$SUMMARY"

for mode in before after; do
  echo ">> billingsim -mode $mode"
  raw="$OUT_DIR/${STAMP}-billing-sim-${mode}.txt"
  docker run --rm --network assetflow_default \
    -v "$(pwd -W 2>/dev/null || pwd):/src" -v assetflow-gomod:/go/pkg/mod -w /src \
    -e DATABASE_URL="$SIM_URL" golang:1.23 \
    go run ./cmd/billingsim -mode "$mode" -payments "$PAYMENTS" -seed "$SEED" \
      -lost-rate "$LOST_RATE" -dup-rate "$DUP_RATE" -workers "$WORKERS" > "$raw"
  sed 's/^/   /' "$raw" | grep -v '^   SUMMARY'
  grep '^SUMMARY|' "$raw" | awk -F'|' '{printf "| %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s |\n", $2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12}' >> "$SUMMARY"
done

echo
echo ">> summary written to $SUMMARY"
cat "$SUMMARY"
