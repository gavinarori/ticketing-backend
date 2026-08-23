// Command worker runs background processing: Kafka consumers for order
// events, notification dispatch (email/SMS), inventory-lock expiry sweeps,
// and other async work that must not block the request path in cmd/api.
//
// This is currently a skeleton — it wires up config/logger/infra and blocks
// on shutdown signals. Kafka consumer groups get added here once
// internal/platform/kafka and the domain event topics are defined.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"go.uber.org/zap"

	"github.com/gavinarori/ticketing-backend/internal/config"
	"github.com/gavinarori/ticketing-backend/internal/pkg/logger"
	"github.com/gavinarori/ticketing-backend/internal/platform/database"
	appredis "github.com/gavinarori/ticketing-backend/internal/platform/redis"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "worker: fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	log, err := logger.New(cfg.App.Env, cfg.App.LogLevel)
	if err != nil {
		return fmt.Errorf("build logger: %w", err)
	}
	defer func() { _ = log.Sync() }()

	log.Info("starting worker", zap.String("env", cfg.App.Env), zap.Strings("kafka_brokers", cfg.Kafka.Brokers))

	dbPool, err := database.NewPool(ctx, cfg.Postgres)
	if err != nil {
		return fmt.Errorf("connect postgres: %w", err)
	}
	defer dbPool.Close()

	redisClient, err := appredis.NewClient(ctx, cfg.Redis)
	if err != nil {
		return fmt.Errorf("connect redis: %w", err)
	}
	defer func() { _ = redisClient.Close() }()

	log.Info("worker ready — no consumers registered yet")

	// TODO(next task): start Kafka consumer group(s) here, e.g.
	//   consumer := kafka.NewConsumer(cfg.Kafka, log, dbPool, redisClient)
	//   go consumer.Run(ctx)

	<-ctx.Done()
	log.Info("shutdown signal received, worker stopping")
	return nil
}
