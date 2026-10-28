// Package reconcile is the background sweeper that compares the metadata
// repository against the storage backend.
//
// Two divergences are detected, in both directions:
//
//   - DB row exists but storage object is missing → "orphan_row" (likely caused
//     by an out-of-band storage deletion or a failed cleanup). The row is
//     soft-deleted so it leaves the active set; an admin can still restore it.
//   - Storage object exists but no DB row references it → "orphan_blob" (e.g. a
//     PUT whose blob landed but whose DB write failed, or a hard delete whose
//     row was removed before its blob). Cleanup is opt-in via deleteOrphanBlobs
//     (env RECONCILE_DELETE_ORPHAN_BLOBS=true) and only acts on blobs older than
//     a grace period, so an in-flight upload that has not yet committed its DB
//     row is never deleted.
//
// The job is opt-in via RECONCILE_INTERVAL_MINUTES > 0.
package reconcile

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/cluster"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/spaces"
	"github.com/aero-vault/aero-vault/internal/storage"
	"github.com/aero-vault/aero-vault/internal/telemetry"
)

// leaseReconcileSweep is the distributed-lease name guarding the sweep so that,
// across replicas, only one instance runs it per round.
const leaseReconcileSweep = "reconcile-sweep"

type Job struct {
	repo               repository.Repository
	store              storage.Storage
	interval           time.Duration
	deleteOrphanBlobs  bool
	gracePeriod        time.Duration
	tenants            []string // tenants to scan; empty means scan default only
	singleton          *cluster.Singleton
	logger             *slog.Logger
	scrub              scrubSettings
	chunkCleaner       ChunkCleaner
	scope              scopeState
	orphanCoverage     spaces.CoverageReader
	coverageConfigured bool
}

func (j *Job) WithChunkCleaner(cleaner ChunkCleaner) *Job {
	j.chunkCleaner = cleaner
	return j
}

// WithResourceBoundary injects the deny-only binding reader used before every
// row-side storage or cleanup effect. An explicit nil remains configured and
// faults rather than falling back to legacy Allow.
func (j *Job) WithResourceBoundary(reader spaces.BindingReader) *Job {
	j.scope = newScopeState(reader)
	j.scope.repo = j.repo
	return j
}

// WithOrphanCoverage supplies aggregate observation evidence, never ownership
// or a grant to delete an unknown orphan. Configure this independently of binding.
func (j *Job) WithOrphanCoverage(reader spaces.CoverageReader) *Job {
	j.orphanCoverage, j.coverageConfigured = reader, true
	return j
}

// ValidateResourceBoundary is side-effect-free production preflight for both
// reconcile and scrub. Legacy constructors remain optional only at runtime.
func (j *Job) ValidateResourceBoundary() error {
	if j == nil {
		return scopeRowFault(errors.New("missing reconcile job"))
	}
	if scopePortMissing(j.store) {
		return scopeRowFault(errors.New("missing reconcile storage"))
	}
	return errors.Join(j.scope.validate(), j.validateOrphanConfiguration(true))
}

func (j *Job) validateOrphanConfiguration(requireCoverage bool) error {
	configured := j.scope.boundary != nil || j.coverageConfigured
	if j.deleteOrphanBlobs && configured {
		return scopeRowFault(errors.New("configured orphan deletion has no exact ownership authority"))
	}
	if (requireCoverage || j.coverageConfigured) && scopePortMissing(j.orphanCoverage) {
		return scopeRowFault(errors.New("missing orphan coverage reader"))
	}
	if j.coverageConfigured && j.scope.boundary == nil {
		return scopeRowFault(errors.New("missing orphan binding reader"))
	}
	return nil
}

func (j *Job) ready(ctx context.Context) bool {
	if err := j.scope.ready(ctx); err != nil {
		j.logger.Warn("reconcile: boundary unavailable", "err", err)
		return false
	}
	if err := j.validateOrphanConfiguration(false); err != nil {
		j.scope.faults++
		j.logger.Warn("reconcile: unsafe orphan configuration", "err", err)
		return false
	}
	return true
}

// WithScrub enables the data-integrity scrub that verifies stored Content-MD5
// values every sweep cycle. batch controls page size (default 100).
func (j *Job) WithScrub(enabled bool, batch int) *Job {
	j.scrub = scrubSettings{enabled: enabled, batch: batch}
	return j
}

// WithClusterSingleton makes the sweep run on only one replica at a time, gated
// by a repository lease held by `holder` (a per-instance id). Without it, every
// replica sweeps independently (fine for a single instance).
func (j *Job) WithClusterSingleton(holder string) *Job {
	j.singleton.Enable(holder)
	return j
}

