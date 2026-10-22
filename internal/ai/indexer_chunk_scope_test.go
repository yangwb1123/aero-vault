package ai

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
)

func TestIndexerChunkSinkConfigurationFaults(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	obj := env.putObject(t, "doc.txt", "text/plain", "alpha beta gamma")
	newIX := func(s ChunkSink) *Indexer {
		return NewIndexer(env.repo, env.store, NewDefaultExtractor(), NewChunker(), NewHashEmbedder(64), nil).
			WithChunkSink(s)
	}

	var typedNil *typedNilScopedSink
	legacy := &legacyOnlySink{}
	cases := []struct {
		name string
		ix   *Indexer
	}{
		{"nil", newIX(nil)},
		{"typed-nil", newIX(typedNil)},
		{"legacy-only", newIX(legacy)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.ix.ValidateChunkSinks(); err == nil || !strings.Contains(err.Error(), "chunk sink") {
				t.Fatalf("ValidateChunkSinks: want configuration fault, got %v", err)
			}
			if err := tc.ix.IndexObjectByID(ctx, obj.ID); err == nil {
				t.Fatal("indexing with a faulted sink configuration must fail before work")
			}
			if err := tc.ix.DeleteObjectChunks(ctx, obj.ID); err == nil {
				t.Fatal("deleting with a faulted sink configuration must fail before work")
			}
		})
	}
	if legacy.upsertCalls != 0 || legacy.deleteCalls != 0 {
		t.Fatalf("legacy-only sink must never be called: upsert=%d delete=%d", legacy.upsertCalls, legacy.deleteCalls)
	}

	noSinks := NewIndexer(env.repo, env.store, NewDefaultExtractor(), NewChunker(), NewHashEmbedder(64), nil)
	if err := noSinks.ValidateChunkSinks(); err != nil {
		t.Fatalf("zero sinks must be valid: %v", err)
	}
}

