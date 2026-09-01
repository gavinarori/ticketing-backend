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

	"go.uber.org/zap"

	"github.com/gavinarori/ticketing-backend/internal/config"
	"github.com/gavinarori/ticketing-backend/internal/domain"
	apphttp "github.com/gavinarori/ticketing-backend/internal/handler/http"
	"github.com/gavinarori/ticketing-backend/internal/pkg/logger"
	"github.com/gavinarori/ticketing-backend/internal/platform/database"
	"github.com/gavinarori/ticketing-backend/internal/platform/payment"
	appredis "github.com/gavinarori/ticketing-backend/internal/platform/redis"
	pgrepo "github.com/gavinarori/ticketing-backend/internal/repository/postgres"
	redisrepo "github.com/gavinarori/ticketing-backend/internal/repository/redis"
	authsvc "github.com/gavinarori/ticketing-backend/internal/service/auth"
	invsvc "github.com/gavinarori/ticketing-backend/internal/service/inventory"
	ordersvc "github.com/gavinarori/ticketing-backend/internal/service/order"
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

	// --- Repositories (Postgres-backed implementations of domain interfaces) ---
	inventoryRepo := pgrepo.NewInventoryRepo(dbPool)
	orderRepo := pgrepo.NewOrderRepo(dbPool)
	paymentRepo := pgrepo.NewPaymentRepo(dbPool)
	eventRepo := pgrepo.NewEventRepo(dbPool)
	userRepo := pgrepo.NewUserRepo(dbPool)
	refreshTokenRepo := pgrepo.NewRefreshTokenRepo(dbPool)

	// --- Redis-backed collaborators for the inventory locking service ---
	locker := redisrepo.NewLocker(redisClient)
	waitingRoom := redisrepo.NewWaitingRoom(redisClient)
	rateLimiter := redisrepo.NewRateLimiter(redisClient)
	inventorySvc := invsvc.NewService(inventoryRepo, locker, waitingRoom, rateLimiter, invsvc.DefaultConfig())

	// --- Payment gateway: real Stripe if configured, otherwise the
	// in-memory mock. This is a deliberate dev-convenience fallback, not
	// a silent production footgun — it's logged loudly below so a
	// misconfigured production deploy is obvious in the logs rather than
	// discovered when a real charge silently never happens. ---
	var gateway domain.PaymentGateway
	if cfg.Stripe.SecretKey != "" {
		gateway = payment.NewStripeGateway(cfg.Stripe.SecretKey, cfg.Stripe.WebhookSecret)
		log.Info("payment gateway: stripe")
	} else {
		gateway = payment.NewMockGateway(cfg.Stripe.WebhookSecret)
		log.Warn("payment gateway: MOCK — no STRIPE_SECRET_KEY configured; no real payments will be processed")
	}

	orderSvc := ordersvc.NewService(dbPool, orderRepo, inventoryRepo, paymentRepo, eventRepo, gateway)
	authService := authsvc.NewService(userRepo, refreshTokenRepo, cfg.JWT)

	_ = inventorySvc // wired for future handlers (seat holds, waiting room) — not yet exposed over HTTP

	healthHandler := apphttp.NewHealthHandler(dbPool, redisClient)
	webhookHandler := apphttp.NewWebhookHandler(gateway, orderRepo, paymentRepo, orderSvc, log)
	authHandler := apphttp.NewAuthHandler(authService, cfg.Bootstrap, log)
	router := apphttp.NewRouter(apphttp.RouterDeps{
		Cfg:     cfg,
		Log:     log,
		Health:  healthHandler,
		Webhook: webhookHandler,
		Auth:    authHandler,
		AuthSvc: authService,
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
