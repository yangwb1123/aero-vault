package ai

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
)

type canonicalFaultRepo struct {
	repository.Repository
	objectErr error
	object    *repository.Object
	chunksErr error
	rows      []repository.Chunk
	override  bool
}

func (r *canonicalFaultRepo) GetObjectByID(ctx context.Context, id int64) (repository.Object, error) {
	if r.objectErr != nil {
		return repository.Object{}, r.objectErr
	}
	if r.object != nil {
		return *r.object, nil
	}
	return r.Repository.GetObjectByID(ctx, id)
}

func (r *canonicalFaultRepo) ListChunksForObject(ctx context.Context, id int64) ([]repository.Chunk, error) {
	if r.chunksErr != nil {
		return nil, r.chunksErr
	}
	if r.override {
		return r.rows, nil
	}
	return r.Repository.ListChunksForObject(ctx, id)
}

type canonicalReranker struct {
	transform func([]Hit) []Hit
	err       error
}

func (r *canonicalReranker) Name() string { return "canonical-test" }
func (r *canonicalReranker) Rerank(_ context.Context, _ string, hits []Hit, _ int) ([]Hit, error) {
	if r.err != nil {
		return nil, r.err
	}
	if r.transform != nil {
		return r.transform(hits), nil
	}
	return hits, nil
}

func chunkHit(obj repository.Object, chunk repository.Chunk) repository.SearchHit {
	return repository.SearchHit{Score: 0.9, Chunk: repository.Chunk{
		ID: chunk.ID, ObjectID: obj.ID, TenantID: obj.TenantID,
		Bucket: obj.Bucket, ObjectKey: obj.Key, Seq: chunk.Seq,
		Content: chunk.Content, EmbedModel: chunk.EmbedModel,
	}}
}

func boundarySearch(t *testing.T, repo repository.Repository, emb Embedder) *Search {
	t.Helper()
	reader := newSearchBoundaryReader(testTenant, testBucket)
	return NewSearch(repo, emb, nil).WithResourceBoundary(reader, reader)
}

func TestSearchCanonicalRejectsTamperedCandidates(t *testing.T) {
	ctx := context.Background()
	env, emb, obj, rows := seedBoundarySearchEnv(t)
	base := chunkHit(obj, rows[0])

	cases := map[string]func(*repository.SearchHit){
		"cross tenant": func(h *repository.SearchHit) { h.Chunk.TenantID = "other" },
		"zero chunk":   func(h *repository.SearchHit) { h.Chunk.ID = 0 },
		"zero object":  func(h *repository.SearchHit) { h.Chunk.ObjectID = 0 },
		"forged text":  func(h *repository.SearchHit) { h.Chunk.Content = "forged" },
		"wrong key":    func(h *repository.SearchHit) { h.Chunk.ObjectKey = "other.txt" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			hit := base
			mutate(&hit)
			fake := &fakeVectorIndex{hits: []repository.SearchHit{hit}}
			s := boundarySearch(t, env.repo, emb).WithVectorIndex(fake)
			if _, err := s.Query(ctx, Request{
				Tenant: testTenant, Bucket: testBucket, Query: "raft", K: 2, Mode: "vector",
			}); !errors.Is(err, ErrCanonicalHit) {
				t.Fatalf("tampered candidate = %v, want canonical rejection", err)
			}
		})
	}
}

func TestSearchCanonicalRejectsOldVersionAndDeletedObject(t *testing.T) {
	ctx := context.Background()
	env, emb, obj, rows := seedBoundarySearchEnv(t)
	oldHit := chunkHit(obj, rows[0])
	if _, err := env.repo.InsertObjectVersion(ctx, repository.Object{
		TenantID: obj.TenantID, Bucket: obj.Bucket, Key: obj.Key,
		Backend: "local", StorageKey: obj.StorageKey + "-v2", Size: 1, ETag: "v2",
	}); err != nil {
		t.Fatalf("insert new version: %v", err)
	}
	s := boundarySearch(t, env.repo, emb).WithVectorIndex(&fakeVectorIndex{hits: []repository.SearchHit{oldHit}})
	if _, err := s.Query(ctx, Request{
		Tenant: testTenant, Bucket: testBucket, Query: "raft", K: 2, Mode: "vector",
	}); !errors.Is(err, ErrCanonicalHit) {
		t.Fatalf("old version = %v, want canonical rejection", err)
	}

	deleted := env.putObject(t, "deleted.txt", "text/plain", "deleted")
	env.seedChunks(t, deleted, emb, "deleted canonical chunk")
	deletedRows, err := env.repo.ListChunksForObject(ctx, deleted.ID)
	if err != nil {
		t.Fatalf("deleted chunks: %v", err)
	}
	if err := env.repo.SoftDeleteObject(ctx, testTenant, testBucket, deleted.Key); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	deletedHit := chunkHit(deleted, deletedRows[0])
	s = boundarySearch(t, env.repo, emb).WithVectorIndex(&fakeVectorIndex{hits: []repository.SearchHit{deletedHit}})
	if _, err := s.Query(ctx, Request{
		Tenant: testTenant, Bucket: testBucket, Query: "deleted", K: 2, Mode: "vector",
	}); !errors.Is(err, ErrCanonicalHit) {
		t.Fatalf("soft-deleted object = %v, want canonical rejection", err)
	}
}

