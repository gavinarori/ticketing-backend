// Command api runs the HTTP API server: the synchronous request path for
// browsing events, reserving seats, and placing orders.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/gavinarori/ticketing-backend/internal/config"
	apphttp "github.com/gavinarori/ticketing-backend/internal/handler/http"
	"github.com/gavinarori/ticketing-backend/internal/pkg/logger"
	"github.com/gavinarori/ticketing-backend/internal/platform/database"
	appredis "github.com/gavinarori/ticketing-backend/internal/platform/redis"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "api: fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	// Root context cancelled on SIGINT/SIGTERM — this is what triggers
	// graceful shutdown below.
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

	log.Info("starting api server", zap.String("env", cfg.App.Env), zap.Int("port", cfg.HTTP.Port))

	dbPool, err := database.NewPool(ctx, cfg.Postgres)
	if err != nil {
		return fmt.Errorf("connect postgres: %w", err)
	}
	defer dbPool.Close()
	log.Info("connected to postgres")

	redisClient, err := appredis.NewClient(ctx, cfg.Redis)
	if err != nil {
		return fmt.Errorf("connect redis: %w", err)
	}
	defer func() { _ = redisClient.Close() }()
	log.Info("connected to redis")

	healthHandler := apphttp.NewHealthHandler(dbPool, redisClient)
	router := apphttp.NewRouter(apphttp.RouterDeps{
		Cfg:    cfg,
		Log:    log,
		Health: healthHandler,
	})

	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.HTTP.Port),
		Handler:      router,
		ReadTimeout:  cfg.HTTP.ReadTimeout,
		WriteTimeout: cfg.HTTP.WriteTimeout,
		IdleTimeout:  cfg.HTTP.IdleTimeout,
	}

	// Run the server in a goroutine so the main goroutine can block on
	// ctx.Done() and drive graceful shutdown below.
	serveErrCh := make(chan error, 1)
	go func() {
		log.Info("http server listening", zap.String("addr", srv.Addr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErrCh <- err
			return
		}
		serveErrCh <- nil
	}()

	select {
	case err := <-serveErrCh:
		if err != nil {
			return fmt.Errorf("http server: %w", err)
		}
	case <-ctx.Done():
		log.Info("shutdown signal received, draining connections")

		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.HTTP.ShutdownTimeout)
		defer cancel()

		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("graceful shutdown: %w", err)
		}
		log.Info("shutdown complete")
	}

	return nil
}
