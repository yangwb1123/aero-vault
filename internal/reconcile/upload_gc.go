package reconcile

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"time"

	"github.com/aero-vault/aero-vault/internal/cluster"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/spaces"
	"github.com/aero-vault/aero-vault/internal/storage"
)

// UploadGCJob periodically cleans up expired and zombie multipart uploads.
// Without it, partial uploads (client disconnected before CompleteMultipart or
// AbortMultipart) accumulate forever in both the DB and the storage backend.
// The job mirrors the pattern of LifecycleJob and RetentionJob.
//
// Two sweep strategies:
//  1. Time-based: uploads whose created_at is older than the TTL.
//  2. Zombie detection: uploads that have parts recorded but were never
//     completed (parts exist but no CompleteMultipart was called).
const leaseUploadGC = "upload-gc"

type UploadGCJob struct {
	repo      repository.Repository
	store     storage.Storage
	interval  time.Duration
	ttl       time.Duration // uploads older than this are eligible for cleanup
	singleton *cluster.Singleton
	scope     scopeState
	logger    *slog.Logger
}

// NewUploadGC creates a new upload GC job. interval controls how often the
// sweep runs; ttl is the age threshold for stale uploads.
func NewUploadGC(repo repository.Repository, store storage.Storage, interval, ttl time.Duration, logger *slog.Logger) *UploadGCJob {
	if logger == nil {
		logger = slog.Default()
	}
	return &UploadGCJob{
		repo:      repo,
		store:     store,
		interval:  interval,
		ttl:       ttl,
		singleton: cluster.NewSingleton(repo, leaseUploadGC, logger),
		logger:    logger,
		scope:     scopeState{repo: repo},
	}
}

// WithClusterSingleton makes the upload GC run on only one replica at a time.
func (j *UploadGCJob) WithClusterSingleton(holder string) *UploadGCJob {
	j.singleton.Enable(holder)
	return j
}

// WithResourceBoundary installs deny-only upload admission before storage
// cleanup or the database cascade. Explicit nil remains a configured fault.
func (j *UploadGCJob) WithResourceBoundary(reader spaces.BindingReader) *UploadGCJob {
	j.scope = newScopeState(reader)
	j.scope.repo = j.repo
	return j
}

// ValidateResourceBoundary is side-effect-free production preflight.
func (j *UploadGCJob) ValidateResourceBoundary() error {
	if j == nil {
		return scopeRowFault(errors.New("missing upload GC job"))
	}
	if scopePortMissing(j.store) {
		return scopeRowFault(errors.New("missing upload GC storage"))
	}
	return j.scope.validate()
}

// Run blocks until ctx is canceled. Runs an initial sweep, then every interval.
func (j *UploadGCJob) Run(ctx context.Context) {
	if j.interval <= 0 || j.ttl <= 0 || j.scope.ready(ctx) != nil {
		return
	}
	t := time.NewTicker(j.interval)
	defer t.Stop()
	j.maybeSweep(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			j.maybeSweep(ctx)
		}
	}
}

// maybeSweep runs the sweep gated by the cluster singleton when enabled.
func (j *UploadGCJob) maybeSweep(ctx context.Context) {
	if err := j.scope.ready(ctx); err != nil {
		j.logger.Warn("upload GC boundary unavailable", "err", err)
		return
	}
	j.singleton.Guard(ctx, 2*j.interval, j.sweep)
}

func (j *UploadGCJob) sweep(ctx context.Context) {
	j.scope.resetCounts()
	if err := j.scope.ready(ctx); err != nil {
		j.logger.Warn("upload GC boundary unavailable", "err", err)
		return
	}
	before := time.Now().Add(-j.ttl).UTC().Format(time.RFC3339Nano)
	expired := j.listExpiredUploads(ctx, before)
	zombies := j.listZombieUploads(ctx, before)
	if j.scope.faults > 0 {
		return
	}
	candidates := mergeUploadCandidates(expired, zombies)
	if len(candidates) == 0 {
		return
	}
	purged := j.purgeUploads(ctx, candidates)
	j.logger.Info("upload gc sweep",
		"expired_candidates", len(expired),
		"zombie_candidates", len(zombies),
		"unique_candidates", len(candidates),
		"purged", purged,
		"scope_refused", j.scope.refused,
		"scope_faults", j.scope.faults,
	)
}

