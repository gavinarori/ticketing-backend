// Command worker runs background processing that must not block the
// request path in cmd/api: reclaiming expired inventory holds and
// admitting fans from each event's waiting room. Both are ticker-driven
// loops, run as separate goroutines so one's failure or slowness never
// blocks the other.
//
// Kafka consumers for order events / notification dispatch are not yet
// built — this worker's scope today is exactly the two loops described
// above.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/gavinarori/ticketing-backend/internal/config"
	"github.com/gavinarori/ticketing-backend/internal/pkg/logger"
	"github.com/gavinarori/ticketing-backend/internal/platform/database"
	appredis "github.com/gavinarori/ticketing-backend/internal/platform/redis"
	pgrepo "github.com/gavinarori/ticketing-backend/internal/repository/postgres"
	redisrepo "github.com/gavinarori/ticketing-backend/internal/repository/redis"
	"github.com/gavinarori/ticketing-backend/internal/service/admission"
	invsvc "github.com/gavinarori/ticketing-backend/internal/service/inventory"
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

	log.Info("starting worker", zap.String("env", cfg.App.Env),
		zap.Duration("sweep_interval", cfg.Worker.SweepInterval),
		zap.Duration("admission_interval", cfg.Worker.AdmissionInterval))

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

	inventoryRepo := pgrepo.NewInventoryRepo(dbPool)
	eventRepo := pgrepo.NewEventRepo(dbPool)
	tenantRepo := pgrepo.NewTenantRepo(dbPool)

	locker := redisrepo.NewLocker(redisClient)
	waitingRoom := redisrepo.NewWaitingRoom(redisClient)
	rateLimiter := redisrepo.NewRateLimiter(redisClient)
	inventorySvc := invsvc.NewService(inventoryRepo, locker, waitingRoom, rateLimiter, invsvc.DefaultConfig())

	admissionSvc := admission.NewService(tenantRepo, eventRepo, inventoryRepo, inventorySvc, cfg.Worker.AdmissionMaxPerTick, log)

	var wg sync.WaitGroup
	wg.Add(2)
	go runSweepLoop(ctx, &wg, inventorySvc, cfg.Worker, log)
	go runAdmissionLoop(ctx, &wg, admissionSvc, cfg.Worker, log)

	log.Info("worker ready — sweep and admission loops running")

	<-ctx.Done()
	log.Info("shutdown signal received, waiting for in-flight ticks to finish")
	wg.Wait()
	log.Info("worker stopped")
	return nil
}

// runSweepLoop periodically reclaims 'held' inventory rows whose
// hold_expires_at has passed, releasing them back to 'available' — see
// internal/service/inventory.Service.SweepExpiredHolds. Runs
// platform-wide (not per-tenant): the underlying query has no tenant
// filter, by design, since one worker process sweeps expired holds
// across every club on the platform in a single pass.
func runSweepLoop(ctx context.Context, wg *sync.WaitGroup, svc *invsvc.Service, cfg config.WorkerConfig, log *zap.Logger) {
	defer wg.Done()
	ticker := time.NewTicker(cfg.SweepInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			released, err := svc.SweepExpiredHolds(ctx, cfg.SweepBatchSize)
			if err != nil {
				log.Error("sweep_tick_failed", zap.Error(err))
				continue
			}
			if released > 0 {
				log.Info("sweep_tick", zap.Int("released", released))
			}
		}
	}
}

// runAdmissionLoop periodically lets waiting-room fans through for every
// on-sale event across every active tenant — see
// internal/service/admission.
func runAdmissionLoop(ctx context.Context, wg *sync.WaitGroup, svc *admission.Service, cfg config.WorkerConfig, log *zap.Logger) {
	defer wg.Done()
	ticker := time.NewTicker(cfg.AdmissionInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			admitted, err := svc.RunOnce(ctx)
			if err != nil {
				log.Error("admission_tick_failed", zap.Error(err))
				continue
			}
			if admitted > 0 {
				log.Info("admission_tick", zap.Int("admitted", admitted))
			}
		}
	}
}
