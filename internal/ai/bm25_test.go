package ai

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
)

// fakeChunkRepo serves a fixed set of chunks for BuildFromRepo. It embeds the
// Repository interface so only the two methods BuildFromRepo exercises are
// implemented; any other call panics (none are expected).
type fakeChunkRepo struct {
	repository.Repository
	chunks []repository.Chunk
}

func (f *fakeChunkRepo) ListBuckets(_ context.Context, _ string) ([]string, error) {
	seen := map[string]bool{}
	var buckets []string
	for _, c := range f.chunks {
		if !seen[c.Bucket] {
			seen[c.Bucket] = true
			buckets = append(buckets, c.Bucket)
		}
	}
	if len(buckets) == 0 {
		buckets = []string{"default"}
	}
	return buckets, nil
}

func (f *fakeChunkRepo) ListObjects(_ context.Context, _, bucket, _, _ string, _ int) (repository.ListPage, error) {
	seen := map[int64]bool{}
	var objs []repository.Object
	for _, c := range f.chunks {
		if c.Bucket != bucket {
			continue
		}
		if !seen[c.ObjectID] {
			seen[c.ObjectID] = true
			objs = append(objs, repository.Object{ID: c.ObjectID})
		}
	}
	return repository.ListPage{Objects: objs}, nil
}

func (f *fakeChunkRepo) ListChunksForObject(_ context.Context, objectID int64) ([]repository.Chunk, error) {
	var out []repository.Chunk
	for _, c := range f.chunks {
		if c.ObjectID == objectID {
			out = append(out, c)
		}
	}
	return out, nil
}

// chunk is a small helper to build a repository.Chunk for the in-memory BM25
// index without a repository.
func chunk(id, objID int64, bucket, content string, seq int) repository.Chunk {
	return repository.Chunk{
		ID: id, ObjectID: objID, TenantID: testTenant,
		Bucket: bucket, ObjectKey: "obj.txt", Seq: seq, Content: content,
	}
}

// buildReference returns a BM25 built the full-corpus way (BuildFromRepo-style)
// over exactly the given surviving chunks, for asserting incremental state
// matches a fresh build.
func buildReference(t *testing.T, chunks []repository.Chunk) *BM25 {
	t.Helper()
	ref := NewBM25()
	if err := ref.BuildFromRepo(context.Background(), &fakeChunkRepo{chunks: chunks}, testTenant); err != nil {
		t.Fatalf("reference build: %v", err)
	}
	return ref
}

func assertSameState(t *testing.T, got, want *BM25) {
	t.Helper()
	if got.totalDoc != want.totalDoc {
		t.Fatalf("totalDoc: got %d want %d", got.totalDoc, want.totalDoc)
	}
	if got.totalLen != want.totalLen {
		t.Fatalf("totalLen: got %d want %d", got.totalLen, want.totalLen)
	}
	if got.avgLen != want.avgLen {
		t.Fatalf("avgLen: got %v want %v", got.avgLen, want.avgLen)
	}
	if !reflect.DeepEqual(got.df, want.df) {
		t.Fatalf("df mismatch:\n got=%v\nwant=%v", got.df, want.df)
	}
	if len(got.docs) != len(want.docs) {
		t.Fatalf("docs len: got %d want %d", len(got.docs), len(want.docs))
	}
}