func New(repo repository.Repository, store storage.Storage, interval time.Duration, deleteOrphanBlobs bool, gracePeriod time.Duration, tenants []string, logger *slog.Logger) *Job {
	if logger == nil {
		logger = slog.Default()
	}
	if len(tenants) == 0 {
		tenants = []string{"default"}
	}
	return &Job{repo: repo, store: store, interval: interval, deleteOrphanBlobs: deleteOrphanBlobs, gracePeriod: gracePeriod, tenants: tenants, singleton: cluster.NewSingleton(repo, leaseReconcileSweep, logger), logger: logger, scope: scopeState{repo: repo}}
}

// Run blocks until ctx is canceled. It runs an initial sweep immediately, then
// every j.interval. Intentionally cheap (single-threaded, batched).
func (j *Job) Run(ctx context.Context) {
	if j.interval <= 0 || !j.ready(ctx) {
		return
	}
	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()
	j.maybeSweep(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			j.maybeSweep(ctx)
		}
	}
}

// maybeSweep runs the sweep, gated by the cluster singleton when enabled. The
// lease TTL is 2× the interval so the holder keeps renewing each round; if it
// dies, the lease frees after ~2 intervals and another replica takes over.
func (j *Job) maybeSweep(ctx context.Context) {
	if !j.ready(ctx) {
		return
	}
	j.singleton.Guard(ctx, 2*j.interval, j.sweep)
}

// sweep reconciles every configured tenant in both directions: orphan rows
// (DB row without a blob) and orphan blobs (blob without a DB row).
func (j *Job) sweep(ctx context.Context) {
	start := time.Now()
	j.scope.resetCounts()
	if !j.ready(ctx) {
		return
	}
	var scanned, orphanRows, orphanBlobs, deletedBlobs, scrubCorrupt int
	for _, t := range j.tenants {
		sc, or := j.sweepOrphanRows(ctx, t)
		ob, db := j.sweepOrphanBlobs(ctx, t)
		scanned += sc
		orphanRows += or
		orphanBlobs += ob
		deletedBlobs += db
		scrubScanned, corrupt := j.scrubAll(ctx, t, j.scrub)
		scanned += scrubScanned
		scrubCorrupt += corrupt
	}
	telemetry.RecordReconcileBlobs(ctx, orphanBlobs, deletedBlobs)
	j.logger.Info("reconcile sweep done",
		"scanned", scanned,
		"orphan_rows", orphanRows,
		"orphan_blobs", orphanBlobs,
		"orphan_blobs_deleted", deletedBlobs,
		"scrub_corrupt", scrubCorrupt,
		"scope_refused", j.scope.refused,
		"scope_faults", j.scope.faults,
		"delete_enabled", j.deleteOrphanBlobs,
		"duration_ms", time.Since(start).Milliseconds())
}

// sweepOrphanRows soft-deletes rows whose backing blob is missing, across every
// bucket the tenant owns. Returns (rows scanned, orphan rows found).
func (j *Job) sweepOrphanRows(ctx context.Context, tenant string) (scanned, orphans int) {
	j.walkObjectRows(ctx, tenant, 200, func(bucket string, obj repository.Object) {
		orphan, counted := j.reconcileObjectRow(ctx, tenant, bucket, obj)
		if counted {
			scanned++
		}
		if orphan {
			orphans++
		}
	})
	return scanned, orphans
}

func (j *Job) walkObjectRows(ctx context.Context, tenant string, batch int, visit func(string, repository.Object)) {
	if !j.ready(ctx) {
		return
	}
	if err := spaces.ValidateTenant(tenant); err != nil {
		j.scope.faults++
		return
	}
	buckets, err := j.repo.ListBuckets(ctx, tenant)
	if err = errors.Join(err, scopeContext(ctx)); err != nil {
		j.scope.faults++
		j.logger.Warn("reconcile: list buckets", "err", err)
		return
	}
	seen := map[string]bool{}
	for _, bucket := range buckets {
		if err := spaces.ValidateBindingInput(tenant, bucket); err != nil || seen[bucket] {
			j.scope.faults++
			return
		}
		seen[bucket] = true
	}
	for _, bucket := range buckets {
		objects, err := j.materializeObjectRows(ctx, tenant, bucket, batch)
		if err != nil {
			j.scope.faults++
			j.logger.Warn("reconcile: incomplete object listing", "err", err)
			continue
		}
		for _, obj := range objects {
			visit(bucket, obj)
		}
	}
}