func TestSearchCanonicalPreservesLookupCausesAndCompleteChunks(t *testing.T) {
	ctx := context.Background()
	env, emb, obj, rows := seedBoundarySearchEnv(t)
	hit := chunkHit(obj, rows[0])
	objectSentinel := errors.New("object provider down")
	chunksSentinel := errors.New("chunk provider down")
	for name, repo := range map[string]*canonicalFaultRepo{
		"object": {Repository: env.repo, objectErr: objectSentinel},
		"chunks": {Repository: env.repo, chunksErr: chunksSentinel},
	} {
		t.Run(name, func(t *testing.T) {
			s := boundarySearch(t, repo, emb).WithVectorIndex(&fakeVectorIndex{hits: []repository.SearchHit{hit}})
			_, err := s.Query(ctx, Request{Tenant: testTenant, Bucket: testBucket, Query: "raft", K: 2, Mode: "vector"})
			if !errors.Is(err, ErrCanonicalHit) {
				t.Fatalf("lookup fault = %v, want canonical rejection", err)
			}
			if name == "object" && !errors.Is(err, objectSentinel) {
				t.Fatalf("object cause lost: %v", err)
			}
			if name == "chunks" && !errors.Is(err, chunksSentinel) {
				t.Fatalf("chunk cause lost: %v", err)
			}
		})
	}

	badRows := append([]repository.Chunk(nil), rows...)
	badRows[0].TenantID = "other"
	repo := &canonicalFaultRepo{Repository: env.repo, rows: badRows, override: true}
	s := boundarySearch(t, repo, emb).WithVectorIndex(&fakeVectorIndex{hits: []repository.SearchHit{hit}})
	if _, err := s.Query(ctx, Request{Tenant: testTenant, Bucket: testBucket, Query: "raft", K: 2, Mode: "vector"}); !errors.Is(err, ErrCanonicalHit) {
		t.Fatalf("incomplete canonical chunks = %v", err)
	}
}

func TestSearchCanonicalGuardsRerankerOutput(t *testing.T) {
	ctx := context.Background()
	env, emb, obj, rows := seedBoundarySearchEnv(t)
	base := chunkHit(obj, rows[0])
	injected := chunkHit(obj, rows[1])
	for name, reranker := range map[string]*canonicalReranker{
		"injected id": {transform: func(hits []Hit) []Hit {
			return append(hits, Hit{ChunkID: rows[1].ID, ObjectID: obj.ID, Bucket: obj.Bucket,
				ObjectKey: obj.Key, Seq: rows[1].Seq, Chunk: rows[1].Content, EmbedModel: rows[1].EmbedModel})
		}},
		"mutated text": {transform: func(hits []Hit) []Hit {
			out := cloneHits(hits)
			out[0].Chunk = "forged"
			return out
		}},
	} {
		t.Run(name, func(t *testing.T) {
			s := boundarySearch(t, env.repo, emb).
				WithVectorIndex(&fakeVectorIndex{hits: []repository.SearchHit{base}}).
				WithReranker(reranker)
			if _, err := s.Query(ctx, Request{Tenant: testTenant, Bucket: testBucket, Query: "raft", K: 2, Mode: "vector"}); !errors.Is(err, ErrCanonicalHit) {
				t.Fatalf("reranker guard = %v, want canonical rejection", err)
			}
		})
	}
	_ = injected
	s := boundarySearch(t, env.repo, emb).
		WithVectorIndex(&fakeVectorIndex{hits: []repository.SearchHit{base}}).
		WithReranker(&canonicalReranker{err: errors.New("reranker down")})
	hits, err := s.Query(ctx, Request{Tenant: testTenant, Bucket: testBucket, Query: "raft", K: 2, Mode: "vector"})
	if err != nil || len(hits) != 1 || hits[0].ChunkID != base.Chunk.ID {
		t.Fatalf("ordinary rerank failure must keep canonical order: hits=%+v err=%v", hits, err)
	}
}

