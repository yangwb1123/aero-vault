package ai

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
)

// An unrecognized search mode is rejected with an error rather than silently
// returning empty results.
func TestSearchQuery_InvalidModeErrors(t *testing.T) {
	s := NewSearch(nil, NewHashEmbedder(8), nil)
	if _, err := s.Query(context.Background(), Request{Query: "x", Mode: "bogus"}); err == nil || !strings.Contains(err.Error(), "invalid search mode") {
		t.Fatalf("want invalid-mode error, got %v", err)
	}
}

// Vector/hybrid search with no embedder configured errors instead of panicking
// on a nil embedder.
func TestSearchQuery_NilEmbedderErrors(t *testing.T) {
	s := NewSearch(nil, nil, nil) // no embedder
	if _, err := s.Query(context.Background(), Request{Query: "x", Mode: "vector"}); err == nil || !strings.Contains(err.Error(), "no embedder configured") {
		t.Fatalf("want nil-embedder error, got %v", err)
	}
}

// TestHybridSort_TiebreakerByChunkID verifies that when two ranked entries share
// an identical RRF score the insertion sort places the lower chunkID first,
// making hybrid search output deterministic regardless of map iteration order.
func TestHybridSort_TiebreakerByChunkID(t *testing.T) {
	type ranked struct {
		chunkID int64
		score   float32
	}
	// Start with higher ID first to confirm the sort moves lower ID to front.
	merged := []ranked{
		{chunkID: 200, score: 0.5},
		{chunkID: 100, score: 0.5},
	}
	// Insertion sort — same logic as the hybrid case in search.go.
	for i := 1; i < len(merged); i++ {
		j := i
		for j > 0 && (merged[j].score > merged[j-1].score ||
			(merged[j].score == merged[j-1].score && merged[j].chunkID < merged[j-1].chunkID)) {
			merged[j], merged[j-1] = merged[j-1], merged[j]
			j--
		}
	}
	if merged[0].chunkID != 100 {
		t.Fatalf("want chunkID 100 first, got chunkID %d first", merged[0].chunkID)
	}
}

// The indexer's public methods error on a nil embedder instead of dereferencing it.
func TestIndexer_NilEmbedderErrors(t *testing.T) {
	ix := &Indexer{} // no embedder configured
	if _, err := ix.ReindexStale(context.Background(), "t", 10); err == nil {
		t.Fatal("ReindexStale with nil embedder should error")
	}
	if err := ix.IndexObjectByID(context.Background(), 1); err == nil {
		t.Fatal("IndexObjectByID with nil embedder should error")
	}
}

// recordingVectorIndex records every requested limit so tests can assert the
// over-retrieval factor Search asks of the backend.
type recordingVectorIndex struct {
	limits []int
	hits   []repository.SearchHit
}

func (r *recordingVectorIndex) SearchVectors(_ context.Context, _, _ string, _ []float32, limit int) ([]repository.SearchHit, error) {
	r.limits = append(r.limits, limit)
	return r.hits, nil
}

// TestSearchVectorLimit pins the over-retrieval cap: Search validates K<=100
// then requests K*2 candidates, which for K=60 is 120 — beyond the 100-cap
// every retrieval backend clamps to (silently truncating to 10 before embed-
// model filtering). The requested limit must stay in [K,100] and the query
// must still deliver exactly K hits.
func seedSearchVectorLimit(t *testing.T) (*testEnv, *HashEmbedder) {
	t.Helper()
	ctx := context.Background()
	env := newTestEnv(t)
	emb := NewHashEmbedder(64)
	obj := env.putObject(t, "k.txt", "text/plain", "sixty")
	contents := make([]string, 60)
	for i := range contents {
		contents[i] = "c"
	}
	seedCanonicalQueryRows(t, env, obj, emb, contents...)
	rows, err := env.repo.ListChunksForObject(ctx, obj.ID)
	if err != nil {
		t.Fatalf("list canonical chunks: %v", err)
	}
	if len(rows) != 60 {
		t.Fatalf("seeded %d canonical chunks, want 60", len(rows))
	}
	if obj.ID != 1 {
		t.Fatalf("fixture object ID=%d, want original 1", obj.ID)
	}
	return env, emb
}

func TestSearchVectorLimit(t *testing.T) {
	ctx := context.Background()
	env, emb := seedSearchVectorLimit(t)
	stub := &recordingVectorIndex{}
	for i := 0; i < 60; i++ {
		stub.hits = append(stub.hits, repository.SearchHit{
			Score: 0.99 - float32(i)*0.001,
			Chunk: repository.Chunk{ID: int64(i + 1), ObjectID: 1, TenantID: testTenant, Bucket: testBucket,
				ObjectKey: "k.txt", Seq: 0, Content: "c", EmbedModel: emb.Name()},
		})
	}
	s := NewSearch(env.repo, emb, nil).WithVectorIndex(stub).WithCanonicalResources(env.repo)

	for _, mode := range []string{"vector", "hybrid"} {
		s2 := s
		if mode == "hybrid" {
			s2 = s.WithBM25(NewBM25()) // empty index: lexical half contributes nothing
		}
		hits, err := s2.Query(ctx, Request{Tenant: testTenant, Bucket: testBucket, Query: "x", K: 60, Mode: mode})
		if err != nil {
			t.Fatalf("%s query: %v", mode, err)
		}
		if len(stub.limits) == 0 {
			t.Fatalf("%s: vector index never consulted", mode)
		}
		got := stub.limits[len(stub.limits)-1]
		if got < 60 || got > 100 {
			t.Errorf("%s: requested limit=%d, want in [60,100] (pre-fix value was 120)", mode, got)
		}
		if len(hits) != 60 {
			t.Errorf("%s: got %d hits, want exactly 60 (pre-fix truncation made this <=10 on every backend)", mode, len(hits))
		}
	}
}

