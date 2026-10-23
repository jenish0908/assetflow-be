-include .env
export

.PHONY: help up down migrate-up migrate-down sqlc run-api run-worker app-up test test-integration lint \
	docker-build perf-list billing-sim notify-bench reconcile

help: ## list targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-18s %s\n", $$1, $$2}'

up: ## start Postgres and wait until it is healthy
	docker compose up -d --wait db

down: ## stop everything (keeps the data volume)
	docker compose --profile app down

migrate-up: ## apply migrations to the dev database
	docker compose run --rm migrate up

migrate-down: ## roll back the last migration
	docker compose run --rm migrate down 1

sqlc: ## regenerate internal/store/db from db/queries
	docker compose run --rm sqlc generate

run-api: ## run the API locally (needs DATABASE_URL, JWT_SECRET)
	go run ./cmd/api

run-worker: ## run the notification worker locally
	go run ./cmd/worker

app-up: up migrate-up ## run API + worker in Docker on :8080
	docker compose --profile app up -d --build api worker

test: ## unit tests with the race detector
	go test -race ./...

TEST_DATABASE_URL ?= postgres://assetflow:assetflow@localhost:5432/assetflow_test?sslmode=disable

test-integration: up ## unit + integration tests against a real Postgres
	docker compose exec -T db psql -U assetflow -d postgres -tAc "SELECT 1 FROM pg_database WHERE datname='assetflow_test'" | grep -q 1 \
		|| docker compose exec -T db psql -U assetflow -d postgres -c "CREATE DATABASE assetflow_test"
	MIGRATE_DATABASE_URL=postgres://assetflow:assetflow@db:5432/assetflow_test?sslmode=disable docker compose run --rm migrate up
	TEST_DATABASE_URL="$(TEST_DATABASE_URL)" go test -race -count=1 ./...

lint: ## golangci-lint (same version as CI)
	docker run --rm -v "$(CURDIR):/src" -w /src golangci/golangci-lint:v1.61.0 golangci-lint run ./...

docker-build: ## build the api and worker images and print their sizes
	docker build -t assetflow-api:local .
	docker build --build-arg CMD=worker -t assetflow-worker:local .
	docker image ls --format '{{.Repository}}:{{.Tag}} {{.Size}}' | grep -E 'assetflow-(api|worker):local'

perf-list: ## M3 load test: N+1 vs batched vs JOIN
	bash scripts/perf_list_assets.sh

billing-sim: ## M4 reconciliation before/after idempotency + webhook dedupe
	bash scripts/billing_sim.sh

notify-bench: ## M5 worker throughput
	bash scripts/notify_throughput.sh

reconcile: ## compare real Stripe (STRIPE_SECRET_KEY) with the DB
	go run ./cmd/reconcile -since 24h
