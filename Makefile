.PHONY: help run-api run-worker build build-api build-worker build-migrate \
	docker-up docker-down docker-logs \
	migrate-up migrate-down migrate-force migrate-new \
	test test-integration lint tidy

APP_NAME       := ticketing-backend
BIN_DIR        := bin
DATABASE_URL   ?= postgres://ticketing:ticketing@localhost:5432/ticketing?sslmode=disable
MIGRATIONS_DIR := migrations

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

## --- Run (local, no docker) ---

run-api: ## Run the HTTP API server
	go run ./cmd/api

run-worker: ## Run the background worker
	go run ./cmd/worker

## --- Build ---

build: build-api build-worker build-migrate ## Build all binaries into ./bin

build-api:
	go build -o $(BIN_DIR)/api ./cmd/api

build-worker:
	go build -o $(BIN_DIR)/worker ./cmd/worker

build-migrate:
	go build -o $(BIN_DIR)/migrate ./cmd/migrate

## --- Local infra ---

docker-up: ## Start Postgres, Redis, Redpanda, Adminer, Redpanda Console
	docker compose up -d

docker-down: ## Stop and remove local infra containers
	docker compose down

docker-logs: ## Tail logs from local infra
	docker compose logs -f

## --- Migrations (golang-migrate) ---

migrate-up: ## Apply all up migrations
	go run ./cmd/migrate -direction up

migrate-down: ## Roll back the last migration
	go run ./cmd/migrate -direction down -steps 1

migrate-new: ## Create a new migration pair: make migrate-new name=add_orders_table
	@if [ -z "$(name)" ]; then echo "usage: make migrate-new name=<migration_name>"; exit 1; fi
	@ts=$$(date +%Y%m%d%H%M%S); \
	touch $(MIGRATIONS_DIR)/$${ts}_$(name).up.sql $(MIGRATIONS_DIR)/$${ts}_$(name).down.sql; \
	echo "created $(MIGRATIONS_DIR)/$${ts}_$(name).{up,down}.sql"

## --- Quality ---

test: ## Run unit tests
	go test ./... -race -count=1

test-integration: ## Run integration tests (requires docker-up)
	go test ./test/integration/... -race -count=1 -tags=integration

lint: ## Run go vet + gofmt check
	gofmt -l . && go vet ./...

tidy: ## Tidy go.mod/go.sum
	go mod tidy
