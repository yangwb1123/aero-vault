package reconcile

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"errors"
	"io"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/storage"
	"github.com/aero-vault/aero-vault/internal/telemetry"
)

// scrubSettings controls the data-integrity scrub feature.
type scrubSettings struct {
	enabled bool
	batch   int
}

// scrubAll iterates over every active object in the tenant, verifies its
// stored Content-MD5, and returns the count of scanned and corrupt objects.
func (j *Job) scrubAll(ctx context.Context, tenant string, ss scrubSettings) (scanned, corrupt int) {
	if !ss.enabled {
		return 0, 0
	}
	buckets, err := j.repo.ListBuckets(ctx, tenant)
	if err != nil {
		j.logger.Warn("scrub: list buckets", "tenant", tenant, "err", err)
		return 0, 0
	}
	batch := ss.batch
	if batch <= 0 {
		batch = 100
	}
	for _, bucket := range buckets {
		var marker string
		for {
			page, err := j.repo.ListObjects(ctx, tenant, bucket, "", marker, batch)
			if err != nil {
				j.logger.Warn("scrub: list objects", "tenant", tenant, "bucket", bucket, "err", err)
				break
			}
			for _, obj := range page.Objects {
				scanned++
				if err := j.scrubObject(ctx, obj); err != nil {
					corrupt++
				}
			}
			if !page.HasMore {
				break
			}
			marker = page.NextMarker
		}
	}
	return scanned, corrupt
}

// scrubObject verifies a single object's integrity by comparing the stored
// _aero_content_md5 against a freshly computed MD5 of the storage content.
// Returns nil when intact, skipped, or on transient error (non-fatal).
func (j *Job) scrubObject(ctx context.Context, obj repository.Object) error {
	md5b64, ok := obj.Metadata["_aero_content_md5"]
	if !ok || md5b64 == "" {
		return nil
	}
	expected, err := base64.StdEncoding.DecodeString(md5b64)
	if err != nil {
		j.logger.Warn("scrub: invalid _aero_content_md5 base64", "key", obj.Key, "err", err)
		return nil
	}
	rc, _, err := j.store.Get(ctx, obj.StorageKey)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil
		}
		j.logger.Warn("scrub: get failed", "key", obj.Key, "err", err)
		return nil
	}
	defer rc.Close()

	h := md5.New()
	if _, err := io.Copy(h, rc); err != nil {
		j.logger.Warn("scrub: read failed", "key", obj.Key, "err", err)
		return nil
	}
	computed := h.Sum(nil)
	if bytes.Equal(computed, expected) {
		telemetry.IncScrubResult(ctx, "ok")
		return nil
	}
	telemetry.IncScrubResult(ctx, "corrupt")
	if err := j.repo.SetObjectMetaKey(ctx, obj.TenantID, obj.Bucket, obj.Key, "_aero_scrub_status", "corrupt"); err != nil {
		j.logger.Warn("scrub: failed to mark corrupt", "key", obj.Key, "err", err)
	}
	j.logger.Warn("scrub: CORRUPT object detected",
		"tenant", obj.TenantID, "bucket", obj.Bucket, "key", obj.Key, "storage_key", obj.StorageKey)
	return errors.New("corrupt")
}
