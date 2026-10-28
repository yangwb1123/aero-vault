package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/antivirus"
	"github.com/aero-vault/aero-vault/internal/config"
	"github.com/aero-vault/aero-vault/internal/events"
	"github.com/aero-vault/aero-vault/internal/jobs"
	"github.com/aero-vault/aero-vault/internal/reconcile"
	"github.com/aero-vault/aero-vault/internal/replication"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

type backgroundWorkers struct {
	cfg         *config.Config
	logger      *slog.Logger
	repo        repository.Repository
	store       storage.Storage
	bus         *events.Bus
	registry    *jobs.Registry
	queue       *jobs.Queue
	svc         *service.FileService
	readers     spaceReaders
	av          *antivirus.Worker
	replica     *replication.Worker
	resync      *replication.Resyncer
	notifier    *events.Notifier
	webhook     *events.Webhook
	relay       *events.EventOutboxRelay
	maintenance *maintenanceWorkers
	initialized bool
	started     bool
}

func prepareBackgroundWorkers(cfg *config.Config, logger *slog.Logger, repo repository.Repository, store storage.Storage, bus *events.Bus, reg *jobs.Registry, queue *jobs.Queue, svc *service.FileService, readers spaceReaders) (*backgroundWorkers, error) {
	if err := readers.validate(); err != nil {
		return nil, err
	}
	w := &backgroundWorkers{cfg: cfg, logger: logger, repo: repo, store: store,
		bus: bus, registry: reg, queue: queue, svc: svc, readers: readers}
	w.notifier = events.NewNotifier(repo, logger).WithEventAuthority(readers.router.EventAuthority).
		WithResourceBoundary(events.NewResourceBoundary(readers.router.Binding))
	if cfg.Events.WebhookURL != "" {
		w.webhook = events.NewWebhook(cfg.Events.WebhookURL, logger).WithSecret(cfg.Events.WebhookSecret).
			WithRetryStore(repo).WithEventAuthority(readers.router.EventAuthority).
			WithResourceBoundary(events.NewResourceBoundary(readers.router.Binding))
	}
	if cfg.Antivirus.Enabled && reg != nil {
		scanner := buildScanner(cfg, logger)
		w.av = antivirus.NewWorker(repo, store, scanner, queue, cfg.Antivirus.Quarantine, logger).
			WithObjectController(svc).WithResourceBoundary(readers.router.Binding)
		registerAntivirusJob(reg, w.av)
	}
	var err error
	w.relay, err = prepareEventOutboxRelay(cfg, logger, repo, readers.router.Binding)
	if err != nil {
		return nil, err
	}
	w.maintenance, err = prepareMaintenance(cfg, logger, repo, store, svc.ChunkCleaner(), readers)
	if err != nil {
		return nil, err
	}
	return w, w.validate()
}

func registerAntivirusJob(reg *jobs.Registry, worker *antivirus.Worker) {
	reg.Register(antivirus.JobScan, func(ctx context.Context, job repository.Job) error {
		id, err := antivirus.DecodeObjectID(job.Payload)
		if err != nil {
			return err
		}
		return worker.ScanObjectByID(access.AntivirusContext(ctx, job.TenantID), id)
	})
}

func registerReplicationJob(reg *jobs.Registry, worker *replication.Worker) {
	reg.Register(replication.JobReplicate, func(ctx context.Context, job repository.Job) error {
		id, err := replication.DecodeObjectID(job.Payload)
		if err != nil {
			return err
		}
		return worker.ReplicateObjectByID(access.SystemContext(ctx, job.TenantID), id)
	})
}

func (w *backgroundWorkers) validate() error {
	if w == nil || w.cfg == nil || w.bus == nil || w.svc == nil {
		return spaceStartupFault(errors.New("missing background worker dependencies"))
	}
	if err := w.readers.validate(); err != nil {
		return err
	}
	if err := w.validateQueue(); err != nil {
		return err
	}
	if err := w.validateConsumers(); err != nil {
		return err
	}
	return w.maintenance.validate(w.cfg)
}

func (w *backgroundWorkers) validateQueue() error {
	if (w.registry == nil) != (w.queue == nil) || (w.cfg.Jobs.Workers > 0 && w.registry == nil) {
		return spaceStartupFault(errors.New("incomplete job registry/queue"))
	}
	return nil
}

func (w *backgroundWorkers) validateConsumers() error {
	if w.notifier == nil || (w.cfg.Events.WebhookURL != "" && w.webhook == nil) || (w.cfg.EventOutbox.Enabled && w.relay == nil) {
		return spaceStartupFault(errors.New("missing prepared event consumer"))
	}
	if w.cfg.Antivirus.Enabled && w.registry != nil && w.av == nil {
		return spaceStartupFault(errors.New("missing prepared antivirus worker"))
	}
	return nil
}

