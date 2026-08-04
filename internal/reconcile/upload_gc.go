package reconcile

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/aero-vault/aero-vault/internal/cluster"
	"github.com/aero-vault/aero-vault/internal/repository"
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
	}
}

// WithClusterSingleton makes the upload GC run on only one replica at a time.
func (j *UploadGCJob) WithClusterSingleton(holder string) *UploadGCJob {
	j.singleton.Enable(holder)
	return j
}

// Run blocks until ctx is canceled. Runs an initial sweep, then every interval.
func (j *UploadGCJob) Run(ctx context.Context) {
	if j.interval <= 0 || j.ttl <= 0 {
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
	j.singleton.Guard(ctx, 2*j.interval, j.sweep)
}

func (j *UploadGCJob) sweep(ctx context.Context) {
	before := time.Now().Add(-j.ttl).UTC().Format(time.RFC3339Nano)
	expired := j.listExpiredUploads(ctx, before)
	zombies := j.listZombieUploads(ctx, before)
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
	)
}

func (j *UploadGCJob) listExpiredUploads(ctx context.Context, before string) []repository.Upload {
	uploads, err := j.repo.ListExpiredUploads(ctx, before, 200)
	if err != nil {
		j.logger.Warn("upload gc list expired", "err", err)
		return nil
	}
	return uploads
}

func (j *UploadGCJob) listZombieUploads(ctx context.Context, before string) []repository.Upload {
	uploads, err := j.repo.ListZombieUploads(ctx, before, 200)
	if err != nil {
		j.logger.Warn("upload gc list zombies", "err", err)
		return nil
	}
	return uploads
}

func mergeUploadCandidates(groups ...[]repository.Upload) []repository.Upload {
	seen := make(map[string]struct{})
	var merged []repository.Upload
	for _, uploads := range groups {
		for _, upload := range uploads {
			if _, exists := seen[upload.ID]; exists {
				continue
			}
			seen[upload.ID] = struct{}{}
			merged = append(merged, upload)
		}
	}
	return merged
}

// purgeUploads removes the DB record only after storage cleanup succeeds, so a
// transient backend failure remains retryable on the next sweep.
func (j *UploadGCJob) purgeUploads(ctx context.Context, uploads []repository.Upload) int {
	purged := 0
	for _, u := range uploads {
		if err := j.store.CleanupParts(ctx, u.StorageKey, u.BackendUID); err != nil && !errors.Is(err, storage.ErrNotFound) {
			j.logger.Warn("upload gc storage cleanup", "upload_id", u.ID, "err", err)
			continue
		}
		if err := j.repo.DeleteUploadCascade(ctx, u.ID); err != nil && !errors.Is(err, repository.ErrUploadNotFound) {
			j.logger.Warn("upload gc db delete", "upload_id", u.ID, "err", err)
			continue
		}
		purged++
	}
	return purged
}