func (j *Job) materializeObjectRows(ctx context.Context, tenant, bucket string, batch int) ([]repository.Object, error) {
	var all []repository.Object
	marker, previous := "", ""
	for {
		if err := scopeContext(ctx); err != nil {
			return nil, err
		}
		page, err := j.repo.ListObjects(ctx, tenant, bucket, "", marker, batch)
		if err = errors.Join(err, scopeContext(ctx)); err != nil {
			return nil, scopeRowFault(err)
		}
		previous, err = validateObjectPage(page, tenant, bucket, marker, previous)
		if err != nil {
			return nil, err
		}
		all = append(all, page.Objects...)
		if !page.HasMore {
			return all, nil
		}
		marker = page.NextMarker
	}
}

func (j *Job) reconcileObjectRow(ctx context.Context, tenant, bucket string, obj repository.Object) (orphan, counted bool) {
	if !j.ready(ctx) {
		return false, false
	}
	actual, outcome, scopeErr := j.scope.object(ctx, obj, tenant, bucket)
	if outcome != scopeAdmitted {
		j.logger.Warn("reconcile: row scope refused",
			"tenant", tenant, "bucket", bucket, "key", obj.Key, "err", scopeErr)
		return false, false
	}
	obj = actual
	if obj.DeletedAt != nil || isDeleteMarker(obj) {
		j.scope.faults++
		return false, false
	}
	info, statErr := j.store.Stat(ctx, obj.StorageKey)
	statErr = errors.Join(statErr, scopeContext(ctx))
	switch {
	case statErr == nil:
		if info.Key != obj.StorageKey {
			j.scope.faults++
			return false, false
		}
		return false, true
	case ordinaryMissing(statErr, storage.ErrNotFound):
		deleted := j.softDeleteMissingRow(ctx, obj)
		return deleted, deleted
	default:
		j.scope.faults++
		j.logger.Warn("reconcile: stat object", "tenant", tenant,
			"bucket", obj.Bucket, "key", obj.Key, "err", statErr)
		return false, false
	}
}

func (j *Job) softDeleteMissingRow(ctx context.Context, obj repository.Object) bool {
	j.logger.Warn("reconcile: storage missing for DB row", "tenant", obj.TenantID, "bucket", obj.Bucket, "key", obj.Key, "storage_key", obj.StorageKey)
	actual, outcome, _ := j.scope.object(ctx, obj, obj.TenantID, obj.Bucket)
	if outcome != scopeAdmitted {
		return false
	}
	if err := softDeleteKey(ctx, j.repo, j.chunkCleaner, actual, j.logger); err != nil {
		j.scope.failed(err)
		j.logger.Warn("reconcile: soft-delete orphan row", "key", obj.Key, "err", err)
		return false
	}
	return j.scope.ready(ctx) == nil
}

// sweepOrphanBlobs finds blobs in storage with no DB row and, when enabled,
// deletes those older than the grace period. Returns (orphan blobs found,
// blobs deleted). With deleteOrphanBlobs=false it only detects and logs — the
// safe default. Configured boundaries reject deletion: coverage is not ownership.
// The blob walk is scoped to the tenant's "<tenant>/" prefix,
// which also excludes backend-internal paths such as ".multipart/".
func (j *Job) sweepOrphanBlobs(ctx context.Context, tenant string) (orphans, deleted int) {
	if !j.ready(ctx) {
		return 0, 0
	}
	if err := spaces.ValidateTenant(tenant); err != nil {
		j.scope.faults++
		return 0, 0
	}
	if !j.checkOrphanCoverage(ctx, tenant) {
		return 0, 0
	}
	referenced, err := j.collectReferencedKeys(ctx)
	if err != nil {
		j.scope.faults++
		j.logger.Warn("reconcile: collect referenced keys", "err", err)
		return 0, 0
	}
	prefix := tenant + "/"
	allKeys, keyAge, complete := j.listStorageObjects(ctx, prefix)
	if !complete {
		j.scope.faults++
		j.logger.Warn("reconcile: incomplete blob listing; orphan deletion skipped", "tenant", tenant)
		return 0, 0
	}
	if !j.checkOrphanCoverage(ctx, tenant) {
		return 0, 0
	}
	return j.handleOrphanBlobs(ctx, tenant, findStaleBlobs(allKeys, referenced), keyAge)
}

