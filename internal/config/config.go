// Package config centralizes all environment-driven configuration for the
// ticketing platform. Every other package (platform, service, handler) should
// receive its settings via this struct rather than reading os.Getenv directly
// — that keeps config parsing/validation in one place and makes services
// trivially testable with a hand-built Config.
package config

import (
	"fmt"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/go-playground/validator/v10"
	"github.com/joho/godotenv"
)

// Config is the root configuration object for every binary in this repo
// (cmd/api, cmd/worker, cmd/migrate). Binaries that don't need a section
// (e.g. migrate doesn't need Stripe) simply ignore it.
type Config struct {
	App      AppConfig
	HTTP     HTTPConfig
	Postgres PostgresConfig
	Redis    RedisConfig
	Kafka    KafkaConfig
	JWT      JWTConfig
	Stripe   StripeConfig
	Adyen    AdyenConfig
}

type AppConfig struct {
	Env      string `env:"APP_ENV" envDefault:"development" validate:"oneof=development staging production"`
	Name     string `env:"APP_NAME" envDefault:"ticketing-backend"`
	LogLevel string `env:"LOG_LEVEL" envDefault:"info" validate:"oneof=debug info warn error"`
}

func (a AppConfig) IsProduction() bool { return a.Env == "production" }

type HTTPConfig struct {
	Port            int           `env:"HTTP_PORT" envDefault:"8080" validate:"required,min=1,max=65535"`
	ReadTimeout     time.Duration `env:"HTTP_READ_TIMEOUT" envDefault:"10s"`
	WriteTimeout    time.Duration `env:"HTTP_WRITE_TIMEOUT" envDefault:"15s"`
	IdleTimeout     time.Duration `env:"HTTP_IDLE_TIMEOUT" envDefault:"60s"`
	ShutdownTimeout time.Duration `env:"HTTP_SHUTDOWN_TIMEOUT" envDefault:"15s"`
}

type PostgresConfig struct {
	// DSN is the single source of truth for connecting to Postgres, the
	// system of record for tenants, events, seat inventory, and orders.
	DSN             string        `env:"DATABASE_URL,required" validate:"required"`
	MaxOpenConns    int32         `env:"DB_MAX_OPEN_CONNS" envDefault:"25" validate:"min=1"`
	MaxIdleConns    int32         `env:"DB_MAX_IDLE_CONNS" envDefault:"10" validate:"min=0"`
	ConnMaxLifetime time.Duration `env:"DB_CONN_MAX_LIFETIME" envDefault:"30m"`
}

type RedisConfig struct {
	// Addr backs distributed seat locks, the virtual waiting room, and
	// rate limiting — all on the hot path of ticket purchase, so keep this
	// pool sized generously (see PoolSize) relative to expected concurrency.
	Addr     string `env:"REDIS_ADDR" envDefault:"localhost:6379" validate:"required"`
	Password string `env:"REDIS_PASSWORD"`
	DB       int    `env:"REDIS_DB" envDefault:"0"`
	PoolSize int    `env:"REDIS_POOL_SIZE" envDefault:"50" validate:"min=1"`
}

type KafkaConfig struct {
	Brokers       []string `env:"KAFKA_BROKERS" envSeparator:"," envDefault:"localhost:9092"`
	ConsumerGroup string   `env:"KAFKA_CONSUMER_GROUP" envDefault:"ticketing-worker"`
}

type JWTConfig struct {
	Secret        string        `env:"JWT_SECRET,required" validate:"required,min=16"`
	RefreshSecret string        `env:"JWT_REFRESH_SECRET,required" validate:"required,min=16"`
	AccessTTL     time.Duration `env:"JWT_ACCESS_TTL" envDefault:"15m"`
	RefreshTTL    time.Duration `env:"JWT_REFRESH_TTL" envDefault:"720h"`
}

type StripeConfig struct {
	SecretKey     string `env:"STRIPE_SECRET_KEY"`
	WebhookSecret string `env:"STRIPE_WEBHOOK_SECRET"`
}

type AdyenConfig struct {
	APIKey          string `env:"ADYEN_API_KEY"`
	MerchantAccount string `env:"ADYEN_MERCHANT_ACCOUNT"`
}

// Load reads a .env file if present (local dev convenience — production
// deployments should inject real environment variables instead), parses
// every field above from the environment, and validates the result.
//
// It intentionally fails fast: a misconfigured deployment should never
// start serving traffic, since that risks the one thing this system cannot
// tolerate — selling a seat that doesn't exist.
func Load() (*Config, error) {
	// Ignore error: .env is optional (e.g. absent in production containers
	// where config comes from the orchestrator instead).
	_ = godotenv.Load()

	cfg := &Config{}
	if err := env.Parse(cfg); err != nil {
		return nil, fmt.Errorf("config: parse env: %w", err)
	}

	if err := validator.New().Struct(cfg); err != nil {
		return nil, fmt.Errorf("config: validate: %w", err)
	}

	return cfg, nil
}