func TestIndexerScopedSinkReceivesExactRef(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	obj := env.putObject(t, "doc.txt", "text/plain", "alpha beta gamma")
	sink := &scopedRecordingSink{}
	ix := NewIndexer(env.repo, env.store, NewDefaultExtractor(), NewChunker(), NewHashEmbedder(64), nil).
		WithChunkSink(sink)

	if err := ix.IndexObjectByID(ctx, obj.ID); err != nil {
		t.Fatalf("index: %v", err)
	}
	want := access.ChunkRef{TenantID: obj.TenantID, Bucket: obj.Bucket, Key: obj.Key, ObjectID: obj.ID}
	if sink.upsertCalls != 1 || sink.lastUpsert.ref != want {
		t.Fatalf("upsert ref: calls=%d ref=%+v want %+v", sink.upsertCalls, sink.lastUpsert.ref, want)
	}
	if len(sink.lastUpsert.chunks) == 0 {
		t.Fatal("expected canonical chunks")
	}
	for _, c := range sink.lastUpsert.chunks {
		if c.ID == 0 || len(c.Embedding) == 0 {
			t.Fatalf("sink row is not canonical: %+v", c)
		}
	}

	if err := ix.DeleteObjectChunks(ctx, obj.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if sink.deleteCalls != 1 || sink.deletes[0] != want {
		t.Fatalf("delete ref: calls=%d refs=%+v want %+v", sink.deleteCalls, sink.deletes, want)
	}
}

func TestIndexerCanonicalRereadEmptyOrMixedFaults(t *testing.T) {
	ctx := context.Background()
	t.Run("empty", func(t *testing.T) {
		env := newTestEnv(t)
		obj := env.putObject(t, "empty.txt", "text/plain", "alpha beta gamma")
		sink := &scopedRecordingSink{}
		repo := &listChunksOverrideRepo{Repository: env.repo, override: true}
		ix := NewIndexer(repo, env.store, NewDefaultExtractor(), NewChunker(), NewHashEmbedder(64), nil).
			WithChunkSink(sink)
		err := ix.IndexObjectByID(ctx, obj.ID)
		if err == nil || !strings.Contains(err.Error(), "canonical") {
			t.Fatalf("empty canonical reread should fault, got %v", err)
		}
		if sink.upsertCalls != 0 {
			t.Fatalf("sink must not see an unexpected empty reread, calls=%d", sink.upsertCalls)
		}
	})
	t.Run("mixed", func(t *testing.T) {
		env := newTestEnv(t)
		obj := env.putObject(t, "mixed.txt", "text/plain", "alpha beta gamma")
		sink := &scopedRecordingSink{}
		rows := []repository.Chunk{
			{ID: 1, ObjectID: obj.ID, TenantID: obj.TenantID, Bucket: obj.Bucket, ObjectKey: obj.Key, Seq: 0, Content: "ok"},
			{ID: 2, ObjectID: obj.ID, TenantID: "other", Bucket: obj.Bucket, ObjectKey: obj.Key, Seq: 1, Content: "bad"},
		}
		repo := &listChunksOverrideRepo{Repository: env.repo, override: true, rows: rows}
		ix := NewIndexer(repo, env.store, NewDefaultExtractor(), NewChunker(), NewHashEmbedder(64), nil).
			WithChunkSink(sink)
		err := ix.IndexObjectByID(ctx, obj.ID)
		if err == nil || !strings.Contains(err.Error(), "scope conflict") {
			t.Fatalf("mixed canonical reread should fault, got %v", err)
		}
		if sink.upsertCalls != 0 {
			t.Fatalf("sink must not see mixed rows, calls=%d", sink.upsertCalls)
		}
	})
}

func TestIndexerDeleteUnresolvedWithoutEvidence(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	sink := &scopedRecordingSink{}
	ix := NewIndexer(env.repo, env.store, NewDefaultExtractor(), NewChunker(), NewHashEmbedder(64), nil).
		WithChunkSink(sink)
	err := ix.DeleteObjectChunks(ctx, 999999)
	if err == nil || !strings.Contains(err.Error(), "unresolved") {
		t.Fatalf("missing object and chunks must be unresolved, got %v", err)
	}
	if sink.deleteCalls != 0 {
		t.Fatalf("unresolved delete must not reach sinks, calls=%d", sink.deleteCalls)
	}
}

func TestIndexerSinkCleansEachOldVersionRef(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	if err := env.svc.SetBucketVersioning(ctx, "", "", true); err != nil {
		t.Fatalf("enable versioning: %v", err)
	}
	sink := &scopedRecordingSink{}
	ix := NewIndexer(env.repo, env.store, NewDefaultExtractor(), NewChunker(), NewHashEmbedder(64), nil).
		WithChunkSink(sink)
	old := env.putObject(t, "versioned.txt", "text/plain", "old body")
	oldRef := access.ChunkRef{TenantID: old.TenantID, Bucket: old.Bucket, Key: old.Key, ObjectID: old.ID}
	if err := ix.IndexObjectByID(ctx, old.ID); err != nil {
		t.Fatalf("index old: %v", err)
	}
	current := env.putObject(t, "versioned.txt", "text/plain", "current body")
	currentRef := access.ChunkRef{TenantID: current.TenantID, Bucket: current.Bucket, Key: current.Key, ObjectID: current.ID}
	if err := ix.IndexObjectByID(ctx, current.ID); err != nil {
		t.Fatalf("index current: %v", err)
	}
	if sink.deleteCalls != 1 || sink.deletes[0] != oldRef {
		t.Fatalf("old version delete ref: calls=%d refs=%+v want %+v", sink.deleteCalls, sink.deletes, oldRef)
	}
	if sink.lastUpsert.ref != currentRef {
		t.Fatalf("current upsert ref: %+v want %+v", sink.lastUpsert.ref, currentRef)
	}
}

func TestReindexStaleCountsOnlyIndexed(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	emb := NewHashEmbedder(64)
	obj := env.putObject(t, "skip.bin", "application/pdf", "%PDF unsupported")
	if err := env.repo.InsertChunks(ctx, []repository.Chunk{{
		ObjectID: obj.ID, TenantID: obj.TenantID, Bucket: obj.Bucket, ObjectKey: obj.Key,
		Seq: 0, Content: "stale", Embedding: []float32{0, 0, 0}, Dim: 3, EmbedModel: "old-model",
	}}); err != nil {
		t.Fatalf("seed stale: %v", err)
	}
	ix := NewIndexer(env.repo, env.store, NewDefaultExtractor(), NewChunker(), emb, nil)
	n, err := ix.ReindexStale(ctx, testTenant, 10)
	if err != nil {
		t.Fatalf("reindex skips should not fault: %v", err)
	}
	if n != 0 {
		t.Fatalf("skips must not inflate the indexed count, got %d", n)
	}
	chunks, err := env.repo.ListChunksForObject(ctx, obj.ID)
	if err != nil || len(chunks) != 1 {
		t.Fatalf("skip should preserve the stale row for a later retry: chunks=%d err=%v", len(chunks), err)
	}
}

func TestReindexStaleReturnsFault(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	emb := NewHashEmbedder(64)
	obj := env.putObject(t, "fault.txt", "text/plain", "alpha beta gamma")
	if err := env.repo.InsertChunks(ctx, []repository.Chunk{{
		ObjectID: obj.ID, TenantID: obj.TenantID, Bucket: obj.Bucket, ObjectKey: obj.Key,
		Seq: 0, Content: "stale", Embedding: []float32{0, 0, 0}, Dim: 3, EmbedModel: "old-model",
	}}); err != nil {
		t.Fatalf("seed stale: %v", err)
	}
	sinkErr := errors.New("sink down")
	ix := NewIndexer(env.repo, env.store, NewDefaultExtractor(), NewChunker(), emb, nil).
		WithChunkSink(&scopedRecordingSink{upsertErr: sinkErr})
	n, err := ix.ReindexStale(ctx, testTenant, 10)
	if n != 0 || !errors.Is(err, sinkErr) {
		t.Fatalf("reindex fault: n=%d err=%v want n=0 sink error", n, err)
	}
}

func TestIndexerDeleteObservedMixedChunksFaults(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	obj := env.putObject(t, "mixed-delete.txt", "text/plain", "mixed delete")
	if err := env.repo.InsertChunks(ctx, []repository.Chunk{{
		ObjectID: obj.ID, TenantID: "other", Bucket: obj.Bucket, ObjectKey: obj.Key,
		Seq: 0, Content: "bad scope",
	}}); err != nil {
		t.Fatalf("seed mixed chunk: %v", err)
	}
	sink := &scopedRecordingSink{}
	ix := NewIndexer(env.repo, env.store, NewDefaultExtractor(), NewChunker(), NewHashEmbedder(64), nil).
		WithChunkSink(sink)
	err := ix.DeleteObjectChunks(ctx, obj.ID)
	if err == nil || !strings.Contains(err.Error(), "scope conflict") {
		t.Fatalf("observed mixed chunks must fault before deletion, got %v", err)
	}
	if sink.deleteCalls != 0 {
		t.Fatalf("faulted delete reached the sink %d times", sink.deleteCalls)
	}
	chunks, listErr := env.repo.ListChunksForObject(ctx, obj.ID)
	if listErr != nil || len(chunks) != 1 {
		t.Fatalf("fault must not delete local chunks: chunks=%d err=%v", len(chunks), listErr)
	}
}

func TestIndexerImplementsScopedCleanerSeam(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	obj := env.putObject(t, "cleaner.txt", "text/plain", "alpha beta gamma")
	emb := NewHashEmbedder(64)
	env.seedChunks(t, obj, emb, "alpha beta gamma")
	sink := &scopedRecordingSink{}
	ix := NewIndexer(env.repo, env.store, NewDefaultExtractor(), NewChunker(), emb, nil).
		WithChunkSink(sink)
	var cleaner access.ScopedChunkCleaner = ix
	ref := access.ChunkRef{TenantID: obj.TenantID, Bucket: obj.Bucket, Key: obj.Key, ObjectID: obj.ID}
	if err := cleaner.DeleteScopedObjectChunks(ctx, ref); err != nil {
		t.Fatalf("scoped cleaner seam: %v", err)
	}
	rows, err := env.repo.ListChunksForObject(ctx, obj.ID)
	if err != nil {
		t.Fatalf("list chunks after scoped cleanup: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("scoped cleaner left %d local chunks", len(rows))
	}
	if sink.deleteCalls != 1 {
		t.Fatalf("scoped cleaner sink delete calls = %d, want 1", sink.deleteCalls)
	}
}

func TestIndexObjectByIDVanishedWithSinkIsUnresolved(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	obj := env.putObject(t, "gone-index.txt", "text/plain", "gone")
	if err := env.repo.HardDeleteObjectByID(ctx, obj.ID); err != nil {
		t.Fatalf("hard delete: %v", err)
	}
	sink := &scopedRecordingSink{}
	ix := NewIndexer(env.repo, env.store, NewDefaultExtractor(), NewChunker(), NewHashEmbedder(64), nil).
		WithChunkSink(sink)
	err := ix.IndexObjectByID(ctx, obj.ID)
	if err == nil || !strings.Contains(err.Error(), "unresolved") {
		t.Fatalf("vanished index source with sink must be unresolved, got %v", err)
	}
	if sink.deleteCalls != 0 {
		t.Fatalf("unresolved index retry must not delete externally, calls=%d", sink.deleteCalls)
	}
}
