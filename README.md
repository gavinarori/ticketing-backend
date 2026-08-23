# ticketing-backend

Multi-tenant, high-concurrency sports ticketing platform backend (Go). Built to
handle flash ticket-drop traffic (50k–200k+ concurrent users) without ever
overselling a seat.

## Stack

- **Language:** Go 1.23
- **HTTP:** go-chi/chi
- **DB:** PostgreSQL (source of truth) via pgx/pgxpool
- **Cache/Locks:** Redis (distributed locks, waiting room, rate limiting) via go-redis
- **Messaging:** Kafka-compatible (Redpanda locally)
- **Migrations:** golang-migrate
- **Logging:** uber-go/zap
- **Config:** caarlos0/env + go-playground/validator

## Project layout

```
cmd/            entrypoints: api, worker, migrate
internal/
  config/       env loading & validation
  domain/       core business entities & interfaces (no external deps)
  repository/   postgres + redis implementations of domain interfaces
  service/      business logic (event, inventory, order, payment, ...)
  handler/      HTTP transport layer + middleware
  platform/     infra clients: db, redis, kafka, payment providers
  pkg/          shared utilities (logger, response envelope, ...)
migrations/     SQL migrations (golang-migrate)
deployments/    Docker + Kubernetes manifests
docs/           architecture notes, incl. the inventory-locking design
```

## Getting started

1. **Copy env file**
   ```bash
   cp .env.example .env
   ```

2. **Start local infra** (Postgres, Redis, Redpanda, Adminer, Redpanda Console)
   ```bash
   make docker-up
   ```

3. **Install deps**
   ```bash
   go mod tidy
   ```

4. **Run migrations** (once migrations exist under `migrations/`)
   ```bash
   make migrate-up
   ```

5. **Run the API**
   ```bash
   make run-api
   ```
   Check it's alive:
   ```bash
   curl localhost:8080/healthz
   curl localhost:8080/readyz
   ```

6. **Run the worker** (separate terminal)
   ```bash
   make run-worker
   ```

## Local dev UIs

- Adminer (Postgres UI): http://localhost:8082
- Redpanda Console (Kafka UI): http://localhost:8081

## Status

Project scaffolding only — see `docs/` for design notes as they're added.
Next up: database schema & migrations, domain layer, inventory locking service.
