// Command worker runs background processing that must not block the
// request path in cmd/api: reclaiming expired inventory holds, admitting
// fans from each event's waiting room, and dispatching queued
// notifications. All three are ticker-driven loops, run as separate
// goroutines so one's failure or slowness never blocks the others.
//
// Kafka consumers for order events are not yet built — this worker's
// scope today is exactly the three loops described above.
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
	"github.com/gavinarori/ticketing-backend/internal/domain"
	"github.com/gavinarori/ticketing-backend/internal/pkg/logger"
	"github.com/gavinarori/ticketing-backend/internal/platform/database"
	"github.com/gavinarori/ticketing-backend/internal/platform/email"
	appredis "github.com/gavinarori/ticketing-backend/internal/platform/redis"
	pgrepo "github.com/gavinarori/ticketing-backend/internal/repository/postgres"
	redisrepo "github.com/gavinarori/ticketing-backend/internal/repository/redis"
	"github.com/gavinarori/ticketing-backend/internal/service/admission"
	invsvc "github.com/gavinarori/ticketing-backend/internal/service/inventory"
	notifsvc "github.com/gavinarori/ticketing-backend/internal/service/notification"
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
		zap.Duration("admission_interval", cfg.Worker.AdmissionInterval),
		zap.Duration("notification_interval", cfg.Worker.NotificationInterval))

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
	notificationRepo := pgrepo.NewNotificationRepo(dbPool)
	userRepo := pgrepo.NewUserRepo(dbPool)

	locker := redisrepo.NewLocker(redisClient)
	waitingRoom := redisrepo.NewWaitingRoom(redisClient)
	rateLimiter := redisrepo.NewRateLimiter(redisClient)
	inventorySvc := invsvc.NewService(inventoryRepo, locker, waitingRoom, rateLimiter, invsvc.DefaultConfig())

	admissionSvc := admission.NewService(tenantRepo, eventRepo, inventoryRepo, inventorySvc, cfg.Worker.AdmissionMaxPerTick, log)

	// Real SMTP if configured, otherwise log-only — same dev-convenience
	// fallback pattern as the payment gateway in cmd/api, logged loudly
	// so a misconfigured production deploy is obvious rather than
	// discovered when a fan never receives their confirmation.
	var sender domain.EmailSender
	if cfg.Email.Host != "" {
		sender = email.NewSMTPSender(cfg.Email.Host, cfg.Email.Port, cfg.Email.Username, cfg.Email.Password, cfg.Email.From)
		log.Info("email sender: smtp", zap.String("host", cfg.Email.Host))
	} else {
		sender = email.NewConsoleSender(log)
		log.Warn("email sender: CONSOLE — no SMTP_HOST configured; no real emails will be sent")
	}
	notificationSvc := notifsvc.NewService(notificationRepo, userRepo, sender, log)

	var wg sync.WaitGroup
	wg.Add(3)
	go runSweepLoop(ctx, &wg, inventorySvc, cfg.Worker, log)
	go runAdmissionLoop(ctx, &wg, admissionSvc, cfg.Worker, log)
	go runNotificationLoop(ctx, &wg, notificationSvc, cfg.Worker, log)

	log.Info("worker ready — sweep, admission, and notification loops running")

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

// runNotificationLoop periodically sends whatever's queued in the
// transactional outbox — see migrations/000014_notifications.up.sql and
// internal/service/notification. The enqueue side lives inside
// internal/service/order.Service.ConfirmPayment, committed atomically
// with marking an order 'paid'; this loop is the decoupled send side.
func runNotificationLoop(ctx context.Context, wg *sync.WaitGroup, svc *notifsvc.Service, cfg config.WorkerConfig, log *zap.Logger) {
	defer wg.Done()
	ticker := time.NewTicker(cfg.NotificationInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sent, err := svc.ProcessPending(ctx, cfg.NotificationBatchSize)
			if err != nil {
				log.Error("notification_tick_failed", zap.Error(err))
				continue
			}
			if sent > 0 {
				log.Info("notification_tick", zap.Int("sent", sent))
			}
		}
	}
}