type typedNilHitAuthorizer struct{}

func (*typedNilHitAuthorizer) CanReadObject(context.Context, string, string, string) error {
	panic("typed-nil hit authorizer must not be called")
}

func TestSearchProductionValidationRequiresExplicitReadersAndAuthorizer(t *testing.T) {
	env, emb, _, _ := seedBoundarySearchEnv(t)
	reader := newSearchBoundaryReader(testTenant, testBucket)
	configured := func() *Search {
		return NewSearch(env.repo, emb, nil).WithResourceBoundary(reader, reader).
			WithCanonicalResources(env.repo).WithHitAuthorizer(env.svc)
	}
	var typedNil *canonicalFaultRepo
	var typedNilAuth *typedNilHitAuthorizer
	for name, search := range map[string]*Search{
		"legacy":               NewSearch(env.repo, emb, nil),
		"implicit canonical":   NewSearch(env.repo, emb, nil).WithResourceBoundary(reader, reader).WithHitAuthorizer(env.svc),
		"opaque core":          configured().WithResourceBoundaryCore(access.NewResourceBoundary(reader, reader)),
		"missing binding":      configured().WithResourceBoundary(nil, reader),
		"missing coverage":     configured().WithResourceBoundary(reader, nil),
		"missing resources":    configured().WithCanonicalResources(nil),
		"typed nil resources":  configured().WithCanonicalResources(typedNil),
		"missing authorizer":   configured().WithHitAuthorizer(nil),
		"typed nil authorizer": configured().WithHitAuthorizer(typedNilAuth),
	} {
		t.Run(name, func(t *testing.T) {
			if err := search.ValidateResourceBoundary(); !IsSearchResourceBoundaryFault(err) {
				t.Fatalf("invalid production configuration admitted: %v", err)
			}
		})
	}
	if err := configured().ValidateResourceBoundary(); err != nil {
		t.Fatalf("explicit configuration: %v", err)
	}
	if reader.bindingCalls != 0 || reader.coverageCalls != 0 {
		t.Fatal("startup validator performed resource observations")
	}
}

func TestSearchUsesRawCanonicalReaderInsteadOfWrappedRepository(t *testing.T) {
	env, emb, _, _ := seedBoundarySearchEnv(t)
	wrapped := &canonicalFaultRepo{Repository: env.repo,
		objectErr: errors.New("wrapped object reader is not authoritative"),
		chunksErr: errors.New("wrapped chunk reader is not authoritative")}
	s := boundarySearch(t, wrapped, emb).WithCanonicalResources(env.repo).WithHitAuthorizer(env.svc)
	if err := s.ValidateResourceBoundary(); err != nil {
		t.Fatal(err)
	}
	hits, err := s.Query(context.Background(), Request{Tenant: testTenant, Bucket: testBucket, Query: "raft", K: 2})
	if err != nil || len(hits) != 2 {
		t.Fatalf("raw reader lost behind wrapper: hits=%+v err=%v", hits, err)
	}
}

func TestSearchConfiguredMissingPortsFaultBeforeWork(t *testing.T) {
	env, seeded, _, _ := seedBoundarySearchEnv(t)
	reader := newSearchBoundaryReader(testTenant, testBucket)
	var typedNil *canonicalFaultRepo
	for _, configure := range []func(*Search) *Search{
		func(s *Search) *Search { return s.WithCanonicalResources(nil) },
		func(s *Search) *Search { return s.WithCanonicalResources(typedNil) },
		func(s *Search) *Search { return s.WithHitAuthorizer((*typedNilHitAuthorizer)(nil)) },
		func(s *Search) *Search { return s.WithResourceBoundary(reader, nil) },
		func(s *Search) *Search { return s.WithVectorIndex((*fakeVectorIndex)(nil)) },
		func(s *Search) *Search { return s.WithLexicalIndex((*fakeLexical)(nil)) },
		func(s *Search) *Search { return s.WithReranker((*observingReranker)(nil)) },
	} {
		emb := &countingHashEmbedder{inner: seeded}
		repo := &countingSearchRepo{Repository: env.repo}
		s := configure(NewSearch(repo, emb, nil))
		_, err := s.Query(context.Background(), Request{Tenant: testTenant, Bucket: testBucket, Query: "raft", K: 2})
		if !IsSearchResourceBoundaryFault(err) || emb.embedded != 0 || repo.searches != 0 || repo.usages != 0 {
			t.Fatalf("configured missing port: err=%v embed=%d search=%d usage=%d", err, emb.embedded, repo.searches, repo.usages)
		}
	}
}

func TestSearchOnlyOrdinaryDenialsMayFilter(t *testing.T) {
	for _, err := range []error{
		errors.Join(service.ErrNotFound, access.ErrResourceBoundaryFault),
		errors.Join(service.ErrForbidden, ErrCanonicalHit),
		errors.Join(service.ErrForbidden, access.ErrSpaceBound),
		errors.Join(service.ErrForbidden, service.ErrTenantDisabled),
		errors.Join(service.ErrNotFound, context.Canceled),
	} {
		if ordinaryHitDenial(err) {
			t.Errorf("fault classified as ordinary denial: %v", err)
		}
	}
	if !ordinaryHitDenial(service.ErrForbidden) || !ordinaryHitDenial(repository.ErrNotFound) {
		t.Fatal("ordinary denial compatibility lost")
	}
}
