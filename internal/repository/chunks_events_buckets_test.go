package repository_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
)

func openCebTestRepo(t *testing.T) repository.Repository {
	t.Helper()
	ctx := context.Background()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	return repo
}

func upsertObj(t *testing.T, repo repository.Repository, tenant, bucket, key string) repository.Object {
	t.Helper()
	ctx := context.Background()
	obj, err := repo.UpsertObject(ctx, repository.Object{
		TenantID:    tenant,
		Bucket:      bucket,
		Key:         key,
		Backend:     "local",
		StorageKey:  "test/" + key,
		Size:        100,
		ETag:        `"abc123"`,
		ContentType: "text/plain",
	})
	if err != nil {
		t.Fatalf("UpsertObject(%q): %v", key, err)
	}
	return obj
}

// --- Chunks ---

func TestDeleteChunksForObject(t *testing.T) {
	ctx := context.Background()
	repo := openCebTestRepo(t)

	obj := upsertObj(t, repo, "default", "b", "k")
	chunks := []repository.Chunk{
		{ObjectID: obj.ID, TenantID: "default", Bucket: "b", ObjectKey: "k", Seq: 0, Content: "a"},
		{ObjectID: obj.ID, TenantID: "default", Bucket: "b", ObjectKey: "k", Seq: 1, Content: "b"},
	}
	if err := repo.InsertChunks(ctx, chunks); err != nil {
		t.Fatalf("InsertChunks: %v", err)
	}
	listed, err := repo.ListChunksForObject(ctx, obj.ID)
	if err != nil {
		t.Fatalf("ListChunksForObject: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("got %d chunks, want 2", len(listed))
	}

	if err := repo.DeleteChunksForObject(ctx, obj.ID); err != nil {
		t.Fatalf("DeleteChunksForObject: %v", err)
	}
	listed, err = repo.ListChunksForObject(ctx, obj.ID)
	if err != nil {
		t.Fatalf("ListChunksForObject after delete: %v", err)
	}
	if len(listed) != 0 {
		t.Fatalf("got %d chunks after delete, want 0", len(listed))
	}
}

func TestDeleteChunksForObject_nonexistent(t *testing.T) {
	ctx := context.Background()
	repo := openCebTestRepo(t)

	if err := repo.DeleteChunksForObject(ctx, 99999); err != nil {
		t.Fatalf("DeleteChunksForObject (nonexistent): %v", err)
	}
}

func TestInsertChunks(t *testing.T) {
	ctx := context.Background()
	repo := openCebTestRepo(t)

	obj := upsertObj(t, repo, "default", "b", "k")
	chunks := []repository.Chunk{
		{ObjectID: obj.ID, TenantID: "default", Bucket: "b", ObjectKey: "k", Seq: 0, Content: "zero"},
		{ObjectID: obj.ID, TenantID: "default", Bucket: "b", ObjectKey: "k", Seq: 1, Content: "one"},
		{ObjectID: obj.ID, TenantID: "default", Bucket: "b", ObjectKey: "k", Seq: 2, Content: "two"},
	}
	if err := repo.InsertChunks(ctx, chunks); err != nil {
		t.Fatalf("InsertChunks: %v", err)
	}

	listed, err := repo.ListChunksForObject(ctx, obj.ID)
	if err != nil {
		t.Fatalf("ListChunksForObject: %v", err)
	}
	if len(listed) != 3 {
		t.Fatalf("got %d chunks, want 3", len(listed))
	}
	for i, c := range listed {
		if c.Content != chunks[i].Content {
			t.Errorf("chunk %d: content=%q, want %q", i, c.Content, chunks[i].Content)
		}
		if c.Seq != chunks[i].Seq {
			t.Errorf("chunk %d: seq=%d, want %d", i, c.Seq, chunks[i].Seq)
		}
		if c.ObjectID != obj.ID {
			t.Errorf("chunk %d: object_id=%d, want %d", i, c.ObjectID, obj.ID)
		}
	}
}

func TestInsertChunks_empty(t *testing.T) {
	ctx := context.Background()
	repo := openCebTestRepo(t)

	if err := repo.InsertChunks(ctx, nil); err != nil {
		t.Fatalf("InsertChunks(nil): %v", err)
	}
	if err := repo.InsertChunks(ctx, []repository.Chunk{}); err != nil {
		t.Fatalf("InsertChunks(empty): %v", err)
	}
}

func TestInsertChunks_embeddingRoundTrip(t *testing.T) {
	ctx := context.Background()
	repo := openCebTestRepo(t)

	obj := upsertObj(t, repo, "e", "b", "k")
	embed := []float32{0.1, 0.2, 0.3, 0.4, 0.5}
	chunks := []repository.Chunk{
		{ObjectID: obj.ID, TenantID: "e", Bucket: "b", ObjectKey: "k", Seq: 0, Content: "embed", Embedding: embed, Dim: 5, EmbedModel: "m"},
	}
	if err := repo.InsertChunks(ctx, chunks); err != nil {
		t.Fatalf("InsertChunks: %v", err)
	}

	listed, err := repo.ListChunksForObject(ctx, obj.ID)
	if err != nil {
		t.Fatalf("ListChunksForObject: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("got %d chunks, want 1", len(listed))
	}
	got := listed[0]
	if len(got.Embedding) != 5 {
		t.Fatalf("embedding length=%d, want 5", len(got.Embedding))
	}
	for i := range embed {
		if got.Embedding[i] != embed[i] {
			t.Errorf("embedding[%d]=%f, want %f", i, got.Embedding[i], embed[i])
		}
	}
	if got.Dim != 5 {
		t.Errorf("Dim=%d, want 5", got.Dim)
	}
	if got.EmbedModel != "m" {
		t.Errorf("EmbedModel=%q, want m", got.EmbedModel)
	}
}

func TestListChunksForObject(t *testing.T) {
	ctx := context.Background()
	repo := openCebTestRepo(t)

	obj1 := upsertObj(t, repo, "t1", "b", "k1")
	obj2 := upsertObj(t, repo, "t1", "b", "k2")
	chunks := []repository.Chunk{
		{ObjectID: obj1.ID, TenantID: "t1", Bucket: "b", ObjectKey: "k1", Seq: 10, Content: "tenth"},
		{ObjectID: obj1.ID, TenantID: "t1", Bucket: "b", ObjectKey: "k1", Seq: 20, Content: "twentieth"},
		{ObjectID: obj2.ID, TenantID: "t1", Bucket: "b", ObjectKey: "k2", Seq: 0, Content: "first"},
	}
	if err := repo.InsertChunks(ctx, chunks); err != nil {
		t.Fatalf("InsertChunks: %v", err)
	}

	listed, err := repo.ListChunksForObject(ctx, obj1.ID)
	if err != nil {
		t.Fatalf("ListChunksForObject: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("got %d chunks for obj1, want 2", len(listed))
	}
	if listed[0].Seq != 10 || listed[0].Content != "tenth" {
		t.Errorf("first chunk: seq=%d content=%q", listed[0].Seq, listed[0].Content)
	}
	if listed[1].Seq != 20 || listed[1].Content != "twentieth" {
		t.Errorf("second chunk: seq=%d content=%q", listed[1].Seq, listed[1].Content)
	}

	listed, err = repo.ListChunksForObject(ctx, obj2.ID)
	if err != nil {
		t.Fatalf("ListChunksForObject: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("got %d chunks for obj2, want 1", len(listed))
	}

	listed, err = repo.ListChunksForObject(ctx, 99999)
	if err != nil {
		t.Fatalf("ListChunksForObject (nonexistent): %v", err)
	}
	if len(listed) != 0 {
		t.Fatalf("got %d chunks for nonexistent obj, want 0", len(listed))
	}
}

func TestSearchChunks(t *testing.T) {
	ctx := context.Background()
	repo := openCebTestRepo(t)

	obj1 := upsertObj(t, repo, "s", "b", "a.txt")
	obj2 := upsertObj(t, repo, "s", "b", "b.txt")
	v1 := []float32{1, 0, 0}
	v2 := []float32{0, 1, 0}
	queryVec := []float32{0, 1, 0}
	chunks := []repository.Chunk{
		{ObjectID: obj1.ID, TenantID: "s", Bucket: "b", ObjectKey: "a.txt", Seq: 0, Content: "unrelated", Embedding: v1, Dim: 3, EmbedModel: "tm"},
		{ObjectID: obj2.ID, TenantID: "s", Bucket: "b", ObjectKey: "b.txt", Seq: 0, Content: "related", Embedding: v2, Dim: 3, EmbedModel: "tm"},
	}
	if err := repo.InsertChunks(ctx, chunks); err != nil {
		t.Fatalf("InsertChunks: %v", err)
	}

	hits, err := repo.SearchChunks(ctx, "s", "b", queryVec, 10)
	if err != nil {
		t.Fatalf("SearchChunks: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected at least 1 hit")
	}
	if hits[0].Chunk.ObjectID != obj2.ID {
		t.Errorf("top hit object_id=%d, want %d", hits[0].Chunk.ObjectID, obj2.ID)
	}
	if hits[0].Score < 0.99 {
		t.Errorf("top hit score=%f, want ~1.0", hits[0].Score)
	}
	if len(hits) > 1 && hits[1].Chunk.ObjectID != obj1.ID {
		t.Errorf("second hit object_id=%d, want %d", hits[1].Chunk.ObjectID, obj1.ID)
	}
}

func TestSearchChunks_limit(t *testing.T) {
	ctx := context.Background()
	repo := openCebTestRepo(t)

	embed := []float32{0.5, 0.5}
	for i := 0; i < 5; i++ {
		obj := upsertObj(t, repo, "slim", "b", "k"+string(rune('0'+i)))
		chunks := []repository.Chunk{
			{ObjectID: obj.ID, TenantID: "slim", Bucket: "b", ObjectKey: "k", Seq: 0, Content: "x", Embedding: embed, Dim: 2, EmbedModel: "m"},
		}
		if err := repo.InsertChunks(ctx, chunks); err != nil {
			t.Fatalf("InsertChunks: %v", err)
		}
	}

	hits, err := repo.SearchChunks(ctx, "slim", "b", []float32{0.5, 0.5}, 3)
	if err != nil {
		t.Fatalf("SearchChunks: %v", err)
	}
	if len(hits) > 3 {
		t.Fatalf("got %d hits, want at most 3", len(hits))
	}
}

func TestSearchChunks_emptyBucket(t *testing.T) {
	ctx := context.Background()
	repo := openCebTestRepo(t)

	obj := upsertObj(t, repo, "s", "bucket_a", "k")
	embed := []float32{1, 0, 0}
	chunks := []repository.Chunk{
		{ObjectID: obj.ID, TenantID: "s", Bucket: "bucket_a", ObjectKey: "k", Seq: 0, Content: "x", Embedding: embed, Dim: 3, EmbedModel: "m"},
	}
	if err := repo.InsertChunks(ctx, chunks); err != nil {
		t.Fatalf("InsertChunks: %v", err)
	}

	hits, err := repo.SearchChunks(ctx, "s", "bucket_b", []float32{1, 0, 0}, 10)
	if err != nil {
		t.Fatalf("SearchChunks: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("got %d hits for different bucket, want 0", len(hits))
	}
}

func TestListObjectIDsToReindex(t *testing.T) {
	ctx := context.Background()
	repo := openCebTestRepo(t)

	obj1 := upsertObj(t, repo, "ri", "b", "k1")
	obj2 := upsertObj(t, repo, "ri", "b", "k2")
	obj3 := upsertObj(t, repo, "ri", "b", "k3")
	embed := []float32{0.1, 0.2}
	chunks := []repository.Chunk{
		{ObjectID: obj1.ID, TenantID: "ri", Bucket: "b", ObjectKey: "k1", Seq: 0, Content: "old", Embedding: embed, Dim: 2, EmbedModel: "old-model"},
		{ObjectID: obj2.ID, TenantID: "ri", Bucket: "b", ObjectKey: "k2", Seq: 0, Content: "current", Embedding: embed, Dim: 2, EmbedModel: "new-model"},
		{ObjectID: obj3.ID, TenantID: "ri", Bucket: "b", ObjectKey: "k3", Seq: 0, Content: "empty", Embedding: embed, Dim: 2, EmbedModel: ""},
	}
	if err := repo.InsertChunks(ctx, chunks); err != nil {
		t.Fatalf("InsertChunks: %v", err)
	}

	ids, err := repo.ListObjectIDsToReindex(ctx, "ri", "new-model", 10)
	if err != nil {
		t.Fatalf("ListObjectIDsToReindex: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("got %d ids, want 2", len(ids))
	}
	got := make(map[int64]bool, len(ids))
	for _, id := range ids {
		got[id] = true
	}
	if !got[obj1.ID] || !got[obj3.ID] {
		t.Errorf("reindex ids=%v, want stale=%d and unknown=%d", ids, obj1.ID, obj3.ID)
	}
}

func TestListObjectIDsToReindex_limit(t *testing.T) {
	ctx := context.Background()
	repo := openCebTestRepo(t)

	for i := 0; i < 5; i++ {
		obj := upsertObj(t, repo, "ri2", "b", "k"+string(rune('0'+i)))
		chunks := []repository.Chunk{
			{ObjectID: obj.ID, TenantID: "ri2", Bucket: "b", ObjectKey: "k", Seq: 0, Content: "x", Embedding: []float32{0.1}, Dim: 1, EmbedModel: "old"},
		}
		if err := repo.InsertChunks(ctx, chunks); err != nil {
			t.Fatalf("InsertChunks: %v", err)
		}
	}

	ids, err := repo.ListObjectIDsToReindex(ctx, "ri2", "new", 2)
	if err != nil {
		t.Fatalf("ListObjectIDsToReindex: %v", err)
	}
	if len(ids) > 2 {
		t.Fatalf("got %d ids, want at most 2", len(ids))
	}
}

func TestSumAICostMicros(t *testing.T) {
	ctx := context.Background()
	repo := openCebTestRepo(t)

	usages := []repository.Usage{
		{TenantID: "cost_tnt", Caller: "search", Query: "q1", ObjectIDs: []int64{1}, RequestID: "r1", Model: "m", TotalTokens: 10, CostMicros: 1000},
		{TenantID: "cost_tnt", Caller: "search", Query: "q2", ObjectIDs: []int64{2}, RequestID: "r2", Model: "m", TotalTokens: 20, CostMicros: 2000},
		{TenantID: "other_tnt", Caller: "search", Query: "q3", ObjectIDs: []int64{3}, RequestID: "r3", Model: "m", TotalTokens: 30, CostMicros: 999999},
	}
	for _, u := range usages {
		if err := repo.RecordUsage(ctx, u); err != nil {
			t.Fatalf("RecordUsage: %v", err)
		}
	}

	total, err := repo.SumAICostMicros(ctx, "cost_tnt", "2020-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("SumAICostMicros: %v", err)
	}
	if total != 3000 {
		t.Errorf("sum=%d, want 3000", total)
	}

	total, err = repo.SumAICostMicros(ctx, "empty_tnt", "2020-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("SumAICostMicros (empty): %v", err)
	}
	if total != 0 {
		t.Errorf("empty sum=%d, want 0", total)
	}
}

func TestSumAICostMicros_defaultTenant(t *testing.T) {
	ctx := context.Background()
	repo := openCebTestRepo(t)

	if err := repo.RecordUsage(ctx, repository.Usage{
		TenantID: "", Caller: "s", Query: "q", ObjectIDs: []int64{1}, RequestID: "r", Model: "m", TotalTokens: 5, CostMicros: 500,
	}); err != nil {
		t.Fatalf("RecordUsage: %v", err)
	}
	total, err := repo.SumAICostMicros(ctx, "", "2020-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("SumAICostMicros: %v", err)
	}
	if total != 500 {
		t.Errorf("sum with empty tenant=%d, want 500", total)
	}
}

// --- Events ---

func TestInsertEvent(t *testing.T) {
	ctx := context.Background()
	repo := openCebTestRepo(t)

	objID := int64(42)
	e := repository.Event{
		TenantID: "evt", Bucket: "b", Key: "k",
		Type: repository.EventCreated, ObjectID: &objID,
		RequestID: "req-1",
		Payload:   map[string]string{"size": "100"},
	}
	id, err := repo.InsertEvent(ctx, e)
	if err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}
	if id <= 0 {
		t.Fatalf("InsertEvent id=%d, want >0", id)
	}

	events, err := repo.NextUnconsumedEvents(ctx, 10)
	if err != nil {
		t.Fatalf("NextUnconsumedEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d unconsumed events, want 1", len(events))
	}
	got := events[0]
	if got.Type != repository.EventCreated {
		t.Errorf("type=%q, want %q", got.Type, repository.EventCreated)
	}
	if got.Bucket != "b" {
		t.Errorf("bucket=%q, want b", got.Bucket)
	}
	if got.RequestID != "req-1" {
		t.Errorf("request_id=%q, want req-1", got.RequestID)
	}
	if got.ObjectID == nil || *got.ObjectID != 42 {
		t.Errorf("object_id=%v, want 42", got.ObjectID)
	}
}

func TestInsertEvent_emptyPayload(t *testing.T) {
	ctx := context.Background()
	repo := openCebTestRepo(t)

	e := repository.Event{
		TenantID: "ep", Bucket: "b", Key: "k",
		Type:    repository.EventAccessed,
		Payload: nil,
	}
	id, err := repo.InsertEvent(ctx, e)
	if err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}
	if id <= 0 {
		t.Fatalf("InsertEvent id=%d, want >0", id)
	}
}

func TestNextUnconsumedEvents(t *testing.T) {
	ctx := context.Background()
	repo := openCebTestRepo(t)

	for i := 0; i < 3; i++ {
		e := repository.Event{
			TenantID: "un", Bucket: "b", Key: "k",
			Type:    repository.EventCreated,
			Payload: map[string]string{"n": "x"},
		}
		if _, err := repo.InsertEvent(ctx, e); err != nil {
			t.Fatalf("InsertEvent %d: %v", i, err)
		}
	}

	events, err := repo.NextUnconsumedEvents(ctx, 2)
	if err != nil {
		t.Fatalf("NextUnconsumedEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
	if events[0].ID >= events[1].ID {
		t.Errorf("events not ordered by ASC id: %d >= %d", events[0].ID, events[1].ID)
	}

	if err := repo.MarkEventConsumed(ctx, events[0].ID); err != nil {
		t.Fatalf("MarkEventConsumed: %v", err)
	}
	if err := repo.MarkEventConsumed(ctx, events[1].ID); err != nil {
		t.Fatalf("MarkEventConsumed: %v", err)
	}

	remaining, err := repo.NextUnconsumedEvents(ctx, 10)
	if err != nil {
		t.Fatalf("NextUnconsumedEvents (after consume): %v", err)
	}
	if len(remaining) != 1 {
		t.Fatalf("got %d unconsumed after consume, want 1", len(remaining))
	}
}

func TestMarkEventConsumed(t *testing.T) {
	ctx := context.Background()
	repo := openCebTestRepo(t)

	e := repository.Event{
		TenantID: "mc", Bucket: "b", Key: "k",
		Type:    repository.EventDeleted,
		Payload: map[string]string{},
	}
	id, err := repo.InsertEvent(ctx, e)
	if err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}

	if err := repo.MarkEventConsumed(ctx, id); err != nil {
		t.Fatalf("MarkEventConsumed: %v", err)
	}

	events, err := repo.NextUnconsumedEvents(ctx, 10)
	if err != nil {
		t.Fatalf("NextUnconsumedEvents: %v", err)
	}
	for _, ev := range events {
		if ev.ID == id {
			t.Fatalf("consumed event %d still returned", id)
		}
	}
}

func TestListEventsAfterIncludesConsumedTenantHistory(t *testing.T) {
	ctx := context.Background()
	repo := openCebTestRepo(t)

	firstID, err := repo.InsertEvent(ctx, repository.Event{
		TenantID: "history", Bucket: "b", Key: "first", Type: repository.EventCreated,
	})
	if err != nil {
		t.Fatalf("InsertEvent first: %v", err)
	}
	if _, err := repo.InsertEvent(ctx, repository.Event{
		TenantID: "other", Bucket: "b", Key: "hidden", Type: repository.EventCreated,
	}); err != nil {
		t.Fatalf("InsertEvent other tenant: %v", err)
	}
	lastID, err := repo.InsertEvent(ctx, repository.Event{
		TenantID: "history", Bucket: "b", Key: "last", Type: repository.EventDeleted,
	})
	if err != nil {
		t.Fatalf("InsertEvent last: %v", err)
	}
	if err := repo.MarkEventConsumed(ctx, firstID); err != nil {
		t.Fatalf("MarkEventConsumed: %v", err)
	}

	got, err := repo.ListEventsAfter(ctx, "history", 0, 10)
	if err != nil {
		t.Fatalf("ListEventsAfter: %v", err)
	}
	if len(got) != 2 || got[0].ID != firstID || got[1].ID != lastID {
		t.Fatalf("events = %+v, want ordered tenant history [%d %d]", got, firstID, lastID)
	}

	got, err = repo.ListEventsAfter(ctx, "history", firstID, 1)
	if err != nil {
		t.Fatalf("ListEventsAfter page: %v", err)
	}
	if len(got) != 1 || got[0].ID != lastID {
		t.Fatalf("page = %+v, want event %d", got, lastID)
	}
}

// --- Buckets ---

func TestSetBucketVersioning(t *testing.T) {
	ctx := context.Background()
	repo := openCebTestRepo(t)

	if err := repo.CreateBucket(ctx, "v", "vb"); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}

	cfg, err := repo.GetBucketConfig(ctx, "v", "vb")
	if err != nil {
		t.Fatalf("GetBucketConfig: %v", err)
	}
	if cfg.Versioning {
		t.Errorf("initial versioning=true, want false")
	}

	if err := repo.SetBucketVersioning(ctx, "v", "vb", true); err != nil {
		t.Fatalf("SetBucketVersioning: %v", err)
	}
	cfg, err = repo.GetBucketConfig(ctx, "v", "vb")
	if err != nil {
		t.Fatalf("GetBucketConfig: %v", err)
	}
	if !cfg.Versioning {
		t.Errorf("after enable versioning=false, want true")
	}

	if err := repo.SetBucketVersioning(ctx, "v", "vb", false); err != nil {
		t.Fatalf("SetBucketVersioning(false): %v", err)
	}
	cfg, err = repo.GetBucketConfig(ctx, "v", "vb")
	if err != nil {
		t.Fatalf("GetBucketConfig: %v", err)
	}
	if cfg.Versioning {
		t.Errorf("after disable versioning=true, want false")
	}
}

func TestSetBucketObjectLock(t *testing.T) {
	ctx := context.Background()
	repo := openCebTestRepo(t)

	if err := repo.CreateBucket(ctx, "l", "lb"); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}

	cfg, err := repo.GetBucketConfig(ctx, "l", "lb")
	if err != nil {
		t.Fatalf("GetBucketConfig: %v", err)
	}
	if cfg.ObjectLockSeconds != 0 {
		t.Errorf("initial lock_seconds=%d, want 0", cfg.ObjectLockSeconds)
	}

	if err := repo.SetBucketObjectLock(ctx, "l", "lb", 86400); err != nil {
		t.Fatalf("SetBucketObjectLock: %v", err)
	}
	cfg, err = repo.GetBucketConfig(ctx, "l", "lb")
	if err != nil {
		t.Fatalf("GetBucketConfig: %v", err)
	}
	if cfg.ObjectLockSeconds != 86400 {
		t.Errorf("lock_seconds=%d, want 86400", cfg.ObjectLockSeconds)
	}
}

// --- Objects ---

func TestGetObjectByID(t *testing.T) {
	ctx := context.Background()
	repo := openCebTestRepo(t)

	obj := upsertObj(t, repo, "byid", "b", "k")

	got, err := repo.GetObjectByID(ctx, obj.ID)
	if err != nil {
		t.Fatalf("GetObjectByID: %v", err)
	}
	if got.Bucket != "b" {
		t.Errorf("bucket=%q, want b", got.Bucket)
	}
	if got.Key != "k" {
		t.Errorf("key=%q, want k", got.Key)
	}
	if got.Size != 100 {
		t.Errorf("size=%d, want 100", got.Size)
	}
	if got.ETag != `"abc123"` {
		t.Errorf("etag=%q", got.ETag)
	}
	if got.Backend != "local" {
		t.Errorf("backend=%q, want local", got.Backend)
	}

	_, err = repo.GetObjectByID(ctx, 99999)
	if err != repository.ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestGetObjectByID_softDeleted(t *testing.T) {
	ctx := context.Background()
	repo := openCebTestRepo(t)

	obj := upsertObj(t, repo, "byid", "b", "k2")
	if err := repo.SoftDeleteObject(ctx, "byid", "b", "k2"); err != nil {
		t.Fatalf("SoftDeleteObject: %v", err)
	}

	got, err := repo.GetObjectByID(ctx, obj.ID)
	if err != nil {
		t.Fatalf("GetObjectByID on soft-deleted: %v", err)
	}
	if got.ID != obj.ID {
		t.Errorf("got id=%d, want %d", got.ID, obj.ID)
	}
	if got.DeletedAt == nil {
		t.Errorf("expected DeletedAt non-nil for soft-deleted object")
	}
}

func TestGetObjectVersion(t *testing.T) {
	ctx := context.Background()
	repo := openCebTestRepo(t)

	obj1 := upsertObj(t, repo, "v", "b", "k")
	vid1 := obj1.VersionID

	obj2, err := repo.InsertObjectVersion(ctx, repository.Object{
		TenantID: "v", Bucket: "b", Key: "k",
		Backend: "local", StorageKey: "test/k", Size: 200,
		ETag: `"def456"`, ContentType: "text/plain",
	})
	if err != nil {
		t.Fatalf("InsertObjectVersion: %v", err)
	}
	vid2 := obj2.VersionID

	got1, err := repo.GetObjectVersion(ctx, "v", "b", "k", vid1)
	if err != nil {
		t.Fatalf("GetObjectVersion (v1): %v", err)
	}
	if got1.ID != obj1.ID {
		t.Errorf("v1 id=%d, want %d", got1.ID, obj1.ID)
	}
	if got1.Size != 100 {
		t.Errorf("v1 size=%d, want 100", got1.Size)
	}

	got2, err := repo.GetObjectVersion(ctx, "v", "b", "k", vid2)
	if err != nil {
		t.Fatalf("GetObjectVersion (v2): %v", err)
	}
	if got2.Size != 200 {
		t.Errorf("v2 size=%d, want 200", got2.Size)
	}

	_, err = repo.GetObjectVersion(ctx, "v", "b", "k", "nonexistent")
	if err != repository.ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestListObjects(t *testing.T) {
	ctx := context.Background()
	repo := openCebTestRepo(t)

	if err := repo.CreateBucket(ctx, "l", "lb"); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	keys := []string{"a/1.txt", "a/2.txt", "b/1.txt", "c/1.txt"}
	for _, k := range keys {
		upsertObj(t, repo, "l", "lb", k)
	}

	page, err := repo.ListObjects(ctx, "l", "lb", "a/", "", 10)
	if err != nil {
		t.Fatalf("ListObjects: %v", err)
	}
	if len(page.Objects) != 2 {
		t.Fatalf("prefix a/: got %d objects, want 2", len(page.Objects))
	}

	page, err = repo.ListObjects(ctx, "l", "lb", "a/", "a/1.txt", 10)
	if err != nil {
		t.Fatalf("ListObjects with marker: %v", err)
	}
	if len(page.Objects) != 1 {
		t.Fatalf("after marker: got %d objects, want 1", len(page.Objects))
	}
	if page.Objects[0].Key != "a/2.txt" {
		t.Errorf("expected a/2.txt, got %q", page.Objects[0].Key)
	}

	page, err = repo.ListObjects(ctx, "l", "lb", "", "", 2)
	if err != nil {
		t.Fatalf("ListObjects limit: %v", err)
	}
	if len(page.Objects) != 2 {
		t.Fatalf("limit 2: got %d objects", len(page.Objects))
	}
	if !page.HasMore {
		t.Errorf("expected HasMore=true")
	}
	if page.NextMarker == "" {
		t.Errorf("expected NextMarker")
	}
}

func TestListObjectPrefixesTreatSQLWildcardsLiterally(t *testing.T) {
	ctx := context.Background()
	repo := openCebTestRepo(t)
	const tenant, bucket = "literal-prefix", "objects"
	for _, key := range []string{
		"under_score.txt", "underXscore.txt",
		"percent%value.txt", "percentXvalue.txt",
		"bang!value.txt",
	} {
		upsertObj(t, repo, tenant, bucket, key)
	}

	assertListedKeys(t, repo, tenant, bucket, "under_", []string{"under_score.txt"})
	assertListedKeys(t, repo, tenant, bucket, "percent%", []string{"percent%value.txt"})
	assertListedKeys(t, repo, tenant, bucket, "bang!", []string{"bang!value.txt"})

	if err := repo.SoftDeleteObject(ctx, tenant, bucket, "percent%value.txt"); err != nil {
		t.Fatal(err)
	}
	deleted, err := repo.ListDeletedObjects(ctx, tenant, bucket, "percent%", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(deleted.Objects) != 1 || deleted.Objects[0].Key != "percent%value.txt" {
		t.Fatalf("deleted prefix result=%v, want percent%%value.txt", objectKeys(deleted.Objects))
	}

	versionKeys, _, _, err := repo.ListObjectVersionKeys(
		ctx, tenant, bucket, "under_", "", 10,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(versionKeys) != 1 || versionKeys[0] != "under_score.txt" {
		t.Fatalf("version prefix result=%v, want under_score.txt", versionKeys)
	}
}

func assertListedKeys(
	t *testing.T,
	repo repository.Repository,
	tenant, bucket, prefix string,
	want []string,
) {
	t.Helper()
	page, err := repo.ListObjects(context.Background(), tenant, bucket, prefix, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	got := objectKeys(page.Objects)
	if len(got) != len(want) {
		t.Fatalf("prefix %q result=%v, want %v", prefix, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("prefix %q result=%v, want %v", prefix, got, want)
		}
	}
}

func objectKeys(objects []repository.Object) []string {
	keys := make([]string, 0, len(objects))
	for _, object := range objects {
		keys = append(keys, object.Key)
	}
	return keys
}

func TestListObjects_softDeletedExcluded(t *testing.T) {
	ctx := context.Background()
	repo := openCebTestRepo(t)

	if err := repo.CreateBucket(ctx, "l", "lb"); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	upsertObj(t, repo, "l", "lb", "visible.txt")
	obj2 := upsertObj(t, repo, "l", "lb", "hidden.txt")
	if err := repo.SoftDeleteObject(ctx, "l", "lb", "hidden.txt"); err != nil {
		t.Fatalf("SoftDeleteObject: %v", err)
	}

	page, err := repo.ListObjects(ctx, "l", "lb", "", "", 10)
	if err != nil {
		t.Fatalf("ListObjects: %v", err)
	}
	if len(page.Objects) != 1 {
		t.Fatalf("got %d objects, want 1 (soft-deleted excluded)", len(page.Objects))
	}
	if page.Objects[0].Key != "visible.txt" {
		t.Errorf("expected visible.txt, got %q", page.Objects[0].Key)
	}
	_ = obj2
}

func TestHardDeleteObject(t *testing.T) {
	ctx := context.Background()
	repo := openCebTestRepo(t)

	upsertObj(t, repo, "hd", "b", "k")
	if err := repo.HardDeleteObject(ctx, "hd", "b", "k"); err != nil {
		t.Fatalf("HardDeleteObject: %v", err)
	}

	_, err := repo.GetObject(ctx, "hd", "b", "k")
	if err != repository.ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestHardDeleteObject_nonexistent(t *testing.T) {
	ctx := context.Background()
	repo := openCebTestRepo(t)

	if err := repo.HardDeleteObject(ctx, "hd", "b", "nonexistent"); err != nil {
		t.Fatalf("HardDeleteObject nonexistent: %v", err)
	}
}

// --- Quota ---

func TestListTenantQuotas(t *testing.T) {
	ctx := context.Background()
	repo := openCebTestRepo(t)

	quotas, err := repo.ListTenantQuotas(ctx)
	if err != nil {
		t.Fatalf("ListTenantQuotas: %v", err)
	}
	if len(quotas) != 0 {
		t.Fatalf("got %d quotas initially, want 0", len(quotas))
	}

	if err := repo.SetTenantQuota(ctx, "t1", 1000, 10); err != nil {
		t.Fatalf("SetTenantQuota t1: %v", err)
	}
	if err := repo.SetTenantQuota(ctx, "t2", 2000, 20); err != nil {
		t.Fatalf("SetTenantQuota t2: %v", err)
	}
	if _, err := repo.AddTenantUsage(ctx, "t3", 500, 5); err != nil {
		t.Fatalf("AddTenantUsage t3: %v", err)
	}

	quotas, err = repo.ListTenantQuotas(ctx)
	if err != nil {
		t.Fatalf("ListTenantQuotas: %v", err)
	}
	if len(quotas) != 3 {
		t.Fatalf("got %d quotas, want 3", len(quotas))
	}

	for _, q := range quotas {
		switch q.TenantID {
		case "t1":
			if q.MaxBytes != 1000 || q.MaxObjects != 10 {
				t.Errorf("t1: MaxBytes=%d MaxObjects=%d", q.MaxBytes, q.MaxObjects)
			}
		case "t2":
			if q.MaxBytes != 2000 || q.MaxObjects != 20 {
				t.Errorf("t2: MaxBytes=%d MaxObjects=%d", q.MaxBytes, q.MaxObjects)
			}
		case "t3":
			if q.UsedBytes != 500 || q.UsedObjects != 5 {
				t.Errorf("t3: UsedBytes=%d UsedObjects=%d", q.UsedBytes, q.UsedObjects)
			}
		default:
			t.Errorf("unexpected tenant %q", q.TenantID)
		}
	}
}

func TestListTenantQuotas_nonEmptySlice(t *testing.T) {
	ctx := context.Background()
	repo := openCebTestRepo(t)

	quotas, err := repo.ListTenantQuotas(ctx)
	if err != nil {
		t.Fatalf("ListTenantQuotas: %v", err)
	}
	if quotas == nil {
		t.Fatal("ListTenantQuotas returned nil, want non-nil empty slice")
	}
}

// --- Jobs ---

func TestCountJobsByStatus(t *testing.T) {
	ctx := context.Background()
	repo := openCebTestRepo(t)

	n, err := repo.CountJobsByStatus(ctx, repository.JobPending)
	if err != nil {
		t.Fatalf("CountJobsByStatus: %v", err)
	}
	if n != 0 {
		t.Fatalf("initial pending=%d, want 0", n)
	}

	for i := 0; i < 3; i++ {
		if _, _, err := repo.EnqueueJob(ctx, repository.Job{Type: "test"}); err != nil {
			t.Fatalf("EnqueueJob %d: %v", i, err)
		}
	}

	n, err = repo.CountJobsByStatus(ctx, repository.JobPending)
	if err != nil {
		t.Fatalf("CountJobsByStatus pending: %v", err)
	}
	if n != 3 {
		t.Fatalf("pending count=%d, want 3", n)
	}

	n, err = repo.CountJobsByStatus(ctx, repository.JobRunning)
	if err != nil {
		t.Fatalf("CountJobsByStatus running: %v", err)
	}
	if n != 0 {
		t.Fatalf("running count=%d, want 0", n)
	}

	n, err = repo.CountJobsByStatus(ctx, repository.JobSucceeded)
	if err != nil {
		t.Fatalf("CountJobsByStatus succeeded: %v", err)
	}
	if n != 0 {
		t.Fatalf("succeeded count=%d, want 0", n)
	}

	_, ok, err := repo.ClaimJob(ctx, "w")
	if err != nil || !ok {
		t.Fatalf("ClaimJob: ok=%v err=%v", ok, err)
	}

	n, err = repo.CountJobsByStatus(ctx, repository.JobRunning)
	if err != nil {
		t.Fatalf("CountJobsByStatus running: %v", err)
	}
	if n != 1 {
		t.Fatalf("running count=%d, want 1", n)
	}

	n, err = repo.CountJobsByStatus(ctx, repository.JobPending)
	if err != nil {
		t.Fatalf("CountJobsByStatus pending: %v", err)
	}
	if n != 2 {
		t.Fatalf("pending count=%d, want 2", n)
	}
}
