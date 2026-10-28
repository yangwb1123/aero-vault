package reconcile

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/aero-vault/aero-vault/internal/cluster"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/spaces"
	"github.com/aero-vault/aero-vault/internal/storage"
)

// LifecycleJob applies per-bucket retention policy (ExpireAfterDays). Runs on
// the same cadence as Reconcile. Soft-delete is the default action; hard
// delete is enabled when expire_action="hard_delete" on the bucket.
const leaseLifecycleSweep = "lifecycle-sweep"

type LifecycleJob struct {
	repo      repository.Repository
	store     storage.Storage
	cleaner   ChunkCleaner
	interval  time.Duration
	singleton *cluster.Singleton
	scope     scopeState
	logger    *slog.Logger
}

func (l *LifecycleJob) WithChunkCleaner(cleaner ChunkCleaner) *LifecycleJob {
	l.cleaner = cleaner
	return l
}

// WithResourceBoundary installs deny-only row admission before protection,
// deletion or transition work. Explicit nil is a configured fault.
func (l *LifecycleJob) WithResourceBoundary(reader spaces.BindingReader) *LifecycleJob {
	l.scope = newScopeState(reader)
	l.scope.repo = l.repo
	return l
}

// ValidateResourceBoundary rejects absent/typed-nil injection before starts.
func (l *LifecycleJob) ValidateResourceBoundary() error {
	if l == nil {
		return scopeRowFault(errors.New("missing lifecycle job"))
	}
	if scopePortMissing(l.store) {
		return scopeRowFault(errors.New("missing lifecycle storage"))
	}
	return l.scope.validate()
}

func NewLifecycle(repo repository.Repository, store storage.Storage, interval time.Duration, logger *slog.Logger) *LifecycleJob {
	if logger == nil {
		logger = slog.Default()
	}
	return &LifecycleJob{repo: repo, store: store, interval: interval, singleton: cluster.NewSingleton(repo, leaseLifecycleSweep, logger), logger: logger, scope: scopeState{repo: repo}}
}

// WithClusterSingleton makes the (destructive) lifecycle sweep run on only one
// replica at a time, gated by a repository lease held by `holder`.
func (l *LifecycleJob) WithClusterSingleton(holder string) *LifecycleJob {
	l.singleton.Enable(holder)
	return l
}

