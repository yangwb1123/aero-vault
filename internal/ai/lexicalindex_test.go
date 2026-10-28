package ai

import (
	"context"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
)

type fakeLexical struct {
	called bool
	hits   []repository.SearchHit
}

func (f *fakeLexical) SearchLexical(_ context.Context, _, _, _ string, _ int) ([]repository.SearchHit, error) {
	f.called = true
	return f.hits, nil
}

func seedInjectedLexicalEnv(t *testing.T) (*testEnv, *HashEmbedder) {
	t.Helper()
	ctx := context.Background()
	env := newTestEnv(t)
	emb := NewHashEmbedder(64)
	primary := env.putObject(t, "k.txt", "text/plain", "primary")
	seedCanonicalQueryRows(t, env, primary, emb, "c1", "c2", "c3", "c4", "c5", "c6", "c7", "c8", "x")
	stale := env.putObject(t, "stale.txt", "text/plain", "stale")
	vecs, err := emb.Embed(ctx, []string{"x"})
	if err != nil {
		t.Fatalf("embed stale chunk: %v", err)
	}
	if err := env.repo.InsertChunks(ctx, []repository.Chunk{{
		ObjectID: stale.ID, TenantID: stale.TenantID, Bucket: stale.Bucket, ObjectKey: stale.Key,
		Seq: 0, Content: "x", Embedding: vecs[0], Dim: len(vecs[0]), EmbedModel: "stale-model",
	}}); err != nil {
		t.Fatalf("insert stale chunk: %v", err)
	}
	primaryChunks, err := env.repo.ListChunksForObject(ctx, primary.ID)
	if err != nil {
		t.Fatalf("list primary chunks: %v", err)
	}
	staleChunks, err := env.repo.ListChunksForObject(ctx, stale.ID)
	if err != nil {
		t.Fatalf("list stale chunks: %v", err)
	}
	if len(primaryChunks) != 9 || len(staleChunks) != 1 {
		t.Fatalf("unexpected canonical chunk counts %d/%d", len(primaryChunks), len(staleChunks))
	}
	if primary.ID != 1 || stale.ID != 2 {
		t.Fatalf("fixture object IDs=%d/%d, want original 1/2", primary.ID, stale.ID)
	}
	return env, emb
}

func TestSearch_UsesInjectedLexicalIndex(t *testing.T) {
	ctx := context.Background()
	env, emb := seedInjectedLexicalEnv(t)
	fake := &fakeLexical{hits: []repository.SearchHit{
		{
			Score: 0.5,
			Chunk: repository.Chunk{
				ID: 9, ObjectID: 1, TenantID: testTenant, Bucket: testBucket, ObjectKey: "k.txt",
				Content: "x", EmbedModel: "hash-64",
			},
		},
		{
			Score: 0.9,
			Chunk: repository.Chunk{
				ID: 10, ObjectID: 2, TenantID: testTenant, Bucket: testBucket, ObjectKey: "stale.txt",
				Content: "x", EmbedModel: "stale-model",
			},
		},
	}}

	s := NewSearch(env.repo, emb, nil).WithLexicalIndex(fake).WithCanonicalResources(env.repo)
	hits, err := s.Query(ctx, Request{Tenant: testTenant, Bucket: testBucket, Query: "hi", K: 5, Mode: "bm25"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if !fake.called {
		t.Fatal("bm25 mode should route through the injected LexicalIndex")
	}
	if len(hits) != 1 || hits[0].ChunkID != 9 {
		t.Fatalf("expected only current-model lexical hit (chunk 9), got %+v", hits)
	}
}

func TestOpenPgFTSIndex_EmptyDSN(t *testing.T) {
	if _, err := OpenPgFTSIndex(context.Background(), "", PgFTSOptions{}); err == nil {
		t.Fatal("expected error for empty dsn")
	}
}

var _ LexicalIndex = (*fakeLexical)(nil)
