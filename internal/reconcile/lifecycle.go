package reconcile

import (
	"context"
	"log/slog"
	"time"

	"github.com/aero-vault/aero-vault/internal/cluster"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/storage"
)

// LifecycleJob applies per-bucket retention policy (ExpireAfterDays). Runs on
// the same cadence as Reconcile. Soft-delete is the default action; hard
// delete is enabled when expire_action="hard_delete" on the bucket.
const leaseLifecycleSweep = "lifecycle-sweep"

type LifecycleJob struct {
	repo      repository.Repository
	store     storage.Storage
	interval  time.Duration
	singleton *cluster.Singleton
	logger    *slog.Logger
}

func NewLifecycle(repo repository.Repository, store storage.Storage, interval time.Duration, logger *slog.Logger) *LifecycleJob {
	if logger == nil {
		logger = slog.Default()
	}
	return &LifecycleJob{repo: repo, store: store, interval: interval, singleton: cluster.NewSingleton(repo, leaseLifecycleSweep, logger), logger: logger}
}

// WithClusterSingleton makes the (destructive) lifecycle sweep run on only one
// replica at a time, gated by a repository lease held by `holder`.
func (l *LifecycleJob) WithClusterSingleton(holder string) *LifecycleJob {
	l.singleton.Enable(holder)
	return l
}

func (l *LifecycleJob) Run(ctx context.Context) {
	if l.interval <= 0 {
		return
	}
	t := time.NewTicker(l.interval)
	defer t.Stop()
	l.maybeSweep(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			l.maybeSweep(ctx)
		}
	}
}

// maybeSweep runs the sweep, gated by the cluster singleton when enabled (lease
// TTL 2× interval; the holder renews each round).
func (l *LifecycleJob) maybeSweep(ctx context.Context) {
	l.singleton.Guard(ctx, 2*l.interval, l.sweep)
}

func (l *LifecycleJob) sweep(ctx context.Context) {
	expired, err := l.repo.ListExpired(ctx, 200)
	if err != nil {
		l.logger.Warn("lifecycle list expired", "err", err)
		return
	}
	if len(expired) == 0 {
		return
	}
	soft, hard := 0, 0
	for _, obj := range expired {
		action := obj.Metadata["__expire_action"]
		if action == "hard_delete" {
			if obj.LockedUntil != nil && obj.LockedUntil.After(time.Now()) {
				continue // can't hard delete while locked
			}
			if err := l.store.Delete(ctx, obj.StorageKey); err != nil {
				l.logger.Warn("lifecycle storage delete", "key", obj.Key, "err", err)
				continue
			}
			if err := l.repo.HardDeleteObject(ctx, obj.TenantID, obj.Bucket, obj.Key); err == nil {
				hard++
			}
		} else {
			if err := l.repo.SoftDeleteObject(ctx, obj.TenantID, obj.Bucket, obj.Key); err == nil {
				soft++
			}
		}
	}
	if soft > 0 || hard > 0 {
		l.logger.Info("lifecycle sweep", "soft_deleted", soft, "hard_deleted", hard)
	}
}