func (l *LifecycleJob) Run(ctx context.Context) {
	if l.interval <= 0 || l.scope.ready(ctx) != nil {
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
	if err := l.scope.ready(ctx); err != nil {
		l.logger.Warn("lifecycle boundary unavailable", "err", err)
		return
	}
	l.singleton.Guard(ctx, 2*l.interval, l.sweep)
}

func (l *LifecycleJob) sweep(ctx context.Context) {
	l.scope.resetCounts()
	if err := l.scope.ready(ctx); err != nil {
		l.logger.Warn("lifecycle boundary unavailable", "err", err)
		return
	}
	soft, hard := l.sweepExpired(ctx)
	noncurrent := l.sweepNonCurrentVersions(ctx)
	transitions := l.sweepTransitions(ctx)
	if soft > 0 || hard > 0 || noncurrent > 0 || transitions > 0 || l.scope.refused > 0 || l.scope.faults > 0 {
		l.logger.Info("lifecycle sweep", "soft_deleted", soft, "hard_deleted", hard,
			"noncurrent_versions_purged", noncurrent, "transitioned", transitions,
			"scope_refused", l.scope.refused, "scope_faults", l.scope.faults)
	}
}

func (l *LifecycleJob) sweepExpired(ctx context.Context) (soft, hard int) {
	if err := l.scope.ready(ctx); err != nil {
		return
	}
	expired, err := l.repo.ListExpired(ctx, 200)
	if err = errors.Join(err, scopeContext(ctx)); err != nil {
		l.scope.faults++
		l.logger.Warn("lifecycle list expired", "err", err)
		return
	}
	for _, obj := range expired {
		action := obj.Metadata["__expire_action"]
		if l.handleExpiredObject(ctx, obj, action) {
			if action == "hard_delete" {
				hard++
			} else {
				soft++
			}
		}
	}
	return
}

func (l *LifecycleJob) handleExpiredObject(ctx context.Context, obj repository.Object, action string) bool {
	actual, outcome, err := l.scope.object(ctx, obj, "", "")
	if outcome != scopeAdmitted {
		l.logger.Warn("lifecycle row scope refused", "key", obj.Key, "err", err)
		return false
	}
	obj = actual
	if obj.DeletedAt != nil {
		l.scope.faults++
		return false
	}
	repo := l.scope.deletionRepo(obj)
	if action == "hard_delete" {
		protected, err := objectKeyDeletionProtected(ctx, repo, obj)
		if err != nil {
			l.scope.failed(err)
			l.logger.Warn("lifecycle protection check", "key", obj.Key, "err", err)
			return false
		}
		if protected {
			return false
		}
		// FR-3: ListExpired returns only the current version (deleted_at IS
		// NULL). Purge that version alone; on versioned buckets the tombstone
		// rows and their blobs stay under the noncurrent_days window and are
		// only removed by sweepNonCurrentVersions.
		if err := hardDeleteVersion(ctx, repo, l.store, l.cleaner, obj, l.logger); err != nil {
			l.scope.failed(err)
			if errors.Is(err, errKeyProtected) {
				l.logger.Warn("lifecycle hard delete skipped: protected", "key", obj.Key)
			} else {
				l.logger.Warn("lifecycle hard delete", "key", obj.Key, "err", err)
			}
			return false
		}
		return l.scope.ready(ctx) == nil
	}
	if err := softDeleteKey(ctx, repo, l.cleaner, obj, l.logger); err != nil {
		l.scope.failed(err)
		l.logger.Warn("lifecycle soft delete", "key", obj.Key, "err", err)
		return false
	}
	return l.scope.ready(ctx) == nil
}

// sweepNonCurrentVersions permanently removes version tombstones (old versions
// from versioning-enabled buckets) whose bucket's noncurrent_days window has
// passed. Returns the count of purged rows.
func (l *LifecycleJob) sweepNonCurrentVersions(ctx context.Context) int {
	if err := l.scope.ready(ctx); err != nil {
		return 0
	}
	versions, err := l.repo.ListExpiredNonCurrentVersions(ctx, 200)
	if err = errors.Join(err, scopeContext(ctx)); err != nil {
		l.scope.faults++
		l.logger.Warn("lifecycle list non-current versions", "err", err)
		return 0
	}
	purged := 0
	for _, v := range versions {
		if l.purgeNonCurrentVersion(ctx, v) {
			purged++
		}
	}
	if purged > 0 {
		l.logger.Info("lifecycle non-current versions purged", "count", purged)
	}
	return purged
}

func (l *LifecycleJob) purgeNonCurrentVersion(ctx context.Context, v repository.Object) bool {
	actual, outcome, scopeErr := l.scope.object(ctx, v, "", "")
	if outcome != scopeAdmitted {
		l.logger.Warn("lifecycle non-current row scope refused", "key", v.Key, "version", v.VersionID, "err", scopeErr)
		return false
	}
	v = actual
	if !v.VersionTombstone || v.DeletedAt == nil {
		l.scope.faults++
		return false
	}
	repo := l.scope.deletionRepo(v)
	protected, err := objectDeletionProtected(ctx, repo, v)
	if err != nil {
		l.scope.failed(err)
		l.logger.Warn("lifecycle non-current protection check", "key", v.Key, "version", v.VersionID, "err", err)
		return false
	}
	if protected {
		return false
	}
	if err := hardDeleteVersion(ctx, repo, l.store, l.cleaner, v, l.logger); err != nil {
		l.scope.failed(err)
		l.logger.Warn("lifecycle non-current version hard delete", "key", v.Key, "version", v.VersionID, "err", err)
		return false
	}
	return l.scope.ready(ctx) == nil
}

// sweepTransitions finds objects whose bucket has transition rules and whose age
// qualifies for a storage-class change, then updates the DB record. Returns the
// number of objects transitioned.
func (l *LifecycleJob) sweepTransitions(ctx context.Context) int {
	if err := l.scope.ready(ctx); err != nil {
		return 0
	}
	objs, err := l.repo.ListTransitionable(ctx, 200)
	if err = errors.Join(err, scopeContext(ctx)); err != nil {
		l.scope.faults++
		l.logger.Warn("lifecycle list transitionable", "err", err)
		return 0
	}
	transitioned := 0
	for _, obj := range objs {
		if l.transitionObject(ctx, obj) {
			transitioned++
		}
	}
	if transitioned > 0 {
		l.logger.Info("lifecycle transitions applied", "count", transitioned)
	}
	return transitioned
}

func (l *LifecycleJob) transitionObject(ctx context.Context, obj repository.Object) bool {
	targetClass := obj.Metadata["__transition_to"]
	actual, outcome, scopeErr := l.scope.object(ctx, obj, "", "")
	if outcome != scopeAdmitted {
		l.logger.Warn("lifecycle transition row scope refused", "key", obj.Key, "err", scopeErr)
		return false
	}
	obj = actual
	if obj.DeletedAt != nil {
		l.scope.faults++
		return false
	}
	if targetClass == "" || targetClass == obj.StorageClass {
		return false
	}
	if err := l.repo.UpdateObjectStorageClass(ctx, obj.TenantID, obj.Bucket, obj.Key, targetClass); err != nil {
		l.scope.failed(err)
		l.logger.Warn("lifecycle transition update", "key", obj.Key, "from", obj.StorageClass, "to", targetClass, "err", err)
		return false
	}
	return l.scope.ready(ctx) == nil
}
