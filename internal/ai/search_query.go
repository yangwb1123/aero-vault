package ai

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/spaces"
	"github.com/aero-vault/aero-vault/internal/telemetry"
)

// Query preserves the public search signature. A pre-cancelled context returns
// that exact cancellation error before argument validation, boundary
// observation, cache lookup, retrieval, canonicalization, reranking or usage.
func (s *Search) Query(ctx context.Context, req Request) ([]Hit, error) {
	if ctx == nil {
		return nil, fmt.Errorf("search: nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := req.validate(s); err != nil {
		return nil, err
	}
	if err := s.boundaryP0(ctx, req.Tenant, req.Bucket); err != nil {
		return nil, err
	}
	if s.results != nil {
		key := resultCacheKey(req)
		if cached, ok := s.results.get(key); ok {
			hits, err := s.deliverCached(ctx, req, cached)
			if err != nil {
				return nil, err
			}
			s.recordUsage(ctx, req, hits)
			return hits, nil
		}
	}
	return s.queryFresh(ctx, req)
}

func (s *Search) queryFresh(ctx context.Context, req Request) ([]Hit, error) {
	start := time.Now()
	merged, err := s.searchAndMerge(ctx, req, req.Mode)
	if err != nil {
		return nil, err
	}
	if err := validateRankedScope(req, merged); err != nil {
		return nil, err
	}
	out := hitsFromRanked(merged)
	guarded, err := s.deliverFresh(ctx, req, out)
	if err != nil {
		return nil, err
	}
	s.recordUsage(ctx, req, guarded)
	telemetry.RecordSearchLatency(ctx, req.Mode, float64(time.Since(start).Milliseconds()))
	if s.results != nil {
		s.results.put(resultCacheKey(req), out)
	}
	return guarded, nil
}

func (s *Search) deliverFresh(ctx context.Context, req Request, hits []Hit) ([]Hit, error) {
	hits, _, err := s.admitHits(ctx, req, hits)
	if err != nil {
		return nil, err
	}
	hits, err = s.rerankOrTrim(ctx, req, hits)
	if err != nil {
		return nil, err
	}
	if err := s.boundaryP1(ctx, req.Tenant, req.Bucket); err != nil {
		return nil, err
	}
	return hits, nil
}

func (s *Search) deliverCached(ctx context.Context, req Request, hits []Hit) ([]Hit, error) {
	return s.deliverFresh(ctx, req, hits)
}

// revalidateHits is the fresh guard used by Chat and Agent on already-issued
// hits. It reports whether an ordinary ACL denial removed any hit so callers
// that already materialized prompt/history content can fail closed instead of
// silently continuing with a different resource set.
func (s *Search) revalidateHits(ctx context.Context, req Request, hits []Hit) ([]Hit, bool, error) {
	if ctx == nil {
		return nil, false, fmt.Errorf("search: nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if err := s.boundaryP0(ctx, req.Tenant, req.Bucket); err != nil {
		return nil, false, err
	}
	filtered, denied, err := s.admitHits(ctx, req, hits)
	if err != nil {
		return nil, false, err
	}
	if err := s.boundaryP1(ctx, req.Tenant, req.Bucket); err != nil {
		return nil, false, err
	}
	return filtered, denied, nil
}

func (s *Search) rerankOrTrim(ctx context.Context, req Request, hits []Hit) ([]Hit, error) {
	if s.rerank == nil || len(hits) == 0 {
		return trimHits(hits, req.K), nil
	}
	if err := s.boundaryP1(ctx, req.Tenant, req.Bucket); err != nil {
		return nil, err
	}
	// Re-observe after P1 too: the boundary/ACL providers may change rows.
	hits, _, err := s.admitHits(ctx, req, hits)
	if err != nil {
		return nil, err
	}
	if len(hits) == 0 {
		return hits, nil
	}
	// Snapshot only the actual admitted input, never pre-authorization rows.
	snapshot, err := s.canonicalizeHits(ctx, req, hits)
	if err != nil {
		return nil, err
	}
	reranked, err := s.rankAdmitted(ctx, req, snapshot)
	if err != nil {
		return nil, err
	}
	// Recheck ALL content sent to the reranker, also on an ordinary failure.
	admitted, _, err := s.admitHits(ctx, req, hits)
	if err != nil {
		return nil, err
	}
	return trimHits(retainAdmittedHits(reranked, admitted), req.K), nil
}

func (s *Search) rankAdmitted(ctx context.Context, req Request, snapshot *canonicalResult) ([]Hit, error) {
	reranked, err := s.rerank.Rerank(ctx, req.Query, cloneHits(snapshot.hits), req.K)
	if fatal := rerankerFailure(ctx, err); fatal != nil {
		return nil, fatal
	}
	if err != nil {
		s.logger.Warn("rerank failed; using raw order", "err", err)
		return snapshot.hits, nil
	}
	return guardReranked(snapshot, reranked, req.Tenant)
}

func retainAdmittedHits(reranked, admitted []Hit) []Hit {
	allowed := make(map[int64]bool, len(admitted))
	for _, hit := range admitted {
		allowed[hit.ChunkID] = true
	}
	out := make([]Hit, 0, len(reranked))
	for _, hit := range reranked {
		if allowed[hit.ChunkID] {
			out = append(out, hit)
		}
	}
	return out
}

func trimHits(hits []Hit, k int) []Hit {
	if k > 0 && len(hits) > k {
		return hits[:k]
	}
	return hits
}

// filterAuthorizedHits drops explicit ordinary denials but propagates boundary
// and provider faults. A fault must never become an empty successful page.
func (s *Search) filterAuthorizedHits(ctx context.Context, req Request, hits []Hit) ([]Hit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.hitAuth == nil {
		return hits, nil
	}
	out := make([]Hit, 0, len(hits))
	for _, hit := range hits {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		err := s.hitAuth.CanReadObject(ctx, req.Tenant, hit.Bucket, hit.ObjectKey)
		if ctx.Err() != nil {
			return nil, errors.Join(err, ctx.Err())
		}
		if err == nil {
			out = append(out, hit)
			continue
		}
		if !ordinaryHitDenial(err) {
			return nil, err
		}
	}
	return out, nil
}

func ordinaryHitDenial(err error) bool {
	if securityResourceError(err) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, access.ErrDenied) {
		return true
	}
	return errors.Is(err, service.ErrForbidden) ||
		errors.Is(err, service.ErrNotFound) ||
		errors.Is(err, repository.ErrNotFound)
}

func securityResourceError(err error) bool {
	return access.IsResourceBoundaryFault(err) || errors.Is(err, ErrCanonicalHit) ||
		errors.Is(err, access.ErrSpaceBound) || errors.Is(err, spaces.ErrTenantUnavailable) ||
		errors.Is(err, service.ErrTenantDisabled)
}

func rerankerFailure(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return errors.Join(err, ctx.Err())
	}
	if securityResourceError(err) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return nil
}

