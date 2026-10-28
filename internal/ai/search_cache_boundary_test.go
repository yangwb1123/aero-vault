package ai

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
)

type mutableHitAuthorizer struct {
	allow map[string]bool
	err   error
}

func (a *mutableHitAuthorizer) CanReadObject(_ context.Context, _, _, key string) error {
	if a.err != nil {
		return a.err
	}
	if a.allow[key] {
		return nil
	}
	return fmt.Errorf("%w: revoked", service.ErrForbidden)
}

func TestSearchCacheRevalidatesBoundaryCanonicalAndAuthorization(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	emb := &countingHashEmbedder{inner: NewHashEmbedder(128)}
	allowed := env.putObject(t, "allowed.txt", "text/plain", "allowed")
	revoked := env.putObject(t, "revoked.txt", "text/plain", "revoked")
	env.seedChunks(t, allowed, emb, "raft consensus protocol")
	env.seedChunks(t, revoked, emb, "raft consensus protocol")
	emb.embedded = 0
	allowedChunks, err := env.repo.ListChunksForObject(ctx, allowed.ID)
	if err != nil {
		t.Fatalf("allowed chunks: %v", err)
	}
	revokedChunks, err := env.repo.ListChunksForObject(ctx, revoked.ID)
	if err != nil {
		t.Fatalf("revoked chunks: %v", err)
	}
	auth := &mutableHitAuthorizer{allow: map[string]bool{"allowed.txt": true, "revoked.txt": true}}
	reader := newSearchBoundaryReader(testTenant, testBucket)
	s := NewSearch(env.repo, emb, nil).
		WithResourceBoundary(reader, reader).
		WithHitAuthorizer(auth).
		WithResultCache(16, time.Minute)
	req := Request{Tenant: testTenant, Bucket: testBucket, Query: "raft consensus", K: 5, Mode: "vector"}

	assertSearchCacheHealthy(t, ctx, s, req, emb)
	auth.allow["revoked.txt"] = false
	assertSearchCacheRevoked(t, ctx, s, req, allowed)
	assertSearchCacheRevokedUsage(t, ctx, env, allowed, revoked, revokedChunks)
	assertSearchCacheBoundAndStale(t, ctx, env, s, req, reader, allowed, revoked)
	_ = allowedChunks
}

func assertSearchCacheHealthy(t *testing.T, ctx context.Context, s *Search, req Request, emb *countingHashEmbedder) {
	t.Helper()
	first, err := s.Query(ctx, req)
	if err != nil {
		t.Fatalf("first query: %v", err)
	}
	second, err := s.Query(ctx, req)
	if err != nil {
		t.Fatalf("cached query: %v", err)
	}
	if len(first) != 2 || len(second) != 2 {
		t.Fatalf("healthy hits = %d/%d, want 2/2", len(first), len(second))
	}
	if emb.embedded != 1 {
		t.Fatalf("healthy cache embedded %d times, want 1", emb.embedded)
	}
}

func assertSearchCacheRevoked(t *testing.T, ctx context.Context, s *Search, req Request, allowed repository.Object) {
	t.Helper()
	hits, err := s.Query(ctx, req)
	if err != nil {
		t.Fatalf("revoked cache query: %v", err)
	}
	if len(hits) != 1 || hits[0].ObjectID != allowed.ID {
		t.Fatalf("revoked cached hit leaked: %+v", hits)
	}
}

func assertSearchCacheRevokedUsage(t *testing.T, ctx context.Context, env *testEnv, allowed, revoked repository.Object, revokedChunks []repository.Chunk) {
	t.Helper()
	allowedUsage, err := env.repo.ListUsageForObject(ctx, testTenant, allowed.ID, 10)
	if err != nil {
		t.Fatalf("allowed usage: %v", err)
	}
	revokedUsage, err := env.repo.ListUsageForObject(ctx, testTenant, revoked.ID, 10)
	if err != nil {
		t.Fatalf("revoked usage: %v", err)
	}
	if len(allowedUsage) != 3 || len(revokedUsage) != 2 {
		t.Fatalf("usage allowed=%d revoked=%d, rejected hit must not create successful usage",
			len(allowedUsage), len(revokedUsage))
	}
	for _, usage := range revokedUsage {
		if len(usage.ChunkIDs) == 1 && usage.ChunkIDs[0] == revokedChunks[0].ID {
			// The two historical successful uses are expected; a third would fail above.
			continue
		}
	}
}

func assertSearchCacheBoundAndStale(t *testing.T, ctx context.Context, env *testEnv, s *Search, req Request, reader *searchBoundaryReader, allowed, revoked repository.Object) {
	t.Helper()
	reader.setBound()
	if _, err := s.Query(ctx, req); !IsSearchResourceBoundaryDenied(err) {
		t.Fatalf("bound cache query = %v, want explicit denial", err)
	}
	reader.setUnbound()

	if err := env.repo.SoftDeleteObject(ctx, testTenant, testBucket, revoked.Key); err != nil {
		t.Fatalf("soft delete revoked object: %v", err)
	}
	if _, err := s.Query(ctx, req); !errors.Is(err, ErrCanonicalHit) {
		t.Fatalf("stale cache query = %v, want canonical rejection", err)
	}
	allowedUsage, err := env.repo.ListUsageForObject(ctx, testTenant, allowed.ID, 10)
	if err != nil {
		t.Fatalf("allowed usage after stale: %v", err)
	}
	if len(allowedUsage) != 3 {
		t.Fatalf("stale cache created successful usage: %d", len(allowedUsage))
	}
}