// Secondary storage construction can perform backend I/O. It is deliberately
// after every consumer's preflight, and before any subscription, pool or Run.
// No nil-storage worker or proxy is installed: the real handle is constructed
// with its raw reader before registration and remains owned by this instance.
func (w *backgroundWorkers) initialize(ctx context.Context) error {
	if err := w.validate(); err != nil {
		return err
	}
	if w.initialized {
		return spaceStartupFault(errors.New("background workers already initialized"))
	}
	if w.cfg.Replication.Enabled && w.registry != nil {
		replica, err := buildStorageFrom(ctx, w.cfg.Replication.Storage)
		if err != nil {
			return fmt.Errorf("build replica storage: %w", err)
		}
		w.replica = replication.NewWorker(w.repo, w.store, replica, w.queue, w.logger).
			WithObjectTagger(w.svc).WithResourceBoundary(w.readers.router.Binding)
		registerReplicationJob(w.registry, w.replica)
		if w.cfg.Replication.ResyncIntervalMinutes > 0 {
			w.resync = replication.NewResyncer(w.repo, w.queue,
				time.Duration(w.cfg.Replication.ResyncIntervalMinutes)*time.Minute, w.logger).
				WithResourceBoundary(w.readers.router.Binding)
		}
		w.logger.Info("replication enabled", "replica_backend", replica.Backend())
	}
	w.initialized = true
	return nil
}

func (w *backgroundWorkers) validateInitialized() error {
	if err := w.validate(); err != nil {
		return err
	}
	if !w.initialized || w.started {
		return spaceStartupFault(errors.New("workers not initialized or already started"))
	}
	return w.validateReplication()
}

func (w *backgroundWorkers) validateReplication() error {
	if !w.cfg.Replication.Enabled || w.registry == nil {
		return nil
	}
	if w.replica == nil {
		return spaceStartupFault(errors.New("replication worker not initialized"))
	}
	if w.cfg.Replication.ResyncIntervalMinutes > 0 && w.resync == nil {
		return spaceStartupFault(errors.New("resync worker not initialized"))
	}
	return nil
}