type observingReranker struct {
	calls int
	run   func(context.Context, []Hit) ([]Hit, error)
}

func (r *observingReranker) Name() string { return "observing" }
func (r *observingReranker) Rerank(ctx context.Context, _ string, hits []Hit, _ int) ([]Hit, error) {
	r.calls++
	return r.run(ctx, hits)
}

func TestSearchCanonicalRequiresCurrentKeyIdentity(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		t.Run(map[bool]string{false: "historical version", true: "same key replacement"}[replacement], func(t *testing.T) {
			ctx := context.Background()
			env, emb, obj, rows := seedBoundarySearchEnv(t)
			if replacement {
				if err := env.repo.HardDeleteObject(ctx, obj.TenantID, obj.Bucket, obj.Key); err != nil {
					t.Fatal(err)
				}
				env.putObject(t, obj.Key, "text/plain", "replacement")
			} else if _, err := env.repo.InsertObjectVersion(ctx, repository.Object{
				TenantID: obj.TenantID, Bucket: obj.Bucket, Key: obj.Key,
				Backend: "local", StorageKey: obj.StorageKey + "-new", ETag: "new", Size: 3,
			}); err != nil {
				t.Fatal(err)
			}
			current, err := env.repo.GetObject(ctx, obj.TenantID, obj.Bucket, obj.Key)
			if err != nil || current.ID == obj.ID {
				t.Fatalf("replacement fixture: current=%+v err=%v", current, err)
			}
			// A historical/cached by-ID reader is not proof of the current key.
			repo := &canonicalFaultRepo{Repository: env.repo, object: &obj, rows: rows, override: true}
			s := boundarySearch(t, repo, emb).WithVectorIndex(&fakeVectorIndex{hits: []repository.SearchHit{chunkHit(obj, rows[0])}})
			if _, err := s.Query(ctx, Request{Tenant: testTenant, Bucket: testBucket, Query: "raft", K: 2}); !errors.Is(err, ErrCanonicalHit) {
				t.Fatalf("historical identity delivered: %v", err)
			}
		})
	}
}

func TestSearchRejectsOriginalScopeBeforeFusion(t *testing.T) {
	env, emb, obj, rows := seedBoundarySearchEnv(t)
	valid := chunkHit(obj, rows[0])
	foreign := valid
	foreign.Chunk.TenantID = "other"
	s := boundarySearch(t, env.repo, emb).
		WithVectorIndex(&fakeVectorIndex{hits: []repository.SearchHit{valid}}).
		WithLexicalIndex(&fakeLexical{hits: []repository.SearchHit{foreign}})
	if _, err := s.Query(context.Background(), Request{Tenant: testTenant, Bucket: testBucket, Query: "raft", K: 2, Mode: "hybrid"}); !errors.Is(err, ErrCanonicalHit) {
		t.Fatalf("fusion hid original cross-tenant row: %v", err)
	}
}

func TestSearchRerankerCannotReturnACLFilteredCandidate(t *testing.T) {
	env, emb, obj, rows := seedBoundarySearchEnv(t)
	denied := env.putObject(t, "denied.txt", "text/plain", "private")
	env.seedChunks(t, denied, emb, "private raft chunk")
	deniedRows, err := env.repo.ListChunksForObject(context.Background(), denied.ID)
	if err != nil {
		t.Fatal(err)
	}
	injected := hitsFromRanked([]ranked{{chunkID: deniedRows[0].ID, chunk: deniedRows[0]}})[0]
	reranker := &observingReranker{run: func(_ context.Context, hits []Hit) ([]Hit, error) {
		if len(hits) != 1 || hits[0].ObjectID != obj.ID {
			t.Fatalf("unadmitted reranker input: %+v", hits)
		}
		return []Hit{injected}, nil
	}}
	s := boundarySearch(t, env.repo, emb).
		WithHitAuthorizer(&mutableHitAuthorizer{allow: map[string]bool{obj.Key: true}}).
		WithVectorIndex(&fakeVectorIndex{hits: []repository.SearchHit{chunkHit(obj, rows[0]), chunkHit(denied, deniedRows[0])}}).
		WithReranker(reranker)
	if _, err := s.Query(context.Background(), Request{Tenant: testTenant, Bucket: testBucket, Query: "raft", K: 2}); !errors.Is(err, ErrCanonicalHit) {
		t.Fatalf("reranker restored ACL-filtered candidate: %v", err)
	}
}

