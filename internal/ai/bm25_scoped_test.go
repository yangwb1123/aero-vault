package ai

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
)

type bm25State struct {
	totalDoc int
	totalLen int
	avgLen   float64
	df       map[string]int
	objDocs  map[int64][]int64
	docs     map[int64]bm25Doc
	dirty    bool
}

func snapshotBM25(b *BM25) bm25State {
	b.mu.RLock()
	defer b.mu.RUnlock()
	df := make(map[string]int, len(b.df))
	for k, v := range b.df {
		df[k] = v
	}
	objDocs := make(map[int64][]int64, len(b.objDocs))
	for k, ids := range b.objDocs {
		objDocs[k] = append([]int64(nil), ids...)
	}
	docs := make(map[int64]bm25Doc, len(b.docs))
	for id, d := range b.docs {
		tokens := make(map[string]int, len(d.tokens))
		for k, v := range d.tokens {
			tokens[k] = v
		}
		d.tokens = tokens
		docs[id] = d
	}
	return bm25State{
		totalDoc: b.totalDoc, totalLen: b.totalLen, avgLen: b.avgLen,
		df: df, objDocs: objDocs, docs: docs, dirty: b.dirty,
	}
}

func assertBM25State(t *testing.T, b *BM25, want bm25State) {
	t.Helper()
	got := snapshotBM25(b)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("bm25 state changed on refusal:\n got=%+v\nwant=%+v", got, want)
	}
}

func TestBM25ScopedReplaceDeletePreservesOtherTenant(t *testing.T) {
	ctx := context.Background()
	b := NewBM25()
	refA := access.ChunkRef{TenantID: "tenant-a", Bucket: "b", Key: "obj.txt", ObjectID: 1}
	refB := access.ChunkRef{TenantID: "tenant-b", Bucket: "b", Key: "obj.txt", ObjectID: 2}
	if err := b.UpsertScopedObjectChunks(ctx, refA, []repository.Chunk{
		tenantChunk(refA.TenantID, 10, refA.ObjectID, refA.Bucket, "obsolete penguin", 0),
	}); err != nil {
		t.Fatalf("upsert A: %v", err)
	}
	if err := b.UpsertScopedObjectChunks(ctx, refB, []repository.Chunk{
		tenantChunk(refB.TenantID, 20, refB.ObjectID, refB.Bucket, "betaonly original", 0),
	}); err != nil {
		t.Fatalf("upsert B: %v", err)
	}
	if err := b.UpsertScopedObjectChunks(ctx, refA, []repository.Chunk{
		tenantChunk(refA.TenantID, 11, refA.ObjectID, refA.Bucket, "fresh narwhal", 0),
	}); err != nil {
		t.Fatalf("replace A: %v", err)
	}
	if got := b.Search(refA.TenantID, "penguin", refA.Bucket, 10); len(got) != 0 {
		t.Fatalf("old A content survived replacement: %+v", got)
	}
	if got := b.Search(refA.TenantID, "narwhal", refA.Bucket, 10); len(got) != 1 || got[0].ChunkID != 11 {
		t.Fatalf("new A content missing: %+v", got)
	}
	if got := b.Search(refB.TenantID, "betaonly", refB.Bucket, 10); len(got) != 1 {
		t.Fatalf("B was disturbed by A replacement: %+v", got)
	}
	if err := b.DeleteScopedObjectChunks(ctx, refA); err != nil {
		t.Fatalf("delete A: %v", err)
	}
	if got := b.Search(refA.TenantID, "narwhal", refA.Bucket, 10); len(got) != 0 {
		t.Fatalf("A survived scoped delete: %+v", got)
	}
	if got := b.Search(refB.TenantID, "betaonly", refB.Bucket, 10); len(got) != 1 {
		t.Fatalf("B was disturbed by A delete: %+v", got)
	}
}