func (s *Search) admitHits(ctx context.Context, req Request, hits []Hit) ([]Hit, bool, error) {
	if !s.canonical {
		for _, hit := range hits {
			if err := validateHitShape(req, hit); err != nil {
				return nil, false, err
			}
		}
		return cloneHits(hits), false, ctx.Err()
	}
	snapshot, err := s.canonicalizeHits(ctx, req, hits)
	if err != nil {
		return nil, false, err
	}
	filtered, err := s.filterAuthorizedHits(ctx, req, snapshot.hits)
	if err != nil {
		return nil, false, err
	}
	if s.hitAuth != nil {
		// The authorizer is an external capability and may change canonical rows.
		if _, err := s.canonicalizeHits(ctx, req, filtered); err != nil {
			return nil, false, err
		}
	}
	return filtered, len(filtered) != len(hits), nil
}

func (s *Search) validateRetrieved(ctx context.Context, req Request, lists ...[]ranked) error {
	for _, hits := range lists {
		if err := validateRankedScope(req, hits); err != nil {
			return err
		}
		if s.canonical {
			if _, err := s.canonicalizeHits(ctx, req, hitsFromRanked(hits)); err != nil {
				return err
			}
		}
	}
	return nil
}

func retrievalSecurityError(errs ...error) error {
	for _, err := range errs {
		if securityResourceError(err) {
			return err
		}
	}
	return nil
}

// Preserve the failing modality's phase/cause; only the both-success decision
// window returns the naked cancellation error (the original F4/F11 contract).
func cancelledRetrieval(ctx context.Context, vecErr, lexErr error) error {
	if vecErr != nil {
		return vecErr
	}
	if lexErr != nil {
		return lexErr
	}
	return ctx.Err()
}