func TestSearchRevalidatesAfterRerankerSuccessAndFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		for _, change := range []string{"canonical", "authorization", "boundary", "cancel"} {
			t.Run(fmt.Sprintf("%s/failure=%v", change, fail), func(t *testing.T) {
				testSearchRerankerChange(t, change, fail)
			})
		}
	}
}

func testSearchRerankerChange(t *testing.T, change string, fail bool) {
	t.Helper()
	env, emb, obj, rows := seedBoundarySearchEnv(t)
	reader := newSearchBoundaryReader(testTenant, testBucket)
	auth := &mutableHitAuthorizer{allow: map[string]bool{obj.Key: true}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reranker := &observingReranker{run: func(_ context.Context, hits []Hit) ([]Hit, error) {
		switch change {
		case "canonical":
			if err := env.repo.SoftDeleteObject(context.Background(), obj.TenantID, obj.Bucket, obj.Key); err != nil {
				t.Fatal(err)
			}
		case "authorization":
			auth.allow[obj.Key] = false
		case "boundary":
			reader.setBound()
		case "cancel":
			cancel()
		}
		if fail {
			return nil, errors.New("ordinary rerank failure")
		}
		return hits, nil
	}}
	s := NewSearch(env.repo, emb, nil).WithResourceBoundary(reader, reader).WithHitAuthorizer(auth).
		WithVectorIndex(&fakeVectorIndex{hits: []repository.SearchHit{chunkHit(obj, rows[0])}}).WithReranker(reranker)
	hits, err := s.Query(ctx, Request{Tenant: testTenant, Bucket: testBucket, Query: "raft", K: 2})
	if change == "authorization" {
		if err != nil || len(hits) != 0 {
			t.Fatalf("revoked rerank output=%+v err=%v", hits, err)
		}
	} else if err == nil || len(hits) != 0 {
		t.Fatalf("changed rerank output=%+v err=%v", hits, err)
	}
	usage, usageErr := env.repo.ListUsageForObject(context.Background(), testTenant, obj.ID, 10)
	if usageErr != nil || len(usage) != 0 {
		t.Fatalf("rejected reference recorded usage=%+v err=%v", usage, usageErr)
	}
}

func TestSearchP1ChangeMustNotLeakRerankerInput(t *testing.T) {
	for _, canonical := range []bool{false, true} {
		t.Run(fmt.Sprintf("canonical=%v", canonical), func(t *testing.T) {
			env, emb, obj, rows := seedBoundarySearchEnv(t)
			reader := newSearchBoundaryReader(testTenant, testBucket)
			auth := &mutableHitAuthorizer{allow: map[string]bool{obj.Key: true}}
			reader.afterBinding = func() {
				if reader.bindingCalls != 2 {
					return
				}
				if canonical {
					if err := env.repo.SoftDeleteObject(context.Background(), testTenant, testBucket, obj.Key); err != nil {
						t.Fatal(err)
					}
				} else {
					auth.allow[obj.Key] = false
				}
			}
			reranker := &countingSearchReranker{}
			s := NewSearch(env.repo, emb, nil).WithResourceBoundary(reader, reader).WithHitAuthorizer(auth).
				WithVectorIndex(&fakeVectorIndex{hits: []repository.SearchHit{chunkHit(obj, rows[0])}}).WithReranker(reranker)
			hits, err := s.Query(context.Background(), Request{Tenant: testTenant, Bucket: testBucket, Query: "raft", K: 2})
			if reranker.calls != 0 || len(hits) != 0 {
				t.Fatalf("P1 changed input leaked: rerank=%d hits=%+v err=%v", reranker.calls, hits, err)
			}
			if canonical && !errors.Is(err, ErrCanonicalHit) {
				t.Fatalf("P1 canonical change cause=%v", err)
			}
		})
	}
}

func TestSearchSecurityFaultFromBackendNeverDegrades(t *testing.T) {
	env, emb, obj, rows := seedBoundarySearchEnv(t)
	cause := errors.New("backend scope observation failed")
	reasons := swapSeam(t)
	s := boundarySearch(t, env.repo, emb).
		WithVectorIndex(&failingVIndex{err: errors.Join(ErrCanonicalHit, cause)}).
		WithLexicalIndex(&fakeLexical{hits: []repository.SearchHit{chunkHit(obj, rows[0])}})
	_, err := s.Query(context.Background(), Request{Tenant: testTenant, Bucket: testBucket, Query: "raft", K: 2, Mode: "hybrid"})
	if !errors.Is(err, ErrCanonicalHit) || !errors.Is(err, cause) || len(*reasons) != 0 {
		t.Fatalf("security fault became healthy fallback: err=%v degrade=%v", err, *reasons)
	}
}