func TestBM25ScopedRefusalsAreAtomic(t *testing.T) {
	ctx := context.Background()
	b := NewBM25()
	refA := access.ChunkRef{TenantID: "tenant-a", Bucket: "b", Key: "obj.txt", ObjectID: 1}
	refB := access.ChunkRef{TenantID: "tenant-b", Bucket: "b", Key: "obj.txt", ObjectID: 2}
	if err := b.UpsertScopedObjectChunks(ctx, refA, []repository.Chunk{
		tenantChunk(refA.TenantID, 10, refA.ObjectID, refA.Bucket, "alpha", 0),
	}); err != nil {
		t.Fatal(err)
	}
	if err := b.UpsertScopedObjectChunks(ctx, refB, []repository.Chunk{
		tenantChunk(refB.TenantID, 20, refB.ObjectID, refB.Bucket, "beta", 0),
	}); err != nil {
		t.Fatal(err)
	}
	before := snapshotBM25(b)
	cases := []struct {
		name   string
		ref    access.ChunkRef
		chunks []repository.Chunk
	}{
		{"invalid-ref", access.ChunkRef{}, []repository.Chunk{tenantChunk(refA.TenantID, 11, refA.ObjectID, refA.Bucket, "x", 0)}},
		{"wrong-tenant", refA, []repository.Chunk{tenantChunk(refB.TenantID, 11, refA.ObjectID, refA.Bucket, "x", 0)}},
		{"wrong-object", refA, []repository.Chunk{tenantChunk(refA.TenantID, 11, refB.ObjectID, refA.Bucket, "x", 0)}},
		{"zero-id", refA, []repository.Chunk{tenantChunk(refA.TenantID, 0, refA.ObjectID, refA.Bucket, "x", 0)}},
		{"duplicate", refA, []repository.Chunk{
			tenantChunk(refA.TenantID, 11, refA.ObjectID, refA.Bucket, "x", 0),
			tenantChunk(refA.TenantID, 11, refA.ObjectID, refA.Bucket, "y", 1),
		}},
		{"cross-object-id", refA, []repository.Chunk{tenantChunk(refA.TenantID, 20, refA.ObjectID, refA.Bucket, "x", 0)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := b.UpsertScopedObjectChunks(ctx, tc.ref, tc.chunks); err == nil {
				t.Fatal("invalid scoped upsert must be refused")
			}
			assertBM25State(t, b, before)
		})
	}
	if err := b.DeleteScopedObjectChunks(ctx, access.ChunkRef{}); err == nil {
		t.Fatal("invalid scoped delete must be refused")
	}
	assertBM25State(t, b, before)
}

func TestBM25ScopedRefusesMixedExistingScope(t *testing.T) {
	ctx := context.Background()
	b := NewBM25()
	ref := access.ChunkRef{TenantID: "tenant-a", Bucket: "b", Key: "obj.txt", ObjectID: 1}
	if err := b.UpsertScopedObjectChunks(ctx, ref, []repository.Chunk{
		tenantChunk(ref.TenantID, 10, ref.ObjectID, ref.Bucket, "alpha", 0),
	}); err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	doc := b.docs[10]
	doc.tenant = "tenant-b"
	b.docs[10] = doc
	b.mu.Unlock()
	before := snapshotBM25(b)
	if err := b.DeleteScopedObjectChunks(ctx, ref); err == nil {
		t.Fatal("mixed existing scope must be refused")
	}
	assertBM25State(t, b, before)
	if err := b.UpsertScopedObjectChunks(ctx, ref, []repository.Chunk{
		tenantChunk(ref.TenantID, 11, ref.ObjectID, ref.Bucket, "new", 0),
	}); err == nil {
		t.Fatal("replace over mixed existing scope must be refused")
	}
	assertBM25State(t, b, before)
}

type warmupFactsRepo struct {
	repository.Repository
	buckets []string
	objects []repository.Object
	chunks  map[int64][]repository.Chunk
}

func (r *warmupFactsRepo) ListBuckets(context.Context, string) ([]string, error) {
	return r.buckets, nil
}

func (r *warmupFactsRepo) ListObjects(context.Context, string, string, string, string, int) (repository.ListPage, error) {
	return repository.ListPage{Objects: r.objects}, nil
}

func (r *warmupFactsRepo) ListChunksForObject(_ context.Context, objectID int64) ([]repository.Chunk, error) {
	return r.chunks[objectID], nil
}

type stuckWarmupRepo struct {
	repository.Repository
}

func (r *stuckWarmupRepo) ListBuckets(context.Context, string) ([]string, error) {
	return []string{"b"}, nil
}

func (r *stuckWarmupRepo) ListObjects(context.Context, string, string, string, string, int) (repository.ListPage, error) {
	return repository.ListPage{HasMore: true}, nil
}

