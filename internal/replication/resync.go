package replication

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/spaces"
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
	boundary *access.ResourceBoundary
	logger   *slog.Logger
}

func NewResyncer(store ResyncStore, queue Enqueuer, interval time.Duration, logger *slog.Logger) *Resyncer {
	if logger == nil {
		logger = slog.Default()
	}
	reader, _ := store.(spaces.BindingReader)
	return &Resyncer{store: store, queue: queue, interval: interval,
		boundary: access.NewResourceBoundary(reader, nil), logger: logger}
}

// WithResourceBoundary injects the raw deny-only binding reader used for each
// listed row before its enqueue. An explicit missing reader stays a fault; it
// never falls back to the constructor-discovered port.
func (r *Resyncer) WithResourceBoundary(reader spaces.BindingReader) *Resyncer {
	r.boundary = access.NewResourceBoundary(reader, nil)
	return r
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

// resyncCounts keeps refusals distinguishable from successful enqueues and
// from rows that were already terminal or deduplicated.
type resyncCounts struct {
	scanned  int
	enqueued int
	skipped  int
	faults   int
}

func (c *resyncCounts) add(other resyncCounts) {
	c.scanned += other.scanned
	c.enqueued += other.enqueued
	c.skipped += other.skipped
	c.faults += other.faults
}

// sweep runs one complete pass. Individual repository and enqueue failures are
// logged and skipped so a bad bucket cannot stop recovery elsewhere.
func (r *Resyncer) sweep(ctx context.Context) (scanned, enqueued int) {
	counts := r.sweepDetailed(ctx)
	return counts.scanned, counts.enqueued
}

func (r *Resyncer) sweepDetailed(ctx context.Context) resyncCounts {
	started := time.Now()
	var counts resyncCounts
	tenants := map[string]struct{}{"default": {}}
	if records, err := r.store.ListTenants(ctx); err != nil {
		r.logger.Warn("replication resync: list tenants", "err", err)
		counts.faults++
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
			counts.faults++
			continue
		}
		for _, bucket := range buckets {
			counts.add(r.sweepBucket(ctx, tenant, bucket))
		}
	}
	r.logger.Info("replication resync sweep done", "scanned", counts.scanned,
		"enqueued", counts.enqueued, "skipped", counts.skipped, "faults", counts.faults,
		"duration_ms", time.Since(started).Milliseconds())
	return counts
}

func (r *Resyncer) sweepBucket(ctx context.Context, tenant, bucket string) resyncCounts {
	marker := ""
	var counts resyncCounts
	for {
		page, err := r.store.ListObjects(ctx, tenant, bucket, "", marker, resyncPageSize)
		if err != nil {
			r.logger.Warn("replication resync: list objects", "tenant", tenant,
				"bucket", bucket, "err", err)
			counts.faults++
			return counts
		}
		for _, obj := range page.Objects {
			counts.add(r.sweepObject(ctx, tenant, bucket, obj))
		}
		if !page.HasMore {
			return counts
		}
		if page.NextMarker == "" || page.NextMarker == marker {
			r.logger.Warn("replication resync: invalid page marker", "tenant", tenant,
				"bucket", bucket, "marker", marker)
			counts.faults++
			return counts
		}
		marker = page.NextMarker
	}
}

func (r *Resyncer) sweepObject(ctx context.Context, tenant, bucket string, obj repository.Object) resyncCounts {
	counts := resyncCounts{scanned: 1}
	if err := validateResyncObject(tenant, bucket, obj); err != nil {
		r.logger.Warn("replication resync: invalid object scope", "tenant", tenant,
			"bucket", bucket, "object_id", obj.ID, "err", err)
		counts.faults++
		return counts
	}
	// "skipped" is terminal for objects that cannot be copied (currently
	// SSE-C). Treating it as pending would make every resync interval
	// enqueue the same job forever.
	if status := obj.Tags[TagStatus]; status == "replicated" || status == "skipped" {
		counts.skipped++
		return counts
	}
	if r.queue == nil {
		r.logger.Warn("replication resync: queue unavailable", "object_id", obj.ID)
		counts.faults++
		return counts
	}
	if err := r.boundary.CheckBucket(ctx, obj.TenantID, obj.Bucket); err != nil {
		if access.IsResourceBoundaryDenied(err) {
			r.logger.Warn("replication resync: resource boundary refused object",
				"tenant", obj.TenantID, "bucket", obj.Bucket, "object_id", obj.ID)
			counts.skipped++
		} else {
			r.logger.Warn("replication resync: resource boundary observation failed",
				"tenant", obj.TenantID, "bucket", obj.Bucket, "object_id", obj.ID, "err", err)
			counts.faults++
		}
		return counts
	}
	_, deduped, err := r.queue.Enqueue(ctx, replicateJob(obj.TenantID, obj.ID))
	switch {
	case err != nil:
		r.logger.Warn("replication resync: enqueue", "object_id", obj.ID, "err", err)
		counts.faults++
	case deduped:
		counts.skipped++
	default:
		counts.enqueued++
	}
	return counts
}

// validateResyncObject requires the listed row to echo the exact list scope and
// carry a complete, positive identity before it can become a job.
func validateResyncObject(tenant, bucket string, obj repository.Object) error {
	if obj.TenantID != tenant {
		return fmt.Errorf("row tenant %q does not match listing tenant %q", obj.TenantID, tenant)
	}
	if obj.Bucket != bucket {
		return fmt.Errorf("row bucket %q does not match listing bucket %q", obj.Bucket, bucket)
	}
	ref := access.ChunkRef{TenantID: obj.TenantID, Bucket: obj.Bucket, Key: obj.Key, ObjectID: obj.ID}
	return access.ValidateChunkRef(ref)
}