func TestSearchCacheReaderFaultDoesNotDeliver(t *testing.T) {
	ctx := context.Background()
	env, emb, _, _ := seedBoundarySearchEnv(t)
	reader := newSearchBoundaryReader(testTenant, testBucket)
	s := NewSearch(env.repo, emb, nil).
		WithResourceBoundary(reader, reader).
		WithResultCache(8, time.Minute)
	req := Request{Tenant: testTenant, Bucket: testBucket, Query: "raft", K: 2, Mode: "vector"}
	if _, err := s.Query(ctx, req); err != nil {
		t.Fatalf("warm query: %v", err)
	}
	reader.bindingErr = errors.New("provider down")
	if _, err := s.Query(ctx, req); !IsSearchResourceBoundaryFault(err) {
		t.Fatalf("cache reader fault = %v, want fault", err)
	}
}

var _ = repository.ErrNotFound

type subjectHitAuthorizer struct{}

func (subjectHitAuthorizer) CanReadObject(ctx context.Context, _, _, _ string) error {
	principal, _ := access.PrincipalFrom(ctx)
	if principal.SubjectID == "reader" {
		return nil
	}
	return service.ErrForbidden
}

func TestSearchCacheStoresCandidatesNotSubjectDecisions(t *testing.T) {
	env, seeded, obj, _ := seedBoundarySearchEnv(t)
	emb := &countingHashEmbedder{inner: seeded}
	s := boundarySearch(t, env.repo, emb).WithHitAuthorizer(subjectHitAuthorizer{}).WithResultCache(8, time.Minute)
	req := Request{Tenant: testTenant, Bucket: testBucket, Query: "raft", K: 2}
	deniedCtx := access.WithPrincipal(context.Background(), access.Principal{SubjectID: "denied"})
	readerCtx := access.WithPrincipal(context.Background(), access.Principal{SubjectID: "reader"})
	hits, err := s.Query(deniedCtx, req)
	if err != nil || len(hits) != 0 {
		t.Fatalf("denied subject hits=%+v err=%v", hits, err)
	}
	hits, err = s.Query(readerCtx, req)
	if err != nil || len(hits) != 2 {
		t.Fatalf("subject-independent cache retained denial: hits=%+v err=%v", hits, err)
	}
	if emb.embedded != 1 {
		t.Fatalf("healthy candidate cache embedded %d times, want 1", emb.embedded)
	}
	usage, err := env.repo.ListUsageForObject(context.Background(), testTenant, obj.ID, 10)
	if err != nil || len(usage) != 1 {
		t.Fatalf("candidate usage=%+v err=%v, want only reader's use", usage, err)
	}
}

func TestSearchCacheReranksOnlyCurrentlyAdmittedCandidates(t *testing.T) {
	env, seeded, first, firstRows := seedBoundarySearchEnv(t)
	second := env.putObject(t, "second.txt", "text/plain", "second")
	env.seedChunks(t, second, seeded, "second raft chunk")
	secondRows, err := env.repo.ListChunksForObject(context.Background(), second.ID)
	if err != nil {
		t.Fatal(err)
	}
	emb := &countingHashEmbedder{inner: seeded}
	auth := &mutableHitAuthorizer{allow: map[string]bool{first.Key: true}}
	expected := first.ID
	reranker := &observingReranker{run: func(_ context.Context, hits []Hit) ([]Hit, error) {
		if len(hits) != 1 || hits[0].ObjectID != expected {
			t.Fatalf("cached authorization/rank decision leaked into input: %+v", hits)
		}
		return hits, nil
	}}
	s := boundarySearch(t, env.repo, emb).WithHitAuthorizer(auth).WithResultCache(8, time.Minute).
		WithVectorIndex(&fakeVectorIndex{hits: []repository.SearchHit{chunkHit(first, firstRows[0]), chunkHit(second, secondRows[0])}}).
		WithReranker(reranker)
	req := Request{Tenant: testTenant, Bucket: testBucket, Query: "raft", K: 1}
	for _, obj := range []repository.Object{first, second} {
		expected = obj.ID
		auth.allow = map[string]bool{obj.Key: true}
		hits, err := s.Query(context.Background(), req)
		if err != nil || len(hits) != 1 || hits[0].ObjectID != obj.ID {
			t.Fatalf("current subject/corpus hits=%+v err=%v", hits, err)
		}
	}
	if emb.embedded != 1 || reranker.calls != 2 {
		t.Fatalf("candidate cache embed=%d rerank=%d, want 1/2", emb.embedded, reranker.calls)
	}
}
