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

type scrubSettings struct {
	enabled bool
	batch   int
}

type scrubResult uint8

const (
	scrubSkipped scrubResult = iota
	scrubIntact
	scrubCorrupt
	scrubFault
	scrubScopeRefused
	scrubScopeFault
)

// Only verified/skipped observations count as scanned; refused or uncertain
// observations and failed effects never count as verification or corruption.
func (j *Job) scrubAll(ctx context.Context, tenant string, ss scrubSettings) (scanned, corrupt int) {
	if !ss.enabled {
		return 0, 0
	}
	batch := ss.batch
	if batch <= 0 {
		batch = 100
	}
	j.walkObjectRows(ctx, tenant, batch, func(bucket string, obj repository.Object) {
		result, err := j.scrubObjectResult(ctx, obj, tenant, bucket)
		switch result {
		case scrubIntact, scrubSkipped:
			scanned++
		case scrubCorrupt:
			scanned++
			corrupt++
		case scrubFault:
			j.scope.faults++
			j.logger.Warn("scrub: object fault", "key", obj.Key, "err", err)
		case scrubScopeRefused, scrubScopeFault:
			j.logger.Warn("scrub: object scope refused", "key", obj.Key, "err", err)
		}
	})
	return scanned, corrupt
}

// Preserve the legacy single-object no-op when its exact row has already gone.
// No blob is opened in that case; scrubAll still reports the observation fault.
// Configured boundaries and nested NotFound/provider faults never get this path.
func (j *Job) scrubObject(ctx context.Context, obj repository.Object) error {
	_, err := j.scrubObjectResult(ctx, obj, "", "")
	if j.scope.boundary == nil && !j.coverageConfigured && errors.Is(err, errScopeObjectGone) {
		return nil
	}
	return err
}

func scrubAdmission(outcome scopeOutcome) scrubResult {
	if outcome == scopeRefused {
		return scrubScopeRefused
	}
	return scrubScopeFault
}

func (j *Job) scrubObjectResult(ctx context.Context, obj repository.Object, tenant, bucket string) (scrubResult, error) {
	if err := j.validateOrphanConfiguration(false); err != nil {
		j.scope.faults++
		return scrubScopeFault, err
	}
	actual, outcome, err := j.scope.object(ctx, obj, tenant, bucket)
	if outcome != scopeAdmitted {
		return scrubAdmission(outcome), err
	}
	obj = actual
	if obj.DeletedAt != nil || isDeleteMarker(obj) {
		return scrubSkipped, nil
	}
	md5b64 := obj.Metadata["_aero_content_md5"]
	if md5b64 == "" {
		return scrubSkipped, nil
	}
	expected, err := base64.StdEncoding.DecodeString(md5b64)
	if err != nil {
		return scrubFault, err
	}
	if len(expected) != md5.Size {
		return scrubFault, errors.New("scrub: invalid Content-MD5 length")
	}
	return j.verifyScrubContent(ctx, obj, expected)
}

func (j *Job) verifyScrubContent(ctx context.Context, obj repository.Object, expected []byte) (scrubResult, error) {
	computed, err := j.readScrubDigest(ctx, obj)
	if ordinaryMissing(err, storage.ErrNotFound) {
		return scrubSkipped, nil
	}
	if err != nil {
		return scrubFault, err
	}
	// Recheck exact identity and binding after reading, before key-level metadata
	// or chunk invalidation. The digest itself must not change mid-observation.
	actual, outcome, err := j.scope.object(ctx, obj, obj.TenantID, obj.Bucket)
	if outcome != scopeAdmitted {
		return scrubAdmission(outcome), err
	}
	if actual.Metadata["_aero_content_md5"] != obj.Metadata["_aero_content_md5"] {
		return scrubFault, scopeRowFault(errors.New("scrub digest identity changed"))
	}
	obj = actual
	if bytes.Equal(computed, expected) {
		if err := j.clearCorruptFlag(ctx, obj); err != nil {
			return scrubFault, err
		}
		telemetry.IncScrubResult(ctx, "ok")
		return scrubIntact, nil
	}
	return j.markScrubCorrupt(ctx, obj)
}

// Both reading and Close finish before any verdict or metadata mutation. A
// close/cancel/read failure is not evidence that persisted content is corrupt.
func (j *Job) readScrubDigest(ctx context.Context, obj repository.Object) ([]byte, error) {
	if err := scopeContext(ctx); err != nil {
		return nil, err
	}
	rc, info, err := j.store.Get(ctx, obj.StorageKey)
	if err != nil {
		if rc != nil {
			err = errors.Join(err, rc.Close())
		}
		return nil, errors.Join(err, scopeContext(ctx))
	}
	if rc == nil {
		return nil, errors.New("scrub: missing storage reader")
	}
	if info.Key != obj.StorageKey {
		return nil, errors.Join(scopeRowFault(errors.New("scrub storage key echo")), rc.Close())
	}
	h := md5.New()
	_, readErr := io.Copy(h, scrubContextReader{ctx: ctx, reader: rc})
	if err := errors.Join(readErr, rc.Close(), scopeContext(ctx)); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}

type scrubContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r scrubContextReader) Read(p []byte) (int, error) {
	if err := scopeContext(r.ctx); err != nil {
		return 0, err
	}
	n, err := r.reader.Read(p)
	if contextErr := scopeContext(r.ctx); contextErr != nil {
		return n, errors.Join(err, contextErr)
	}
	return n, err
}

func (j *Job) markScrubCorrupt(ctx context.Context, obj repository.Object) (scrubResult, error) {
	err := j.repo.SetObjectMetaKey(ctx, obj.TenantID, obj.Bucket, obj.Key, "_aero_scrub_status", "corrupt")
	if err = errors.Join(err, scopeContext(ctx)); err != nil {
		j.logger.Warn("scrub: failed to mark corrupt", "key", obj.Key, "err", err)
		return scrubFault, err
	}
	// As with deletion, cleanup failures warn and do not undo the corrupt flag.
	// They do, however, prevent a successful completed-scrub outcome.
	if err := deleteScopedObjectChunks(ctx, j.chunkCleaner, obj); err != nil {
		j.logger.Warn("chunk cleanup during reconcile delete failed", "id", obj.ID, "key", obj.Key, "err", err)
		return scrubFault, err
	}
	if err := scopeContext(ctx); err != nil {
		return scrubFault, err
	}
	telemetry.IncScrubResult(ctx, "corrupt")
	j.logger.Warn("scrub: CORRUPT object detected", "tenant", obj.TenantID, "bucket", obj.Bucket, "key", obj.Key, "storage_key", obj.StorageKey)
	return scrubCorrupt, errors.New("corrupt")
}

// Failure leaves the object locked for a later retry, but is not verification.
func (j *Job) clearCorruptFlag(ctx context.Context, obj repository.Object) error {
	if err := scopeContext(ctx); err != nil {
		return err
	}
	if obj.Metadata["_aero_scrub_status"] != "corrupt" {
		return nil
	}
	err := j.repo.DeleteObjectMetaKey(ctx, obj.TenantID, obj.Bucket, obj.Key, "_aero_scrub_status")
	if err = errors.Join(err, scopeContext(ctx)); err != nil {
		j.logger.Warn("scrub: failed to clear corrupt flag", "tenant", obj.TenantID, "bucket", obj.Bucket, "key", obj.Key, "err", err)
		return err
	}
	telemetry.IncScrubResult(ctx, "repaired")
	j.logger.Info("scrub: repaired object cleared", "tenant", obj.TenantID, "bucket", obj.Bucket, "key", obj.Key, "storage_key", obj.StorageKey)
	return nil
}