func (j *UploadGCJob) listExpiredUploads(ctx context.Context, before string) []repository.Upload {
	if err := j.scope.ready(ctx); err != nil {
		return nil
	}
	uploads, err := j.repo.ListExpiredUploads(ctx, before, 200)
	if err = errors.Join(err, scopeContext(ctx)); err != nil {
		j.scope.faults++
		j.logger.Warn("upload gc list expired", "err", err)
		return nil
	}
	return uploads
}

func (j *UploadGCJob) listZombieUploads(ctx context.Context, before string) []repository.Upload {
	if err := j.scope.ready(ctx); err != nil {
		return nil
	}
	uploads, err := j.repo.ListZombieUploads(ctx, before, 200)
	if err = errors.Join(err, scopeContext(ctx)); err != nil {
		j.scope.faults++
		j.logger.Warn("upload gc list zombies", "err", err)
		return nil
	}
	return uploads
}

func mergeUploadCandidates(groups ...[]repository.Upload) []repository.Upload {
	seen := make(map[string][]repository.Upload)
	var merged []repository.Upload
	for _, uploads := range groups {
		for _, upload := range uploads {
			if uploadAlreadyObserved(seen[upload.ID], upload) {
				continue
			}
			seen[upload.ID] = append(seen[upload.ID], upload)
			merged = append(merged, upload)
		}
	}
	return merged
}

func uploadAlreadyObserved(observed []repository.Upload, upload repository.Upload) bool {
	for _, prior := range observed {
		if sameUploadObservation(prior, upload) {
			return true
		}
	}
	return false
}

func sameUploadObservation(a, b repository.Upload) bool {
	return sameUploadIdentity(a, b) && a.CreatedAt.Equal(b.CreatedAt) && a.UploadID == b.UploadID &&
		a.ContentType == b.ContentType && a.StorageClass == b.StorageClass && a.TotalParts == b.TotalParts &&
		a.CompletedParts == b.CompletedParts && maps.Equal(a.Metadata, b.Metadata) && maps.Equal(a.Tags, b.Tags)
}

// Stage the whole candidate batch before effects so no first-wins duplicate
// can clean storage while a conflicting identity is still unseen.
func (j *UploadGCJob) admittedUploads(ctx context.Context, uploads []repository.Upload) []repository.Upload {
	uploads = mergeUploadCandidates(uploads)
	counts := map[string]int{}
	for _, upload := range uploads {
		counts[upload.ID]++
	}
	var admitted []repository.Upload
	for _, upload := range uploads {
		if counts[upload.ID] > 1 {
			j.scope.faults++
			j.logger.Warn("upload GC conflicting candidate identity", "upload_id", upload.ID)
			continue
		}
		actual, outcome, err := j.scope.upload(ctx, upload)
		if outcome != scopeAdmitted {
			j.logger.Warn("upload gc upload scope refused", "upload_id", upload.ID, "err", err)
			continue
		}
		admitted = append(admitted, actual)
	}
	return admitted
}

// purgeUploads preserves storage-before-database ordering and retryability.
func (j *UploadGCJob) purgeUploads(ctx context.Context, uploads []repository.Upload) int {
	if err := j.scope.ready(ctx); err != nil {
		return 0
	}
	purged := 0
	for _, upload := range j.admittedUploads(ctx, uploads) {
		if j.purgeUpload(ctx, upload) {
			purged++
		}
	}
	return purged
}

func (j *UploadGCJob) purgeUpload(ctx context.Context, upload repository.Upload) bool {
	u, outcome, _ := j.scope.upload(ctx, upload)
	if outcome != scopeAdmitted {
		return false
	}
	err := errors.Join(j.store.CleanupParts(ctx, u.StorageKey, u.BackendUID), scopeContext(ctx))
	if err != nil && !ordinaryMissing(err, storage.ErrNotFound) {
		j.scope.faults++
		j.logger.Warn("upload gc storage cleanup", "upload_id", u.ID, "err", err)
		return false
	}
	if _, outcome, _ := j.scope.upload(ctx, u); outcome != scopeAdmitted {
		return false
	}
	if err := errors.Join(j.repo.DeleteUploadCascade(ctx, u.ID), scopeContext(ctx)); err != nil {
		j.scope.faults++
		j.logger.Warn("upload gc db delete", "upload_id", u.ID, "err", err)
		return false
	}
	return true
}
