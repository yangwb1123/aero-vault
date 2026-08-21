package replication

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
)

const resyncPageSize = 100

// ResyncStore is the repository slice needed by the replication sweep.
type ResyncStore interface {
	ListTenants(ctx context.Context) ([]repository.TenantRecord, error)
	ListBuckets(ctx context.Context, tenant string) ([]string, error)
	ListObjects(ctx context.Context, tenant, bucket, prefix, marker string, limit int) (repository.ListPage, error)
}

// replicateJob builds the shared job shape used by the event bridge and the
// resync sweep. Keeping both paths here prevents payload or dedupe drift.
func replicateJob(tenantID string, objectID int64) repository.Job {
	return repository.Job{
		TenantID:  tenantID,
		Type:      JobReplicate,
		Payload:   EncodeObjectID(objectID),
		DedupeKey: fmt.Sprintf("%s:%d", JobReplicate, objectID),
	}
}

// Resyncer periodically finds active objects without a successful replication
// status and re-enqueues their idempotent replication jobs.
type Resyncer struct {
	store    ResyncStore
	queue    Enqueuer
	interval time.Duration
	logger   *slog.Logger
}

func NewResyncer(store ResyncStore, queue Enqueuer, interval time.Duration, logger *slog.Logger) *Resyncer {
	if logger == nil {
		logger = slog.Default()
	}
	return &Resyncer{store: store, queue: queue, interval: interval, logger: logger}
}

// Run performs an immediate sweep, then repeats at the configured interval.
// A non-positive interval keeps this opt-in worker disabled.
func (r *Resyncer) Run(ctx context.Context) {
	if r.interval <= 0 {
		return
	}
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	r.sweep(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.sweep(ctx)
		}
	}
}

// sweep runs one complete pass. Individual repository and enqueue failures are
// logged and skipped so a bad bucket cannot stop recovery elsewhere.
func (r *Resyncer) sweep(ctx context.Context) (scanned, enqueued int) {
	started := time.Now()
	tenants := map[string]struct{}{"default": {}}
	if records, err := r.store.ListTenants(ctx); err != nil {
		r.logger.Warn("replication resync: list tenants", "err", err)
	} else {
		for _, record := range records {
			if record.TenantID != "" {
				tenants[record.TenantID] = struct{}{}
			}
		}
	}
	for tenant := range tenants {
		buckets, err := r.store.ListBuckets(ctx, tenant)
		if err != nil {
			r.logger.Warn("replication resync: list buckets", "tenant", tenant, "err", err)
			continue
		}
		for _, bucket := range buckets {
			s, e := r.sweepBucket(ctx, tenant, bucket)
			scanned += s
			enqueued += e
		}
	}
	r.logger.Info("replication resync sweep done", "scanned", scanned,
		"enqueued", enqueued, "duration_ms", time.Since(started).Milliseconds())
	return scanned, enqueued
}

func (r *Resyncer) sweepBucket(ctx context.Context, tenant, bucket string) (scanned, enqueued int) {
	marker := ""
	for {
		page, err := r.store.ListObjects(ctx, tenant, bucket, "", marker, resyncPageSize)
		if err != nil {
			r.logger.Warn("replication resync: list objects", "tenant", tenant,
				"bucket", bucket, "err", err)
			return scanned, enqueued
		}
		for _, obj := range page.Objects {
			scanned++
			// "skipped" is terminal for objects that cannot be copied (currently
			// SSE-C). Treating it as pending would make every resync interval
			// enqueue the same job forever.
			if status := obj.Tags[TagStatus]; status == "replicated" || status == "skipped" {
				continue
			}
			if r.queue == nil {
				r.logger.Warn("replication resync: queue unavailable", "object_id", obj.ID)
				continue
			}
			_, deduped, err := r.queue.Enqueue(ctx, replicateJob(obj.TenantID, obj.ID))
			if err != nil {
				r.logger.Warn("replication resync: enqueue", "object_id", obj.ID, "err", err)
				continue
			}
			if !deduped {
				enqueued++
			}
		}
		if !page.HasMore {
			return scanned, enqueued
		}
		if page.NextMarker == "" || page.NextMarker == marker {
			r.logger.Warn("replication resync: invalid page marker", "tenant", tenant,
				"bucket", bucket, "marker", marker)
			return scanned, enqueued
		}
		marker = page.NextMarker
	}
}