func (j *Job) handleOrphanBlobs(ctx context.Context, tenant string, stale []string, keyAge map[string]time.Time) (orphans, deleted int) {
	if !j.ready(ctx) {
		return 0, 0
	}
	for _, staleKey := range stale {
		if err := j.scope.ready(ctx); err != nil {
			return orphans, deleted
		}
		orphans++
		if j.gracePeriod > 0 && time.Since(keyAge[staleKey]) <= j.gracePeriod {
			continue
		}
		if !j.deleteOrphanBlobs {
			j.logger.Warn("reconcile: orphan blob (cleanup disabled)",
				"tenant", tenant, "storage_key", staleKey, "last_modified", keyAge[staleKey])
			continue
		}
		if err := errors.Join(j.store.Delete(ctx, staleKey), scopeContext(ctx)); err != nil {
			j.scope.faults++
			j.logger.Warn("reconcile: orphan blob delete", "tenant", tenant, "storage_key", staleKey, "err", err)
			continue
		}
		deleted++
		j.logger.Info("reconcile: orphan blob deleted", "tenant", tenant, "storage_key", staleKey)
	}
	return orphans, deleted
}

func (j *Job) listStorageObjects(ctx context.Context, prefix string) ([]string, map[string]time.Time, bool) {
	var allKeys []string
	keyAge := map[string]time.Time{}
	var marker, previous string
	markers := map[string]bool{"": true}
	for {
		if err := scopeContext(ctx); err != nil {
			return nil, nil, false
		}
		page, err := j.store.List(ctx, prefix, marker, 200)
		if err = errors.Join(err, scopeContext(ctx)); err != nil {
			j.logger.Warn("reconcile: list blobs", "prefix", prefix, "err", err)
			return nil, nil, false
		}
		previous, err = validateStoragePage(page, prefix, previous)
		if err != nil {
			j.logger.Warn("reconcile: invalid blob page", "err", err)
			return nil, nil, false
		}
		for _, oi := range page.Objects {
			allKeys = append(allKeys, oi.Key)
			keyAge[oi.Key] = oi.LastModified
		}
		if !page.HasMore {
			break
		}
		if markers[page.NextMarker] {
			return nil, nil, false
		}
		marker = page.NextMarker
		markers[marker] = true
	}
	return allKeys, keyAge, true
}

func validateStoragePage(page storage.ListResult, prefix, previous string) (string, error) {
	for _, obj := range page.Objects {
		if !strings.HasPrefix(obj.Key, prefix) || obj.Key <= previous || obj.LastModified.IsZero() || obj.Size < 0 {
			return "", scopeRowFault(errors.New("blob page scope, identity or key progress"))
		}
		previous = obj.Key
	}
	if page.HasMore && (len(page.Objects) == 0 || page.NextMarker == "") {
		return "", scopeRowFault(errors.New("blob page has no marker progress"))
	}
	return previous, nil
}

// collectReferencedKeys builds a set of every storage_key that exists in the
// metadata repository, including keys from soft-deleted rows (they still pin
// their blob). The set is used by sweepOrphanBlobs to identify unreferenced
// storage blobs.
func (j *Job) collectReferencedKeys(ctx context.Context) (map[string]bool, error) {
	if err := scopeContext(ctx); err != nil {
		return nil, err
	}
	keys, err := j.repo.ListStorageKeys(ctx)
	if err = errors.Join(err, scopeContext(ctx)); err != nil {
		return nil, err
	}
	ref := make(map[string]bool, len(keys))
	for _, k := range keys {
		if k == "" {
			return nil, scopeRowFault(errors.New("empty referenced storage key"))
		}
		ref[k] = true
	}
	return ref, nil
}

// Aggregate P0/materialization/P1 is observation admission only. Even complete
// all-unbound coverage cannot assign an unknown blob to a bucket or enable GC.
func (j *Job) checkOrphanCoverage(ctx context.Context, tenant string) bool {
	if j.scope.boundary == nil && !j.coverageConfigured {
		return true
	}
	boundary := access.NewResourceBoundary(j.scope.binding, j.orphanCoverage)
	outcome, err := scopeBoundaryOutcome(boundary.CheckCoverage(ctx, spaces.CoverageScope{Kind: spaces.CoverageTenant, TenantID: tenant}))
	if err == nil {
		return true
	}
	j.scope.observe(outcome)
	j.logger.Warn("reconcile: orphan coverage refused", "err", err)
	return false
}

// findStaleBlobs returns storage keys that exist in allKeys but not in the
// referenced set.
func findStaleBlobs(allKeys []string, referenced map[string]bool) []string {
	var stale []string
	for _, k := range allKeys {
		if !referenced[k] {
			stale = append(stale, k)
		}
	}
	return stale
}
