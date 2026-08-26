package rest

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/telemetry"
	"github.com/aero-vault/aero-vault/internal/thumbnail"
)

// thumbFreshnessMaxAge bounds the client-side freshness of the derived
// thumbnail resource (package constant, not config). The thumbnail URL
// carries no version pin, so a PUT that replaces the source object is
// invisible to caches keyed on the URL; bounded freshness (max-age) plus
// must-revalidate (RFC 9111 §5.2.2.2) forces revalidation once the window
// lapses, which engages the If-None-Match/re-Stat machinery in thumbnail.go —
// a replaced object is observed within this window instead of up to the
// historical 24 h.
const thumbFreshnessMaxAge = 300

// sniffHeadLen is the longest magic signature Sniff recognizes: RIFF(4) +
// size(4) + WEBP(4) = 12 bytes. Bucket-3 openers read exactly this many
// bytes from the object stream to classify it.
const sniffHeadLen = 12

// sniffedStream replays the sniffed magic head in front of the live object
// stream (io.MultiReader) and forwards Close to the underlying stream, which
// the API's deferred close runs on this wrapper — io.NopCloser would leak it
// (its Close is a no-op). Mirrors the close-forwarding precedent of
// service.ETagVerifier.
type sniffedStream struct {
	io.Reader
	rc io.Closer
}

func (s *sniffedStream) Close() error { return s.rc.Close() }

// parseThumbDim validates one ?w=/?h= thumbnail dimension parameter. An
// absent parameter yields 0 (default-size semantics per Generate's contract).
// Present values must parse as a non-negative integer; parse errors and
// negatives are client argument errors that the caller maps to 400 before any
// cache validator is emitted or the decode pipeline is entered.
func parseThumbDim(q url.Values, name string) (int, error) {
	if !q.Has(name) {
		return 0, nil
	}
	v := q.Get(name)
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%w: invalid ?%s value %q (must be a non-negative integer)",
			service.ErrInvalidArgs, name, v)
	}
	return n, nil
}

// statPinned stats the derivation source without opening its blob: the pinned
// version when version is non-empty (StatVersionWithOptions — an
// authorization decision plus a repo read: authorizeObject runs inside, so
// the E7 discriminator can return ErrForbidden/ErrInvalidArgs, not just row
// absence; delete marker → ErrNotFound, corrupt → ErrObjectCorrupt,
// unauthorized → ErrForbidden, SSE-C without key → ErrInvalidArgs), else the
// current object (Stat). Both classify via writeError exactly like the
// unpinned pre-open Stat, so the pinned arm is at parity, not a regression.
func (h *Handler) statPinned(ctx context.Context, tenant, key, version string) (repository.Object, error) {
	if version == "" {
		return h.svc.Stat(ctx, tenant, service.DefaultBucket, key)
	}
	return h.svc.StatVersionWithOptions(ctx, tenant, service.DefaultBucket, key, version, service.ReadOptions{})
}

func thumbnailSourceIdentity(obj repository.Object) thumbnail.SourceIdentity {
	return thumbnail.SourceIdentity{
		TenantID:  obj.TenantID,
		Bucket:    obj.Bucket,
		Key:       obj.Key,
		VersionID: obj.VersionID,
	}
}

func openedThumbnailValidator(source *thumbnail.OpenedSource, effW, effH int) string {
	if source == nil || !source.Bound {
		return ""
	}
	return thumbValidatorETag(thumbnail.CacheKeyVersion, source.Identity, source.ETag, effW, effH)
}

func addThumbnailVary(w http.ResponseWriter) {
	want := []string{"Authorization", "X-Aero-Tenant", "X-Api-Key"}
	seen := make(map[string]bool)
	var values []string
	for _, raw := range w.Header().Values("Vary") {
		for _, token := range strings.Split(raw, ",") {
			token = strings.TrimSpace(token)
			if token != "" && !seen[strings.ToLower(token)] {
				seen[strings.ToLower(token)] = true
				values = append(values, token)
			}
		}
	}
	for _, token := range want {
		if !seen[strings.ToLower(token)] {
			seen[strings.ToLower(token)] = true
			values = append(values, token)
		}
	}
	w.Header().Set("Vary", strings.Join(values, ", "))
}

// thumbnailCacheAdmissible reports whether the source object may seed the
// server-side thumbnail cache — the handler's admission gate. An object is
// admissible only when its ETag is provably a whole-object content MD5
// (exactly 32 lowercase hex: the shape local storage emits for single-PUT
// objects, and the shape S3/OSS/COS echo for plain uploads) AND it is
// neither SSE-C nor SSE-KMS. SSE-C-derived bytes must never persist in
// server memory beyond the request; AWS documents SSE-KMS ETags as non-MD5
// (they may even be 32-hex-shaped), so only the metadata gate — not the
// shape test — keeps that class out. AES256 (SSE-S3 / local envelope) is
// not bypassed: its ETag remains the content MD5. Pinned and unpinned
// requests are identical here: both arms pass the statPinned row, so the
// gate evaluates the pinned version's metadata and ETag for a pinned read.
func thumbnailCacheAdmissible(obj repository.Object) bool {
	return thumbnailCacheBypassReason(obj) == ""
}

func thumbnailCacheBypassReason(obj repository.Object) string {
	if _, _, ssec := service.SSECustomerInfo(obj.Metadata); ssec {
		return "sse-c"
	}
	if algo, _, ok := service.ServerSideEncryptionInfo(obj.Metadata); ok && algo == "aws:kms" {
		return "sse-kms"
	}
	if !thumbnail.ContentMD5ETag(obj.ETag) {
		return "non-content-md5"
	}
	return ""
}

func recordThumbnailCacheBypass(ctx context.Context, cache *thumbnail.Cache, reason string) {
	if cache != nil && cache.Enabled() && (reason == "sse-c" || reason == "sse-kms" || reason == "storage-generation") {
		telemetry.IncThumbnailCacheBypass(ctx, reason)
	}
}

// bucketVersioning reports whether the default bucket has versioning enabled.
// It gates unpinned X-Version-Id emission on the thumbnail path (S3
// writeCurrentVersionHeader parity): the repository's internal version_id is
// non-empty on every object row — also on unversioned PUTs — so only the
// bucket flag, never the row's VersionID, may decide unpinned emission.
// Errors fail closed (no header), mirroring the S3 adapter.
func (h *Handler) bucketVersioning(ctx context.Context, tenant string) bool {
	cfg, err := h.svc.GetBucketConfig(ctx, tenant, service.DefaultBucket)
	return err == nil && cfg.Versioning
}
