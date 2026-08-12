package main

import (
	"context"
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

func buildBackgroundWorkers(ctx context.Context, cfg *config.Config, logger *slog.Logger, repo repository.Repository, store storage.Storage, bus *events.Bus, jobReg *jobs.Registry, jobQueue *jobs.Queue, svc *service.FileService) error {
	if cfg.Antivirus.Enabled && jobReg != nil {
		scanner := buildScanner(cfg, logger)
		avw := antivirus.NewWorker(repo, store, scanner, jobQueue, cfg.Antivirus.Quarantine, logger).
			WithObjectController(svc)
		jobReg.Register(antivirus.JobScan, func(ctx context.Context, job repository.Job) error {
			id, err := antivirus.DecodeObjectID(job.Payload)
			if err != nil {
				return err
			}
			return avw.ScanObjectByID(access.AntivirusContext(ctx, job.TenantID), id)
		})
		avSub, _ := bus.Subscribe()
		go avw.Run(ctx, avSub)
		logger.Info("antivirus enabled", "scanner", scanner.Name(), "quarantine", cfg.Antivirus.Quarantine)
	}
	if cfg.Replication.Enabled && jobReg != nil {
		replica, err := buildStorageFrom(ctx, cfg.Replication.Storage)
		if err != nil {
			return fmt.Errorf("build replica storage: %w", err)
		}
		rw := replication.NewWorker(repo, store, replica, jobQueue, logger).
			WithObjectTagger(svc)
		jobReg.Register(replication.JobReplicate, func(ctx context.Context, job repository.Job) error {
			id, err := replication.DecodeObjectID(job.Payload)
			if err != nil {
				return err
			}
			return rw.ReplicateObjectByID(access.SystemContext(ctx, job.TenantID), id)
		})
		rwSub, _ := bus.Subscribe()
		go rw.Run(ctx, rwSub)
		logger.Info("replication enabled", "replica_backend", replica.Backend())
	}
	if jobReg != nil {
		go jobs.NewPool(repo, jobReg, cfg.Jobs.Workers, logger).Run(ctx)
		logger.Info("job pool started", "workers", cfg.Jobs.Workers)
	}
	startWebhook(ctx, cfg, logger, repo, bus)
	startNotificationWorker(ctx, logger, repo, bus)
	if err := startEventOutboxRelay(ctx, cfg, logger, repo); err != nil {
		return err
	}
	if cfg.Reconcile.IntervalMinutes > 0 {
		startReconcile(ctx, cfg, logger, repo, store, svc.ChunkCleaner())
	}
	return nil
}

func startWebhook(ctx context.Context, cfg *config.Config, logger *slog.Logger, repo repository.Repository, bus *events.Bus) {
	if cfg.Events.WebhookURL != "" {
		wh := events.NewWebhook(cfg.Events.WebhookURL, logger).
			WithSecret(cfg.Events.WebhookSecret).
			WithRetryStore(repo)
		whSub, _ := bus.Subscribe()
		go wh.Run(ctx, whSub)
		go wh.RetryLoop(ctx)
		logger.Info("event webhook enabled", "url", cfg.Events.WebhookURL, "signed", cfg.Events.WebhookSecret != "")
	}
}

func startReconcile(ctx context.Context, cfg *config.Config, logger *slog.Logger, repo repository.Repository, store storage.Storage, cc reconcile.ChunkCleaner) {
	j := reconcile.New(repo, store, time.Duration(cfg.Reconcile.IntervalMinutes)*time.Minute,
		cfg.Reconcile.DeleteOrphanBlobs,
		time.Duration(cfg.Reconcile.OrphanGraceMinutes)*time.Minute,
		cfg.Reconcile.Tenants, logger).WithScrub(cfg.Reconcile.ScrubEnabled, 100)
	if cc != nil {
		j.WithChunkCleaner(cc)
	}
	lf := reconcile.NewLifecycle(repo, store, time.Duration(cfg.Reconcile.IntervalMinutes)*time.Minute, logger)
	if cc != nil {
		lf.WithChunkCleaner(cc)
	}
	var rg *reconcile.RetentionJob
	if cfg.Reconcile.RetentionDays > 0 || cfg.Reconcile.IdempotencyTTLHours > 0 {
		rg = reconcile.NewRetention(repo, store, time.Duration(cfg.Reconcile.IntervalMinutes)*time.Minute,
			time.Duration(cfg.Reconcile.RetentionDays)*24*time.Hour, logger)
		if cc != nil {
			rg.WithChunkCleaner(cc)
		}
		if cfg.Reconcile.IdempotencyTTLHours > 0 {
			rg.WithIdempotencyTTL(time.Duration(cfg.Reconcile.IdempotencyTTLHours) * time.Hour)
		}
	}
	var ug *reconcile.UploadGCJob
	if cfg.Reconcile.UploadGCEnable {
		ug = reconcile.NewUploadGC(repo, store,
			time.Duration(cfg.Reconcile.IntervalMinutes)*time.Minute,
			time.Duration(cfg.Reconcile.UploadGCHours)*time.Hour, logger)
	}
	if cfg.Reconcile.ClusterSingleton {
		instanceID := uuid.NewString()
		j.WithClusterSingleton(instanceID)
		lf.WithClusterSingleton(instanceID)
		if rg != nil {
			rg.WithClusterSingleton(instanceID)
		}
		if ug != nil {
			ug.WithClusterSingleton(instanceID)
		}
	}
	go j.Run(ctx)
	go lf.Run(ctx)
	if rg != nil {
		go rg.Run(ctx)
	}
	if ug != nil {
		go ug.Run(ctx)
	}
	logger.Info("reconcile + lifecycle jobs started",
		"interval_minutes", cfg.Reconcile.IntervalMinutes,
		"delete_orphan_blobs", cfg.Reconcile.DeleteOrphanBlobs,
		"orphan_grace_minutes", cfg.Reconcile.OrphanGraceMinutes,
		"tenants", cfg.Reconcile.Tenants,
		"cluster_singleton", cfg.Reconcile.ClusterSingleton,
		"retention_days", cfg.Reconcile.RetentionDays,
		"upload_gc_ttl_hours", cfg.Reconcile.UploadGCHours)
}

func startNotificationWorker(ctx context.Context, logger *slog.Logger, repo repository.Repository, bus *events.Bus) {
	notif := events.NewNotifier(repo, logger)
	sub, _ := bus.Subscribe()
	go notif.Run(ctx, sub)
	logger.Info("bucket notification worker started")
}

// startEventOutboxRelay drains the transactional deletion outbox (claim →
// deliver → complete). It starts unless EVENT_OUTBOX_ENABLED=false: the
// kill-switch gates only the relay loop — the transactional enqueue inside
// *DeleteObject*WithEvent is never gated (deletion atomicity is not
// negotiable), so rows keep accumulating while disabled and drain (FIFO) on
// re-enable. With no notification rules the relay is a silent no-op. Notify
// facts are delivered from their self-contained payload; deleted@1.1 facts
// are completed without local re-broadcast (D3) unless an L2 AuditSink is
// configured, in which case they are delivered to the audit sink (FR-2/FR-4).
// An L2 adapter that fails its own endpoint validation aborts startup
// (fail-fast, F6 — config.Validate already rejected it; this is the second
// enforcement point). Numeric knobs are likewise validated unconditionally,
// so a disabled relay still fails startup on a bad poll interval or
// malformed L2 endpoint (F3/F7).
func startEventOutboxRelay(ctx context.Context, cfg *config.Config, logger *slog.Logger, repo repository.Repository) error {
	// Kill-switch gate first, before any options/L2-sink construction or
	// goroutine (D1): the backlog count is the one diagnostic that still runs
	// while disabled (read-only, nil-repo-safe, never blocks startup — D6).
	if !cfg.EventOutbox.Enabled {
		logger.Info("event outbox relay disabled",
			"backlog", eventOutboxBacklog(ctx, repo))

		return nil
	}
	opts := events.EventOutboxRelayOptions{
		PollInterval:    time.Duration(cfg.EventOutbox.PollMilliseconds) * time.Millisecond,
		BatchSize:       cfg.EventOutbox.BatchSize,
		ClaimTTL:        time.Duration(cfg.EventOutbox.ClaimTTLSeconds) * time.Second,
		HTTPTimeout:     time.Duration(cfg.EventOutbox.HTTPTimeoutSeconds) * time.Second,
		MaxAttempts:     cfg.EventOutbox.MaxAttempts,
		DeliveredRetain: time.Duration(cfg.EventOutbox.DeliveredRetentionHours) * time.Hour,
		FailedRetain:    time.Duration(cfg.EventOutbox.FailedRetentionHours) * time.Hour,
	}
	if cfg.AuditSinkL2.Endpoint != "" {
		sink, err := events.NewAuditSinkL2(cfg.AuditSinkL2.Endpoint,
			auditSinkL2Bindings(cfg.AuditSinkL2.Bindings),
			events.NewAuditSinkL2Client(opts.HTTPTimeout), logger)
		if err != nil {
			return fmt.Errorf("build audit sink L2: %w", err)
		}
		opts.AuditSink = sink
		logger.Info("event outbox relay L2 audit sink enabled", "endpoint", cfg.AuditSinkL2.Endpoint,
			"bindings", len(cfg.AuditSinkL2.Bindings))
	}
	relay := events.NewEventOutboxRelay(repo, logger, opts)
	go relay.Run(ctx)
	logger.Info("event outbox relay started",
		"poll_ms", cfg.EventOutbox.PollMilliseconds,
		"batch", cfg.EventOutbox.BatchSize,
		"claim_ttl_s", cfg.EventOutbox.ClaimTTLSeconds,
		"http_timeout_s", cfg.EventOutbox.HTTPTimeoutSeconds,
		"max_attempts", cfg.EventOutbox.MaxAttempts,
		"delivered_retain_h", cfg.EventOutbox.DeliveredRetentionHours,
		"failed_retain_h", cfg.EventOutbox.FailedRetentionHours,
		"backlog", eventOutboxBacklog(ctx, repo))

	return nil
}

// eventOutboxBacklog returns the event_outbox depth for the startup log
// (D6). Diagnostic only: a nil repo or query error yields "unknown" and
// never blocks or fails startup. It is the only outbox signal that exists
// while the relay is disabled — all outbox counters are relay-side and
// frozen when off, and PROMETHEUS_ENABLED defaults to false.
func eventOutboxBacklog(ctx context.Context, repo repository.Repository) any {
	if repo == nil {
		return "unknown"
	}
	n, err := repo.CountEventOutbox(ctx)
	if err != nil {
		return "unknown"
	}
	return n
}

// auditSinkL2Bindings flattens the config bindings into the adapter's
// tenant → token map. Token values never appear in logs (the caller logs
// only the count).
func auditSinkL2Bindings(bindings []config.AuditSinkL2Binding) map[string]string {
	if len(bindings) == 0 {
		return nil
	}
	out := make(map[string]string, len(bindings))
	for _, binding := range bindings {
		out[binding.Tenant] = binding.Token
	}
	return out
}