func (w *backgroundWorkers) start(ctx context.Context) error {
	if err := w.validateInitialized(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	w.started = true
	if w.av != nil {
		sub, _ := w.bus.Subscribe()
		go w.av.Run(ctx, sub)
	}
	if w.replica != nil {
		sub, _ := w.bus.Subscribe()
		go w.replica.Run(ctx, sub)
	}
	if w.resync != nil {
		go w.resync.Run(ctx)
	}
	if w.webhook != nil {
		sub, _ := w.bus.Subscribe()
		go w.webhook.Run(ctx, sub)
		go w.webhook.RetryLoop(ctx)
		w.logger.Info("event webhook enabled", "url", w.cfg.Events.WebhookURL, "signed", w.cfg.Events.WebhookSecret != "")
	}
	sub, _ := w.bus.Subscribe()
	go w.notifier.Run(ctx, sub)
	w.logger.Info("bucket notification worker started")
	runPreparedEventOutboxRelay(ctx, w.cfg, w.logger, w.repo, w.relay)
	w.maintenance.start(ctx, w.cfg, w.logger)
	if w.registry != nil {
		go jobs.NewPool(w.repo, w.registry, w.cfg.Jobs.Workers, w.logger).Run(ctx)
		w.logger.Info("job pool started", "workers", w.cfg.Jobs.Workers)
	}
	return nil
}

// Retain the standalone builder for existing tests/callers, but use the same
// guarded prepare/initialize/start path. Production supplies captured raw ports
// explicitly and never rediscovers them through a write wrapper.
func buildBackgroundWorkers(ctx context.Context, cfg *config.Config, logger *slog.Logger, repo repository.Repository, store storage.Storage, bus *events.Bus, reg *jobs.Registry, queue *jobs.Queue, svc *service.FileService) error {
	readers, err := captureSpaceReaders(repo)
	if err != nil {
		return err
	}
	w, err := prepareBackgroundWorkers(cfg, logger, repo, store, bus, reg, queue, svc, readers)
	if err != nil {
		return err
	}
	if err := w.initialize(ctx); err != nil {
		return err
	}
	return w.start(ctx)
}

type maintenanceWorkers struct {
	reconcile *reconcile.Job
	lifecycle *reconcile.LifecycleJob
	retention *reconcile.RetentionJob
	uploadGC  *reconcile.UploadGCJob
}

func prepareMaintenance(cfg *config.Config, logger *slog.Logger, repo repository.Repository, store storage.Storage, cc reconcile.ChunkCleaner, readers spaceReaders) (*maintenanceWorkers, error) {
	m := &maintenanceWorkers{}
	if cfg.Reconcile.IntervalMinutes <= 0 {
		return m, nil
	}
	interval := time.Duration(cfg.Reconcile.IntervalMinutes) * time.Minute
	m.reconcile = reconcile.New(repo, store, interval, cfg.Reconcile.DeleteOrphanBlobs,
		time.Duration(cfg.Reconcile.OrphanGraceMinutes)*time.Minute, cfg.Reconcile.Tenants, logger).
		WithScrub(cfg.Reconcile.ScrubEnabled, 100).WithChunkCleaner(cc).
		WithResourceBoundary(readers.router.Binding).WithOrphanCoverage(readers.router.Coverage)
	m.lifecycle = reconcile.NewLifecycle(repo, store, interval, logger).
		WithChunkCleaner(cc).WithResourceBoundary(readers.router.Binding)
	if cfg.Reconcile.RetentionDays > 0 || cfg.Reconcile.IdempotencyTTLHours > 0 {
		m.retention = reconcile.NewRetention(repo, store, interval,
			time.Duration(cfg.Reconcile.RetentionDays)*24*time.Hour, logger).
			WithChunkCleaner(cc).WithResourceBoundary(readers.router.Binding)
		if cfg.Reconcile.IdempotencyTTLHours > 0 {
			m.retention.WithIdempotencyTTL(time.Duration(cfg.Reconcile.IdempotencyTTLHours) * time.Hour)
		}
	}
	if cfg.Reconcile.UploadGCEnable {
		m.uploadGC = reconcile.NewUploadGC(repo, store, interval,
			time.Duration(cfg.Reconcile.UploadGCHours)*time.Hour, logger).WithResourceBoundary(readers.router.Binding)
	}
	if cfg.Reconcile.ClusterSingleton {
		m.singleton(uuid.NewString())
	}
	return m, m.validate(cfg)
}

func (m *maintenanceWorkers) singleton(id string) {
	m.reconcile.WithClusterSingleton(id)
	m.lifecycle.WithClusterSingleton(id)
	if m.retention != nil {
		m.retention.WithClusterSingleton(id)
	}
	if m.uploadGC != nil {
		m.uploadGC.WithClusterSingleton(id)
	}
}

func (m *maintenanceWorkers) validate(cfg *config.Config) error {
	if m == nil {
		return spaceStartupFault(errors.New("missing maintenance preparation"))
	}
	if cfg.Reconcile.IntervalMinutes <= 0 {
		return nil
	}
	if err := errors.Join(m.reconcile.ValidateResourceBoundary(), m.lifecycle.ValidateResourceBoundary()); err != nil {
		return err
	}
	if cfg.Reconcile.RetentionDays > 0 || cfg.Reconcile.IdempotencyTTLHours > 0 {
		if err := m.retention.ValidateResourceBoundary(); err != nil {
			return err
		}
	}
	if cfg.Reconcile.UploadGCEnable {
		return m.uploadGC.ValidateResourceBoundary()
	}
	return nil
}

func (m *maintenanceWorkers) start(ctx context.Context, cfg *config.Config, logger *slog.Logger) {
	if m.reconcile == nil {
		return
	}
	go m.reconcile.Run(ctx)
	go m.lifecycle.Run(ctx)
	if m.retention != nil {
		go m.retention.Run(ctx)
	}
	if m.uploadGC != nil {
		go m.uploadGC.Run(ctx)
	}
	logger.Info("reconcile + lifecycle jobs started", "interval_minutes", cfg.Reconcile.IntervalMinutes,
		"delete_orphan_blobs", cfg.Reconcile.DeleteOrphanBlobs, "orphan_grace_minutes", cfg.Reconcile.OrphanGraceMinutes,
		"tenants", cfg.Reconcile.Tenants, "cluster_singleton", cfg.Reconcile.ClusterSingleton,
		"retention_days", cfg.Reconcile.RetentionDays, "upload_gc_ttl_hours", cfg.Reconcile.UploadGCHours)
}

func startReconcile(ctx context.Context, cfg *config.Config, logger *slog.Logger, repo repository.Repository, store storage.Storage, cc reconcile.ChunkCleaner) {
	readers, err := captureSpaceReaders(repo)
	if err != nil {
		logger.Error("prepare maintenance readers", "err", err)
		return
	}
	m, err := prepareMaintenance(cfg, logger, repo, store, cc, readers)
	if err != nil {
		logger.Error("prepare maintenance", "err", err)
		return
	}
	m.start(ctx, cfg, logger)
}

func startNotificationWorker(ctx context.Context, logger *slog.Logger, repo repository.Repository, bus *events.Bus) {
	readers, err := captureSpaceReaders(repo)
	if err != nil {
		logger.Error("prepare notification readers", "err", err)
		return
	}
	n := events.NewNotifier(repo, logger).WithEventAuthority(readers.router.EventAuthority).
		WithResourceBoundary(events.NewResourceBoundary(readers.router.Binding))
	sub, _ := bus.Subscribe()
	go n.Run(ctx, sub)
	logger.Info("bucket notification worker started")
}

func startWebhook(ctx context.Context, cfg *config.Config, logger *slog.Logger, repo repository.Repository, bus *events.Bus) {
	if cfg.Events.WebhookURL == "" {
		return
	}
	readers, err := captureSpaceReaders(repo)
	if err != nil {
		logger.Error("prepare webhook readers", "err", err)
		return
	}
	w := events.NewWebhook(cfg.Events.WebhookURL, logger).WithSecret(cfg.Events.WebhookSecret).
		WithRetryStore(repo).WithEventAuthority(readers.router.EventAuthority).
		WithResourceBoundary(events.NewResourceBoundary(readers.router.Binding))
	sub, _ := bus.Subscribe()
	go w.Run(ctx, sub)
	go w.RetryLoop(ctx)
}
