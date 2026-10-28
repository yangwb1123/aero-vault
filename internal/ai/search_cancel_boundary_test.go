package ai

import (
	"context"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
)

func TestSearchPreCancelColdAndWarmDoesNoWork(t *testing.T) {
	env, seeded, _, _ := seedBoundarySearchEnv(t)
	repo := &countingSearchRepo{Repository: env.repo}
	emb := &countingHashEmbedder{inner: seeded}
	reader := newSearchBoundaryReader(testTenant, testBucket)
	reranker := &countingSearchReranker{}
	s := NewSearch(repo, emb, nil).
		WithResourceBoundary(reader, reader).
		WithResultCache(16, time.Minute).
		WithReranker(reranker)
	req := Request{Tenant: testTenant, Bucket: testBucket, Query: "raft consensus", K: 5, Mode: "vector"}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Query(ctx, req); err != ctx.Err() {
		t.Fatalf("cold pre-cancel error = %v, want identical %v", err, ctx.Err())
	}
	assertSearchPreCancelColdNoWork(t, reader, emb, repo, reranker)

	if _, err := s.Query(context.Background(), req); err != nil {
		t.Fatalf("warm-up query: %v", err)
	}
	reader.bindingCalls, reader.coverageCalls = 0, 0
	emb.embedded, repo.searches, repo.usages, reranker.calls = 0, 0, 0, 0
	repo.objectReads, repo.chunkReads = 0, 0
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	if _, err := s.Query(ctx2, req); err != ctx2.Err() {
		t.Fatalf("warm pre-cancel error = %v, want identical %v", err, ctx2.Err())
	}
	assertSearchPreCancelWarmNoWork(t, reader, emb, repo, reranker)
}

type cancelAfterVector struct {
	cancel context.CancelFunc
	hits   []repository.SearchHit
}

func (v *cancelAfterVector) SearchVectors(context.Context, string, string, []float32, int) ([]repository.SearchHit, error) {
	v.cancel()
	return v.hits, nil
}

func TestSearchCancellationStopsNextRetrievalCall(t *testing.T) {
	env, emb, obj, rows := seedBoundarySearchEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lexical := &fakeLexical{hits: []repository.SearchHit{chunkHit(obj, rows[0])}}
	s := boundarySearch(t, env.repo, emb).
		WithVectorIndex(&cancelAfterVector{cancel: cancel, hits: []repository.SearchHit{chunkHit(obj, rows[0])}}).
		WithLexicalIndex(lexical)
	if _, err := s.Query(ctx, Request{Tenant: testTenant, Bucket: testBucket, Query: "raft", K: 2, Mode: "hybrid"}); err != ctx.Err() {
		t.Fatalf("cancelled retrieval error=%v, want naked %v", err, ctx.Err())
	}
	if lexical.called {
		t.Fatal("started another external retrieval after cancellation")
	}
}

func assertSearchPreCancelColdNoWork(t *testing.T, reader *searchBoundaryReader, emb *countingHashEmbedder, repo *countingSearchRepo, reranker *countingSearchReranker) {
	t.Helper()
	if reader.bindingCalls != 0 || reader.coverageCalls != 0 || emb.embedded != 0 ||
		repo.searches != 0 || repo.usages != 0 || reranker.calls != 0 {
		t.Fatalf("cold pre-cancel did work: binding=%d coverage=%d embed=%d search=%d usage=%d rerank=%d",
			reader.bindingCalls, reader.coverageCalls, emb.embedded, repo.searches, repo.usages, reranker.calls)
	}
	if repo.objectReads != 0 || repo.chunkReads != 0 {
		t.Fatalf("cold pre-cancel canonical work: objects=%d chunks=%d", repo.objectReads, repo.chunkReads)
	}
}

func assertSearchPreCancelWarmNoWork(t *testing.T, reader *searchBoundaryReader, emb *countingHashEmbedder, repo *countingSearchRepo, reranker *countingSearchReranker) {
	t.Helper()
	if reader.bindingCalls != 0 || reader.coverageCalls != 0 || emb.embedded != 0 ||
		repo.searches != 0 || repo.usages != 0 || reranker.calls != 0 {
		t.Fatalf("warm pre-cancel did work: binding=%d coverage=%d embed=%d search=%d usage=%d rerank=%d",
			reader.bindingCalls, reader.coverageCalls, emb.embedded, repo.searches, repo.usages, reranker.calls)
	}
	if repo.objectReads != 0 || repo.chunkReads != 0 {
		t.Fatalf("warm pre-cancel canonical work: objects=%d chunks=%d", repo.objectReads, repo.chunkReads)
	}
}