func TestBM25WarmupValidationIsAtomic(t *testing.T) {
	ctx := context.Background()
	b := NewBM25()
	refA := access.ChunkRef{TenantID: "tenant-a", Bucket: "b", Key: "obj.txt", ObjectID: 1}
	if err := b.UpsertScopedObjectChunks(ctx, refA, []repository.Chunk{
		tenantChunk(refA.TenantID, 10, refA.ObjectID, refA.Bucket, "alpha original", 0),
	}); err != nil {
		t.Fatal(err)
	}
	before := snapshotBM25(b)
	chunk := tenantChunk(refA.TenantID, 11, 2, "b", "replacement", 0)

	cases := []struct {
		name string
		ctx  context.Context
		repo repository.Repository
	}{
		{"incomplete-object", ctx, &warmupFactsRepo{
			buckets: []string{"b"},
			objects: []repository.Object{{ID: 2, Bucket: "b", Key: "obj.txt"}},
			chunks:  map[int64][]repository.Chunk{2: {chunk}},
		}},
		{"empty-chunk-tenant", ctx, &warmupFactsRepo{
			buckets: []string{"b"},
			objects: []repository.Object{{ID: 2, TenantID: refA.TenantID, Bucket: "b", Key: "obj.txt"}},
			chunks: map[int64][]repository.Chunk{2: {
				tenantChunk("", 11, 2, "b", "bad", 0),
			}},
		}},
		{"stuck-pagination", ctx, &stuckWarmupRepo{}},
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	cases = append(cases, struct {
		name string
		ctx  context.Context
		repo repository.Repository
	}{"canceled", canceled, &fakeChunkRepo{chunks: []repository.Chunk{chunk}}})

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := b.BuildFromRepo(tc.ctx, tc.repo, refA.TenantID); err == nil {
				t.Fatal("invalid warmup must fail")
			}
			assertBM25State(t, b, before)
		})
	}
}

func TestBM25WarmupSuccessReplacesOnlyTenant(t *testing.T) {
	ctx := context.Background()
	b := NewBM25()
	refA := access.ChunkRef{TenantID: "tenant-a", Bucket: "b", Key: "obj.txt", ObjectID: 1}
	refB := access.ChunkRef{TenantID: "tenant-b", Bucket: "b", Key: "obj.txt", ObjectID: 2}
	if err := b.UpsertScopedObjectChunks(ctx, refA, []repository.Chunk{
		tenantChunk(refA.TenantID, 10, refA.ObjectID, refA.Bucket, "alpha old", 0),
	}); err != nil {
		t.Fatal(err)
	}
	if err := b.UpsertScopedObjectChunks(ctx, refB, []repository.Chunk{
		tenantChunk(refB.TenantID, 20, refB.ObjectID, refB.Bucket, "beta only", 0),
	}); err != nil {
		t.Fatal(err)
	}
	repo := &fakeChunkRepo{chunks: []repository.Chunk{
		tenantChunk(refA.TenantID, 11, refA.ObjectID, refA.Bucket, "alpha fresh", 0),
		tenantChunk(refB.TenantID, 20, refB.ObjectID, refB.Bucket, "beta only", 0),
	}}
	if err := b.BuildFromRepo(ctx, repo, refA.TenantID); err != nil {
		t.Fatalf("warmup: %v", err)
	}
	if got := b.Search(refA.TenantID, "old", refA.Bucket, 10); len(got) != 0 {
		t.Fatalf("old tenant A content survived warmup: %+v", got)
	}
	if got := b.Search(refA.TenantID, "fresh", refA.Bucket, 10); len(got) != 1 || got[0].ChunkID != 11 {
		t.Fatalf("fresh tenant A content missing: %+v", got)
	}
	if got := b.Search(refB.TenantID, "beta", refB.Bucket, 10); len(got) != 1 || got[0].ChunkID != 20 {
		t.Fatalf("tenant B was replaced: %+v", got)
	}
}

func TestBM25ScopedConcurrentReadWrite(t *testing.T) {
	b := NewBM25()
	ctx := context.Background()
	refs := make([]access.ChunkRef, 0, 8)
	for i := int64(1); i <= 8; i++ {
		ref := access.ChunkRef{TenantID: "tenant-a", Bucket: "b", Key: "obj.txt", ObjectID: i}
		refs = append(refs, ref)
		if err := b.UpsertScopedObjectChunks(ctx, ref, []repository.Chunk{
			tenantChunk(ref.TenantID, i*100, i, ref.Bucket, "concurrent indexing stress content", 0),
		}); err != nil {
			t.Fatal(err)
		}
	}

	var readers, writers sync.WaitGroup
	stop := make(chan struct{})
	for r := 0; r < 4; r++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = b.Search("tenant-a", "concurrent content", "", 5)
				}
			}
		}()
	}
	for w := 0; w < 4; w++ {
		writers.Add(1)
		go func(base int) {
			defer writers.Done()
			for i := 0; i < 100; i++ {
				ref := refs[(base+i)%len(refs)]
				_ = b.UpsertScopedObjectChunks(ctx, ref, []repository.Chunk{
					tenantChunk(ref.TenantID, ref.ObjectID*1000+int64(i%3), ref.ObjectID, ref.Bucket, "mutating concurrent payload", 0),
				})
				if i%2 == 0 {
					_ = b.DeleteScopedObjectChunks(ctx, ref)
				}
			}
		}(w)
	}
	writers.Wait()
	close(stop)
	readers.Wait()
}