func TestBM25UpsertMakesTermsSearchable(t *testing.T) {
	b := NewBM25()
	ctx := context.Background()
	if err := b.UpsertObjectChunks(ctx, 1, []repository.Chunk{
		chunk(10, 1, "alpha", "quokka marsupial", 0),
		chunk(11, 1, "alpha", "smallest wallaby", 1),
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	hits := b.Search("quokka", "", 10)
	if len(hits) != 1 || hits[0].ChunkID != 10 {
		t.Fatalf("expected chunk 10 for 'quokka', got %+v", hits)
	}
	// Correct bucket scoping.
	if got := b.Search("quokka", "alpha", 10); len(got) != 1 {
		t.Fatalf("bucket 'alpha' should match, got %d", len(got))
	}
	if got := b.Search("quokka", "beta", 10); len(got) != 0 {
		t.Fatalf("bucket 'beta' should not match, got %d", len(got))
	}
}

func TestBM25DeleteRemovesUniqueTermFromDF(t *testing.T) {
	b := NewBM25()
	ctx := context.Background()
	// Term "zorptastic" appears ONLY in object A.
	if err := b.UpsertObjectChunks(ctx, 1, []repository.Chunk{
		chunk(10, 1, "b", "zorptastic shared", 0),
	}); err != nil {
		t.Fatalf("upsert A: %v", err)
	}
	if err := b.UpsertObjectChunks(ctx, 2, []repository.Chunk{
		chunk(20, 2, "b", "shared other", 0),
	}); err != nil {
		t.Fatalf("upsert B: %v", err)
	}

	if got := b.Search("zorptastic", "", 10); len(got) == 0 {
		t.Fatal("zorptastic should be searchable before delete")
	}

	if err := b.DeleteObjectChunks(ctx, 1); err != nil {
		t.Fatalf("delete A: %v", err)
	}
	if got := b.Search("zorptastic", "", 10); len(got) != 0 {
		t.Fatalf("zorptastic must be gone after delete, got %d hits", len(got))
	}
	if _, ok := b.df["zorptastic"]; ok {
		t.Fatal("df must drop the term key when frequency reaches 0")
	}
	// idf math is sane: "shared" still in object B, so a new doc with it scores.
	if err := b.UpsertObjectChunks(ctx, 3, []repository.Chunk{
		chunk(30, 3, "b", "shared again", 0),
	}); err != nil {
		t.Fatalf("upsert C: %v", err)
	}
	if got := b.Search("shared", "", 10); len(got) != 2 {
		t.Fatalf("'shared' should now match objects B and C, got %d", len(got))
	}

	assertSameState(t, b, buildReference(t, []repository.Chunk{
		chunk(20, 2, "b", "shared other", 0),
		chunk(30, 3, "b", "shared again", 0),
	}))
}

func TestBM25ReUpsertReplacesContent(t *testing.T) {
	b := NewBM25()
	ctx := context.Background()
	if err := b.UpsertObjectChunks(ctx, 1, []repository.Chunk{
		chunk(10, 1, "b", "obsolete penguin", 0),
		chunk(11, 1, "b", "obsolete walrus", 1),
	}); err != nil {
		t.Fatalf("upsert v1: %v", err)
	}
	if got := b.Search("penguin", "", 10); len(got) == 0 {
		t.Fatal("penguin should be searchable in v1")
	}

	// Re-upsert the SAME object with different content and a different chunk
	// count, several times — totalDoc must not grow unbounded.
	for i := 0; i < 5; i++ {
		if err := b.UpsertObjectChunks(ctx, 1, []repository.Chunk{
			chunk(12, 1, "b", "fresh narwhal", 0),
		}); err != nil {
			t.Fatalf("re-upsert: %v", err)
		}
	}

	if got := b.Search("penguin", "", 10); len(got) != 0 {
		t.Fatalf("obsolete 'penguin' must be gone after replace, got %d", len(got))
	}
	if got := b.Search("narwhal", "", 10); len(got) != 1 || got[0].ChunkID != 12 {
		t.Fatalf("new 'narwhal' should be found as chunk 12, got %+v", got)
	}
	if b.totalDoc != 1 {
		t.Fatalf("totalDoc must reflect only surviving chunks, got %d", b.totalDoc)
	}
	if _, ok := b.df["penguin"]; ok {
		t.Fatal("obsolete term 'penguin' must be purged from df")
	}

	assertSameState(t, b, buildReference(t, []repository.Chunk{
		chunk(12, 1, "b", "fresh narwhal", 0),
	}))
}

func TestBM25IncrementalMatchesFreshBuild(t *testing.T) {
	b := NewBM25()
	ctx := context.Background()

	mustUpsert := func(obj int64, cs ...repository.Chunk) {
		if err := b.UpsertObjectChunks(ctx, obj, cs); err != nil {
			t.Fatalf("upsert %d: %v", obj, err)
		}
	}

	mustUpsert(1, chunk(10, 1, "b", "the quick brown fox", 0), chunk(11, 1, "b", "jumps over lazy dog", 1))
	mustUpsert(2, chunk(20, 2, "c", "quick foxes are clever", 0))
	mustUpsert(3, chunk(30, 3, "b", "lazy summer afternoon", 0))
	// Replace object 1.
	mustUpsert(1, chunk(12, 1, "b", "a slow purple turtle", 0))
	// Delete object 2.
	if err := b.DeleteObjectChunks(ctx, 2); err != nil {
		t.Fatalf("delete 2: %v", err)
	}
	// Add a fresh object.
	mustUpsert(4, chunk(40, 4, "c", "turtle races lazy snail", 0), chunk(41, 4, "c", "snail trails glisten", 1))

	surviving := []repository.Chunk{
		chunk(12, 1, "b", "a slow purple turtle", 0),
		chunk(30, 3, "b", "lazy summer afternoon", 0),
		chunk(40, 4, "c", "turtle races lazy snail", 0),
		chunk(41, 4, "c", "snail trails glisten", 1),
	}
	assertSameState(t, b, buildReference(t, surviving))
}

func TestBM25ConcurrentSearchAndMutate(t *testing.T) {
	b := NewBM25()
	ctx := context.Background()
	// Seed so Search has something to scan.
	for i := int64(1); i <= 8; i++ {
		_ = b.UpsertObjectChunks(ctx, i, []repository.Chunk{
			chunk(i*100, i, "b", "concurrent indexing stress content", 0),
		})
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
					_ = b.Search("concurrent content", "", 5)
				}
			}
		}()
	}
	// Writers: upsert + delete the same set of objects repeatedly.
	for w := 0; w < 4; w++ {
		writers.Add(1)
		go func(base int64) {
			defer writers.Done()
			for i := 0; i < 200; i++ {
				obj := base + 1
				_ = b.UpsertObjectChunks(ctx, obj, []repository.Chunk{
					chunk(obj*1000+int64(i%3), obj, "b", "mutating concurrent payload here", 0),
				})
				if i%2 == 0 {
					_ = b.DeleteObjectChunks(ctx, obj)
				}
			}
		}(int64(w))
	}

	writers.Wait()
	close(stop)
	readers.Wait()
}
