package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/aero-vault/aero-vault/internal/antivirus"
	"github.com/aero-vault/aero-vault/internal/config"
	"github.com/aero-vault/aero-vault/internal/events"
	"github.com/aero-vault/aero-vault/internal/jobs"
	"github.com/aero-vault/aero-vault/internal/reconcile"
	"github.com/aero-vault/aero-vault/internal/replication"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/storage"
)

func buildBackgroundWorkers(ctx context.Context, cfg *config.Config, logger *slog.Logger, repo repository.Repository, store storage.Storage, bus *events.Bus, jobReg *jobs.Registry, jobQueue *jobs.Queue, cc reconcile.ChunkCleaner) error {
	if cfg.Antivirus.Enabled && jobReg != nil {
		scanner := buildScanner(cfg, logger)
		avw := antivirus.NewWorker(repo, store, scanner, jobQueue, cfg.Antivirus.Quarantine, logger)
		jobReg.Register(antivirus.JobScan, func(ctx context.Context, job repository.Job) error {
			id, err := antivirus.DecodeObjectID(job.Payload)
			if err != nil {
				return err
			}
			return avw.ScanObjectByID(ctx, id)
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
		rw := replication.NewWorker(repo, store, replica, jobQueue, logger)
		jobReg.Register(replication.JobReplicate, func(ctx context.Context, job repository.Job) error {
			id, err := replication.DecodeObjectID(job.Payload)
			if err != nil {
				return err
			}
			return rw.ReplicateObjectByID(ctx, id)
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
	if cfg.Reconcile.IntervalMinutes > 0 {
		startReconcile(ctx, cfg, logger, repo, store, cc)
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
	lf := reconcile.NewLifecycle(repo, store, time.Duration(cfg.Reconcile.IntervalMinutes)*time.Minute, logger)
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
