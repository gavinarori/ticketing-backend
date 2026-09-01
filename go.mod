module github.com/gavinarori/ticketing-backend

go 1.23

require (
	github.com/caarlos0/env/v11 v11.2.2
	github.com/go-chi/chi/v5 v5.1.0
	github.com/go-chi/cors v1.2.1
	github.com/go-playground/validator/v10 v10.22.1
	github.com/golang-jwt/jwt/v5 v5.2.1
	github.com/golang-migrate/migrate/v4 v4.18.1
	github.com/google/uuid v1.6.0
	github.com/jackc/pgx/v5 v5.7.1
	github.com/joho/godotenv v1.5.1
	github.com/redis/go-redis/v9 v9.6.1
	go.uber.org/zap v1.27.0
	golang.org/x/crypto v0.27.0
)

// Run `go mod tidy` after cloning — this file is hand-authored for scaffolding
// and indirect dependencies / checksums (go.sum) have not been resolved yet.
